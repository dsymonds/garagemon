package main

import (
	"bytes"
	"context"
	"embed"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dsymonds/netutil"
	rpio "github.com/stianeikeland/go-rpio/v4"
	"gopkg.in/yaml.v2"
	"tailscale.com/client/tailscale"
	"tailscale.com/tailcfg"
)

var (
	configFile   = flag.String("config_file", "garagemon.yaml", "configuration `filename`")
	httpFlag     = flag.String("http", "localhost:8080", "`address` on which to serve HTTP")
	netInterface = flag.String("net_interface", "", "if set, serve HTTP *only* on the named interface")
)

var _ = yaml.Unmarshal

type Config struct {
	// Mode is one of "hardware" or "hubitat".
	//
	// Hardware mode is where this is running on a Raspberry Pi with a GPIO-controlled relay
	// for the garage door activation.
	//
	// Hubitat mode is where this is running somewhere and can talk to a Hubitat instance
	// with the Maker API enabled, and a configured device that can activate the garage door.
	//
	// Only "hardware" works right now.
	Mode string `yaml:"mode"`

	Hardware struct {
		ActionPin       int    `yaml:"action_pin"`        // pin (raw BCM2835 pinout) for action
		ActionActiveLow bool   `yaml:"action_active_low"` // whether the action pin is active low
		LEDPath         string `yaml:"led_path"`          // path to built-in LED; leave empty for no LED
		BlinkLED        bool   `yaml:"blink_led"`         // whether to background blink the LED
	} `yaml:"hardware"`

	Hubitat struct {
		// TODO
	} `yaml:"hubitat"`
}

func main() {
	flag.Parse()

	var config Config
	configRaw, err := ioutil.ReadFile(*configFile)
	if err != nil {
		log.Fatalf("Reading config file %s: %v", *configFile, err)
	}
	if err := yaml.UnmarshalStrict(configRaw, &config); err != nil {
		log.Fatalf("Parsing config from %s: %v", *configFile, err)
	}

	log.Printf("garagemon starting...")
	time.Sleep(500 * time.Millisecond)

	if *httpFlag != "" && *netInterface != "" {
		addr, err := netutil.RestrictAddrToInterface(*httpFlag, *netInterface)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("Restricted %q to interface %q as %q", *httpFlag, *netInterface, addr)
		*httpFlag = addr
	}

	s, err := NewServer(config)
	if err != nil {
		log.Fatal(err)
	}
	http.Handle("/", s)
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFiles))))

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())

	// Handle signals.
	go func() {
		sigc := make(chan os.Signal, 1)
		signal.Notify(sigc, os.Interrupt) // TODO: others?

		sig := <-sigc
		log.Printf("Caught signal %v; shutting down gracefully", sig)
		cancel()
	}()

	// Start HTTP server.
	httpServer := &http.Server{}
	wg.Add(1)
	go func() {
		defer wg.Done()

		l, err := net.Listen("tcp", *httpFlag)
		if err != nil {
			log.Printf("net.Listen(_, %q): %v", *httpFlag, err)
			cancel()
		}

		log.Printf("Serving HTTP on %s", l.Addr())
		err = httpServer.Serve(l)
		if err != http.ErrServerClosed {
			log.Printf("http.Serve: %v", err)
			cancel()
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()

		<-ctx.Done()
		httpServer.Shutdown(context.Background())
	}()

	// Wait a bit. If things are still okay, consider this a successful startup.
	select {
	case <-ctx.Done():
		goto exit
	case <-time.After(3 * time.Second):
	}

	log.Printf("garagemon startup OK")
	s.StartupBlink()
	time.Sleep(1 * time.Second)

	// Background blinking light.
	// TODO: This is now poorly layered.
	if config.Mode == "hardware" && config.Hardware.BlinkLED {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Blink(ctx)
		}()
	}

exit:
	<-ctx.Done()
	wg.Wait()
	s.Shutdown()
	log.Printf("garagemon done")
}

type server struct {
	cfg Config

	// hardware mode only
	action rpio.Pin
}

