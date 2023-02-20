# Garagemon

This is a program to run on a Raspberry Pi for controlling a garage door.

For me, my garage door opener can be triggered by closing a circuit, so a simple relay module
is effective.

My specific hardware:

   * Raspberry Pi Zero WH
   * 3 Channel Relay Module Shield Smart Home for Raspberry Pi (Waveshare)
   * Aussie Openers 800N

To configure for that, fill in a `garagemon.yaml` file like this:

```yaml
mode: hardware

hardware:
  action_pin: 26
  action_active_low: true
  led_path: /sys/class/leds/led0
  blink_led: true
```

See `main.go:Config` for documentation on the structure and details.

## Manual use

If the program breaks like

```
setting up manual control of built-in LED: open /sys/class/leds/led0/trigger: permission denied
```

then you'll want to permit non-root access to the built-in LED. Run `led-setup.sh`.

## systemd Automation

To have this run all the time from boot, customise `garagemon.service` and then

```
sudo cp garagemon.service /lib/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable garagemon.service
sudo systemctl start garagemon.service
```

## Tailscale

If using [Tailscale](https://tailscale.com/) on the Raspberry Pi, pass
`-net_interface=tailscale0` so HTTP is served only on that interface, isolating
it from the local network.
