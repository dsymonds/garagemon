package main

import (
	"bytes"
	"context"
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

	rpio "github.com/stianeikeland/go-rpio/v4"
	"tailscale.com/client/tailscale"
	"tailscale.com/tailcfg"

	_ "embed"
)

var (
	actionPin       = flag.Int("action_pin", 22, "`pin` (raw BCM2835 pinout) for action")
	actionActiveLow = flag.Bool("action_active_low", false, "whether the action pin is active low")
	blinkLED        = flag.Bool("blink_led", true, "whether to background blink the LED")

	ledPath      = flag.String("led_path", "", "`path` to built-in LED; leave empty for no LED")
	httpFlag     = flag.String("http", "localhost:8080", "`address` on which to serve HTTP")
	netInterface = flag.String("net_interface", "", "if set, serve HTTP *only* on the named interface")
)

func main() {
	flag.Parse()
	log.Printf("garagemon starting...")
	time.Sleep(500 * time.Millisecond)

	if *httpFlag != "" && *netInterface != "" {
		var err error
		*httpFlag, err = restrictAddrToInterface(*httpFlag, *netInterface)
		if err != nil {
			log.Fatal(err)
		}
	}

	s := &server{
		action: rpio.Pin(*actionPin),
		led:    *ledPath,
	}
	if err := s.Init(); err != nil {
		log.Fatal(err)
	}
	http.Handle("/", s)

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
	if *blinkLED {
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

func restrictAddrToInterface(origAddr, ifaceName string) (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("getting network interfaces: %v", err)
	}
	var addrs []net.Addr
	for _, iface := range ifaces {
		if iface.Name != ifaceName {
			continue
		}
		addrs, err = iface.Addrs()
		if err != nil {
			return "", fmt.Errorf("getting network addresses for interface %q: %v", iface.Name, err)
		}
		break
	}
	if addrs == nil {
		return "", fmt.Errorf("unknown or address-free network interface %q", ifaceName)
	}
	var ip net.IP
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip = ipn.IP.To4() // pick out the IPv4 address
		if ip != nil {
			break
		}
	}
	if ip == nil {
		return "", fmt.Errorf("network interface %q does not have any IPv4 addresses", ifaceName)
	}

	_, port, err := net.SplitHostPort(origAddr)
	if err != nil {
		return "", fmt.Errorf("splitting %q: %v", origAddr, err)
	}
	addr := net.JoinHostPort(ip.String(), port)

	log.Printf("Restricted %q to interface %q as %q", origAddr, ifaceName, addr)
	return addr, nil
}

type server struct {
	action rpio.Pin
	led    string // path like "/sys/class/leds/led0"
}

func (s *server) Init() error {
	if err := rpio.Open(); err != nil {
		return fmt.Errorf("opening memory range for GPIO access: %v", err)
	}
	s.action.Output()
	s.actionWrite(false)

	// Enable manual control of built-in LED.
	if err := s.ledWrite("trigger", "gpio"); err != nil {
		return fmt.Errorf("setting up manual control of built-in LED: %w", err)
	}

	return nil
}

func (s *server) Shutdown() {
	s.actionWrite(false)
	rpio.Close()
}

func (s *server) actionWrite(active bool) {
	if *actionActiveLow {
		active = !active
	}
	if active {
		s.action.High()
	} else {
		s.action.Low()
	}
}

func (s *server) ledWrite(file, contents string) error {
	if s.led == "" {
		return nil
	}
	full := filepath.Join(s.led, file)
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
		serveFront(w, r)
	case "/activate":
		if r.Method != "POST" {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		s.Activate()
		io.WriteString(w, "{}")
	}
}

func serveFront(w http.ResponseWriter, r *http.Request) {
	data := struct {
		Uptime string
		Peer   *tailcfg.UserProfile // may be nil
	}{
		Uptime: uptime(time.Since(startTime)),
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
