#!/bin/sh
set -eu

read_secret() {
  path=$1
  if [ ! -r "$path" ]; then
    echo "required restricted secret is unavailable" >&2
    exit 1
  fi
  IFS= read -r value < "$path" || true
  if [ -z "$value" ]; then
    echo "required restricted secret is empty" >&2
    exit 1
  fi
  printf '%s' "$value"
}

# Operator binaries run without MQTT credentials, but still receive the
# application DB credential through the same restricted host-managed mount.
if [ "$#" -gt 0 ]; then
  if [ -z "${PGPASSWORD:-}" ]; then
    export PGPASSWORD="$(read_secret /run/poweriot/secrets/application-db-password)"
  fi
  exec "$@"
fi

if [ -z "${PGPASSWORD:-}" ]; then
  export PGPASSWORD="$(read_secret /run/poweriot/secrets/application-db-password)"
fi
export MQTT_USERNAME="$(read_secret /run/poweriot/secrets/mqtt-username)"
export MQTT_PASSWORD="$(read_secret /run/poweriot/secrets/mqtt-password)"
exec /usr/local/bin/power-iot-server "$@"
