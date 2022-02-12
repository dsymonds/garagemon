#! /bin/sh

set -e

LED_PATH=/sys/class/leds/led0
if [ ! -w ${LED_PATH}/brightness ]; then
  echo 1>&2 "Fixing permissions for $LED_PATH ..."
  sudo chgrp gpio ${LED_PATH}/trigger ${LED_PATH}/brightness
  sudo chmod g+w ${LED_PATH}/trigger ${LED_PATH}/brightness
fi

echo 1>&2 "I think it's okay"
