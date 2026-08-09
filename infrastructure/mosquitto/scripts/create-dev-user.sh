#!/usr/bin/env sh
set -eu

USERNAME=${1:-dev-user}
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
CONFIG_DIR="$ROOT/infrastructure/mosquitto/config"
PASSWORD_FILE="$CONFIG_DIR/password_file"
ACL_FILE="$CONFIG_DIR/acl"

if ! command -v mosquitto_passwd >/dev/null 2>&1; then
  echo "mosquitto_passwd is required (install Mosquitto tools first)" >&2
  exit 1
fi
if [ ! -f "$ACL_FILE" ]; then
  cp "$CONFIG_DIR/acl.example" "$ACL_FILE"
fi
printf 'Password for %s (not saved in Git): ' "$USERNAME" >&2
stty -echo
IFS= read -r PASSWORD
stty echo
printf '\n' >&2
if [ -z "$PASSWORD" ]; then echo "password must not be empty" >&2; exit 2; fi
umask 077
if [ -f "$PASSWORD_FILE" ]; then
  mosquitto_passwd -b "$PASSWORD_FILE" "$USERNAME" "$PASSWORD"
else
  mosquitto_passwd -c -b "$PASSWORD_FILE" "$USERNAME" "$PASSWORD"
fi
unset PASSWORD
printf 'Created local Mosquitto user %s in %s\n' "$USERNAME" "$PASSWORD_FILE"
