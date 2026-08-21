#!/bin/sh
set -eu

read_secret() {
  path=$1
  if [ ! -r "$path" ]; then
    echo "required restricted provider secret is unavailable" >&2
    exit 1
  fi
  IFS= read -r value < "$path" || true
  if [ -z "$value" ]; then
    echo "required restricted provider secret is empty" >&2
    exit 1
  fi
  printf '%s' "$value"
}

# The provider DSN deliberately contains no password. pgx receives the
# provider-only credential from this read-only host-managed file.
export PGPASSWORD="$(read_secret /run/poweriot/secrets/provider-db-password)"
exec /usr/local/bin/d1l-authority "$@"
