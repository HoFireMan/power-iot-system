#!/bin/sh
set -eu

# Host-installed immutable D6 drain artifact. Production installs the
# architecture-specific binary from the validated package; this wrapper never
# compiles on tcrfid01.
binary=${D6_DRAIN_BINARY:-/opt/poweriot/d6-drain-linux-amd64}
[ -x "$binary" ] || { echo "validated d6-drain binary is missing: $binary" >&2; exit 2; }
exec "$binary" "$@"
