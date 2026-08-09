#!/usr/bin/env sh
set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  echo "usage: $0 <development-lan-ip> [dns-name]" >&2
  exit 2
fi

LAN_IP=$1
DNS_NAME=${2:-}
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
CERT_DIR="$ROOT/infrastructure/mosquitto/certs"
OTA_CERT_DIR="$ROOT/infrastructure/firmware/certs"
FIRMWARE_CA_DIR="$ROOT/firmware/esp8266-power-meter/device_v1/data"
mkdir -p "$CERT_DIR" "$OTA_CERT_DIR" "$FIRMWARE_CA_DIR"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
cat > "$TMP/server-ext.cnf" <<EOF
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=IP:$LAN_IP$(if [ -n "$DNS_NAME" ]; then printf ',DNS:%s' "$DNS_NAME"; fi)
EOF

openssl genrsa -out "$CERT_DIR/ca.key" 4096
openssl req -x509 -new -nodes -key "$CERT_DIR/ca.key" -sha256 -days 1825 \
  -subj "/CN=Power IoT Development CA" -out "$CERT_DIR/ca.crt"

serial_args="-CAserial $CERT_DIR/ca.srl -CAcreateserial"
for name in server ota; do
  out_dir=$CERT_DIR
  [ "$name" = ota ] && out_dir=$OTA_CERT_DIR
  openssl genrsa -out "$out_dir/$name.key" 2048
  openssl req -new -key "$out_dir/$name.key" -subj "/CN=${DNS_NAME:-$LAN_IP}" -out "$TMP/$name.csr"
  # shellcheck disable=SC2086
  openssl x509 -req -in "$TMP/$name.csr" -CA "$CERT_DIR/ca.crt" -CAkey "$CERT_DIR/ca.key" \
    $serial_args -out "$out_dir/$name.crt" -days 825 -sha256 -extfile "$TMP/server-ext.cnf"
  serial_args="-CAserial $CERT_DIR/ca.srl"
done

# Only the public CA is staged for firmware. The private keys remain local.
cp "$CERT_DIR/ca.crt" "$FIRMWARE_CA_DIR/ca.pem"
chmod 600 "$CERT_DIR"/*.key "$OTA_CERT_DIR"/*.key
printf 'Generated development CA and SAN certificates for %s%s\n' "$LAN_IP" "${DNS_NAME:+ ($DNS_NAME)}"
printf 'Public firmware CA: %s\n' "$FIRMWARE_CA_DIR/ca.pem"