func NewServer(cfg Config) (*server, error) {
	s := &server{
		cfg: cfg,
	}
	if cfg.Mode == "hardware" {
		s.action = rpio.Pin(cfg.Hardware.ActionPin)

		if err := rpio.Open(); err != nil {
			return nil, fmt.Errorf("opening memory range for GPIO access: %v", err)
		}
		s.action.Output()
		s.actionWrite(false)

		// Enable manual control of built-in LED.
		if err := s.ledWrite("trigger", "gpio"); err != nil {
			return nil, fmt.Errorf("setting up manual control of built-in LED: %w", err)
		}
	} else {
		return nil, fmt.Errorf("unsupported mode %q", cfg.Mode)
	}

	return s, nil
}

func (s *server) Shutdown() {
	if s.cfg.Mode == "hardware" {
		s.actionWrite(false)
		rpio.Close()
	}
}

func (s *server) actionWrite(active bool) {
	if s.cfg.Hardware.ActionActiveLow {
		active = !active
	}
	if active {
		s.action.High()
	} else {
		s.action.Low()
	}
}

func (s *server) ledWrite(file, contents string) error {
	if s.cfg.Mode != "hardware" || s.cfg.Hardware.LEDPath == "" {
		return nil
	}
	full := filepath.Join(s.cfg.Hardware.LEDPath, file)
	return ioutil.WriteFile(full, []byte(contents), 0666)
}

func (s *server) SetLED(on bool) {
	contents := "0"
	if on {
		contents = "255"
	}
	if err := s.ledWrite("brightness", contents); err != nil {
		log.Printf("Setting LED state: %v", err)
	}
}

func (s *server) StartupBlink() {
	for i := 0; i < 4; i++ {
		s.SetLED(true)
		time.Sleep(100 * time.Millisecond)
		s.SetLED(false)
		time.Sleep(100 * time.Millisecond)
	}
}

func (s *server) Blink(ctx context.Context) {
	for ctx.Err() == nil {
		s.SetLED(true)
		time.Sleep(1200 * time.Millisecond)
		s.SetLED(false)
		time.Sleep(1200 * time.Millisecond)
	}
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	default:
		http.NotFound(w, r)
		return
	case "/":
		s.serveFront(w, r)
	case "/activate":
		if r.Method != "POST" {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		s.Activate()
		io.WriteString(w, "{}")
	}
}

func (s *server) serveFront(w http.ResponseWriter, r *http.Request) {
	data := struct {
		Uptime string
		Config Config
		Peer   *tailcfg.UserProfile // may be nil
	}{
		Uptime: uptime(time.Since(startTime)),
		Config: s.cfg,
	}

	// See if we can identify the peer.
	if whois, err := tailscale.WhoIs(r.Context(), r.RemoteAddr); err == nil {
		data.Peer = whois.UserProfile
	}

	var buf bytes.Buffer
	if err := frontHTMLTmpl.Execute(&buf, data); err != nil {
		log.Printf("Executing template: %v", err)
		http.Error(w, "Internal error executing template: "+err.Error(), 500)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.Copy(w, &buf)
}

var startTime = time.Now()

var timeUnits = []struct {
	u time.Duration
	s string
}{
	// These must be in decreasing order.
	{24 * time.Hour, "d"},
	{time.Hour, "h"},
	{time.Minute, "m"},
	{time.Second, "s"},
}

func uptime(x time.Duration) string {
	// Different to x.String() to make it more human.
	// Render the first two non-zero units from timeUnits.
	if x <= 0 {
		return "0"
	}
	var parts []string
	for _, tu := range timeUnits {
		if x < tu.u && len(parts) == 0 {
			continue
		}
		n := x / tu.u
		parts = append(parts, fmt.Sprintf("%d%s", n, tu.s))
		x -= n * tu.u
		if len(parts) == 2 {
			break
		}
	}
	return strings.Join(parts, "")
}

func (s *server) Activate() {
	log.Printf("Activating!")

	s.actionWrite(true)
	time.Sleep(500 * time.Millisecond)
	s.actionWrite(false)
	time.Sleep(200 * time.Millisecond)
}

//go:embed front.html.tmpl
var frontHTML string

var frontHTMLTmpl = template.Must(template.New("front").Parse(frontHTML))

//go:embed *.png
var staticFiles embed.FS
