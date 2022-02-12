# Garagemon

This is a program to run on a Raspberry Pi for controlling a garage door.

For me, my garage door opener can be triggered by closing a circuit, so a simple relay module
is effective.

My specific hardware:

   * Raspberry Pi Zero WH
   * 3 Channel Relay Module Shield Smart Home for Raspberry Pi (Waveshare)
   * Aussie Openers 800N

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
