#!/usr/bin/env bash
set -Eeuo pipefail

# Slice 5 local system E2E. This harness intentionally drives the production
# simulator binary through a real TLS MQTT broker, the real Backend process,
# and the real PostgreSQL/TimescaleDB migrations and persistence path.

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
COMPOSE=(docker compose -f "$ROOT/infrastructure/docker-compose.yml")
RUN_ID=$(date -u +%Y%m%d%H%M%S)-$$
TMP=$(mktemp -d "${TMPDIR:-/tmp}/power-iot-slice5.${RUN_ID}.XXXXXX")
KEEP_TMP=0
BACKEND_PID=""
CAPTURE_PID=""
CAPTURE_NAME=""
SIM_B_PID=""
MQTT_STARTED=0
DB_STARTED=0
CERTS_EXISTED=0
OTA_CERTS_EXISTED=0
ACL_EXISTED=0
PASSWORD_FILE_EXISTED=0
FIRMWARE_CA_EXISTED=0
TEST_DB_CREATED=0
TEST_DB_NAME=""

log() {
  printf '[slice5] %s\n' "$*"
}
fail() {
  KEEP_TMP=1
  printf '[slice5] FAILURE: %s\n' "$*" >&2
  exit 1
}

stop_pid() {
  pid=$1
  kill -TERM "$pid" 2>/dev/null || true
  for _ in $(seq 1 150); do
    kill -0 "$pid" 2>/dev/null || return 0
    sleep 0.1
  done
  kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}

cleanup() {
  status=$?
  set +e
  if [ -n "$CAPTURE_NAME" ]; then
    docker rm -f "$CAPTURE_NAME" >/dev/null 2>&1 || true
  fi
  if [ -n "$CAPTURE_PID" ]; then stop_pid "$CAPTURE_PID"; fi
  if [ -n "$SIM_B_PID" ]; then stop_pid "$SIM_B_PID"; fi
  if [ -n "$BACKEND_PID" ]; then stop_pid "$BACKEND_PID"; fi
  if [ "$TEST_DB_CREATED" -eq 1 ]; then
    "${COMPOSE[@]}" exec -T db psql -U "$POSTGRES_USER" -d postgres -q -v ON_ERROR_STOP=1 \
      -c "DROP DATABASE IF EXISTS \"$TEST_DB_NAME\";" >/dev/null 2>&1 || true
  fi
  if [ "$MQTT_STARTED" -eq 1 ]; then
    "${COMPOSE[@]}" stop mqtt >/dev/null 2>&1 || true
  fi
  if [ "$DB_STARTED" -eq 1 ]; then
    "${COMPOSE[@]}" stop db >/dev/null 2>&1 || true
  fi
  if [ "$CERTS_EXISTED" -eq 0 ]; then
    rm -rf "$ROOT/infrastructure/mosquitto/certs"
  fi
  if [ "$OTA_CERTS_EXISTED" -eq 0 ]; then
    rm -rf "$ROOT/infrastructure/firmware/certs"
  fi
  if [ "$FIRMWARE_CA_EXISTED" -eq 0 ]; then
    rm -f "$ROOT/firmware/esp8266-power-meter/device_v1/data/ca.pem"
  fi
  if [ "$ACL_EXISTED" -eq 0 ]; then
    rm -f "$ROOT/infrastructure/mosquitto/config/acl"
  fi
  if [ "$PASSWORD_FILE_EXISTED" -eq 0 ]; then
    rm -f "$ROOT/infrastructure/mosquitto/config/password_file"
  fi
  if [ "$KEEP_TMP" -eq 1 ] || [ "$status" -ne 0 ]; then
    printf '[slice5] Evidence retained at %s\n' "$TMP" >&2
  else
    rm -rf "$TMP"
  fi
  exit "$status"
}
trap cleanup EXIT INT TERM

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}
for command in docker curl jq python3 openssl timeout; do
  require_command "$command"
done

docker compose -f "$ROOT/infrastructure/docker-compose.yml" config >/dev/null

if [ -d "$ROOT/infrastructure/mosquitto/certs" ]; then CERTS_EXISTED=1; fi
if [ -d "$ROOT/infrastructure/firmware/certs" ]; then OTA_CERTS_EXISTED=1; fi
if [ -f "$ROOT/infrastructure/mosquitto/config/acl" ]; then ACL_EXISTED=1; fi
if [ -f "$ROOT/infrastructure/mosquitto/config/password_file" ]; then PASSWORD_FILE_EXISTED=1; fi
if [ -f "$ROOT/firmware/esp8266-power-meter/device_v1/data/ca.pem" ]; then FIRMWARE_CA_EXISTED=1; fi
if [ "$CERTS_EXISTED" -eq 1 ] || [ "$OTA_CERTS_EXISTED" -eq 1 ] || [ "$ACL_EXISTED" -eq 1 ] || [ "$PASSWORD_FILE_EXISTED" -eq 1 ] || [ "$FIRMWARE_CA_EXISTED" -eq 1 ]; then
  fail "generated local broker artifacts already exist; isolate or remove them before running Slice 5 harness"
fi

# Development credentials are generated for this run and never printed or
# committed. The ignored password_file is removed by cleanup when newly made.
MQTT_USERNAME=dev-user
MQTT_PASSWORD=$(openssl rand -hex 24)
export MQTT_USERNAME MQTT_PASSWORD
export MQTT_CONTAINER_UID=$(id -u)
export MQTT_CONTAINER_GID=$(id -g)
export POSTGRES_USER=${POSTGRES_USER:-admin}
export POSTGRES_PASSWORD=${POSTGRES_PASSWORD:-dev-only-change-me}
export POSTGRES_DB=${POSTGRES_DB:-power_iot}
export POSTGRES_PORT=${POSTGRES_PORT:-5432}
export MQTT_TLS_PORT=${MQTT_TLS_PORT:-8883}
TEST_DB_NAME="slice5_${RUN_ID//-/_}"
DATABASE_URL="postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@127.0.0.1:${POSTGRES_PORT}/${TEST_DB_NAME}?sslmode=disable"
export DATABASE_URL

sh "$ROOT/infrastructure/mosquitto/scripts/generate-dev-certs.sh" 127.0.0.1 >/dev/null
cp "$ROOT/infrastructure/mosquitto/config/acl.example" "$ROOT/infrastructure/mosquitto/config/acl"
docker run --rm --user "$(id -u):$(id -g)" \
  -v "$ROOT/infrastructure/mosquitto/config:/config" \
  --entrypoint mosquitto_passwd eclipse-mosquitto:2 -c -b /config/password_file "$MQTT_USERNAME" "$MQTT_PASSWORD" >/dev/null

log "starting local PostgreSQL/TimescaleDB and Mosquitto"
DB_WAS_RUNNING=$(docker inspect -f '{{.State.Running}}' power_iot_db 2>/dev/null || printf false)
MQTT_WAS_RUNNING=$(docker inspect -f '{{.State.Running}}' power_iot_mqtt 2>/dev/null || printf false)
# Record ownership before Compose starts so partial startup failures still clean
# up services that this harness may have created.
if [ "$DB_WAS_RUNNING" != true ]; then DB_STARTED=1; fi
if [ "$MQTT_WAS_RUNNING" != true ]; then MQTT_STARTED=1; fi
"${COMPOSE[@]}" up -d db mqtt >/dev/null

wait_healthy() {
  service=$1
  deadline=$((SECONDS + 90))
  while [ "$SECONDS" -lt "$deadline" ]; do
    state=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "power_iot_${service}" 2>/dev/null || true)
    if [ "$state" = healthy ]; then return 0; fi
    sleep 1
  done
  docker inspect "power_iot_${service}" >&2 || true
  fail "$service did not become healthy"
}
wait_healthy db
wait_healthy mqtt
"${COMPOSE[@]}" exec -T db psql -U "$POSTGRES_USER" -d postgres -q -v ON_ERROR_STOP=1 \
  -c "CREATE DATABASE \"$TEST_DB_NAME\";" >/dev/null
TEST_DB_CREATED=1

log "building real Backend and simulator binaries"
(cd "$ROOT/backend" && go build -o "$TMP/backend" ./cmd/server)
(cd "$ROOT/backend" && go build -o "$TMP/devseed" ./cmd/devseed)
(cd "$ROOT/backend" && go build -o "$TMP/migrate" ./cmd/migrate)
(cd "$ROOT/tools/device-simulator" && go build -o "$TMP/device-simulator" .)

log "applying latest embedded migrations"
"$TMP/migrate" -database-url "$DATABASE_URL" -direction up >"$TMP/migrate.log" 2>&1

# Unique fixture identities make this safe against the repository's persistent
# local development volume. Fixture creation happens before Backend startup, so
# it cannot race the Backend's application writer fence.
HEX=$(openssl rand -hex 12 | tr '[:lower:]' '[:upper:]')
MAC_A=${HEX:0:12}
MAC_B=${HEX:12:12}
SHOP_CODE="slice5-${RUN_ID}"
MP_A="slice5-${RUN_ID}-a"
MP_B="slice5-${RUN_ID}-b"
ASSIGNMENT_FROM=$(date -u -d '10 minutes ago' '+%Y-%m-%dT%H:%M:%SZ')
SHOP_ID=$("${COMPOSE[@]}" exec -T db psql -U "$POSTGRES_USER" -d "$TEST_DB_NAME" -qAt -v ON_ERROR_STOP=1 \
  -c "INSERT INTO shops (code, name) VALUES ('$SHOP_CODE', 'Slice 5 $RUN_ID') RETURNING id;" | tr -d '[:space:]')
[ -n "$SHOP_ID" ] || fail "shop fixture was not created"
"$TMP/devseed" --device-mac "$MAC_A" --device-name "slice5-$RUN_ID-a" --shop-id "$SHOP_ID" \
  --measurement-point-name "$MP_A" --assignment-from "$ASSIGNMENT_FROM" >"$TMP/seed-a.log" 2>&1
"$TMP/devseed" --device-mac "$MAC_B" --device-name "slice5-$RUN_ID-b" --shop-id "$SHOP_ID" \
  --measurement-point-name "$MP_B" --assignment-from "$ASSIGNMENT_FROM" >"$TMP/seed-b.log" 2>&1

export MQTT_BROKER_URL="tls://127.0.0.1:${MQTT_TLS_PORT}"
export MQTT_CA_FILE="$ROOT/infrastructure/mosquitto/certs/ca.crt"
export MQTT_CLIENT_ID="power-iot-slice5-backend-${RUN_ID}"
HTTP_PORT=$(python3 - <<'PY'
import socket
with socket.socket() as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
)
export HTTP_ADDR="127.0.0.1:${HTTP_PORT}"
log "starting the existing Backend process"
(env MQTT_BROKER_URL="$MQTT_BROKER_URL" MQTT_CA_FILE="$MQTT_CA_FILE" \
  MQTT_CLIENT_ID="$MQTT_CLIENT_ID" MQTT_USERNAME="$MQTT_USERNAME" MQTT_PASSWORD="$MQTT_PASSWORD" \
  DATABASE_URL="$DATABASE_URL" HTTP_ADDR="$HTTP_ADDR" "$TMP/backend" >"$TMP/backend.log" 2>&1) &
BACKEND_PID=$!

wait_backend_ready() {
  deadline=$((SECONDS + 90))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if ! kill -0 "$BACKEND_PID" 2>/dev/null; then
      cat "$TMP/backend.log" >&2 || true
      fail "Backend exited before readiness"
    fi
    body=$(curl -fsS "http://${HTTP_ADDR}/" 2>/dev/null || true)
    if printf '%s' "$body" | jq -e '.mqtt_ready == true and .db == "connected"' >/dev/null 2>&1; then
      printf '%s\n' "$body" >"$TMP/backend-ready.json"
      return 0
    fi
    sleep 1
  done
  cat "$TMP/backend.log" >&2 || true
  fail "Backend did not reach MQTT READY"
}
wait_backend_ready

# Capture the real wire bytes independently over MQTT. This is not a fake
# broker or an in-process callback: the subscriber is a second authenticated
# TLS MQTT client attached to the running Mosquitto container.
CAPTURE_NAME="slice5-capture-${RUN_ID}"
PROBE_TOPIC="device/slice5-${RUN_ID}/status"
docker run --rm --name "$CAPTURE_NAME" \
  --network container:power_iot_mqtt \
  -v "$TMP:/out" \
  -v "$ROOT/infrastructure/mosquitto/certs:/cert:ro" \
  eclipse-mosquitto:2 sh -c \
  "mosquitto_sub -v -h 127.0.0.1 -p 8883 --cafile /cert/ca.crt -u '$MQTT_USERNAME' -P '$MQTT_PASSWORD' -t device/upload/data -t '$PROBE_TOPIC' 2>&1 | tee /out/telemetry.log" &
CAPTURE_PID=$!
deadline=$((SECONDS + 30))
while [ "$SECONDS" -lt "$deadline" ]; do
  docker run --rm --network container:power_iot_mqtt \
    -v "$ROOT/infrastructure/mosquitto/certs:/cert:ro" \
    --entrypoint mosquitto_pub eclipse-mosquitto:2 \
    -h 127.0.0.1 -p 8883 --cafile /cert/ca.crt -u "$MQTT_USERNAME" -P "$MQTT_PASSWORD" \
    -t "$PROBE_TOPIC" -m slice5-ready >/dev/null 2>&1 || true
  if grep -q "$PROBE_TOPIC slice5-ready" "$TMP/telemetry.log" 2>/dev/null; then break; fi
  if ! kill -0 "$CAPTURE_PID" 2>/dev/null; then
    cat "$TMP/telemetry.log" >&2 || true
    fail "wire capture subscriber exited"
  fi
  sleep 1
done
grep -q "$PROBE_TOPIC slice5-ready" "$TMP/telemetry.log" || fail "wire capture subscriber did not receive readiness probe"

run_simulator_once() {
  name=$1
  shift
  if ! timeout 90s env \
    MQTT_BROKER_URL="$MQTT_BROKER_URL" MQTT_CA_FILE="$MQTT_CA_FILE" \
    MQTT_USERNAME="$MQTT_USERNAME" MQTT_PASSWORD="$MQTT_PASSWORD" \
    "$TMP/device-simulator" "$@" >"$TMP/$name.log" 2>&1; then
    cat "$TMP/$name.log" >&2 || true
    fail "$name simulator run failed"
  fi
}

log "collision case: two devices, same boot_counter=1 and seq=1"
run_simulator_once collision --mode once --boot-counter 1 --start-seq 1 --ack-timeout 15s \
  --device-mac "$MAC_A" --device-mac "$MAC_B"
grep -q 'ACK stored' "$TMP/collision.log" || fail "collision case did not receive stored ACK"
python3 - "$TMP/telemetry.log" "$MAC_A" "$MAC_B" <<'PY'
import json
import sys
from collections import Counter
path, mac_a, mac_b = sys.argv[1:]
rows = []
with open(path, encoding="utf-8") as stream:
    for line in stream:
        line = line.strip()
        if ' ' not in line:
            continue
        topic, payload = line.split(' ', 1)
        if topic != 'device/upload/data':
            continue
        try:
            value = json.loads(payload)
        except json.JSONDecodeError:
            continue
        if value.get("boot_counter") == 1 and value.get("seq") == 1:
            rows.append(value)
counts = Counter(value.get("mac") for value in rows)
assert counts[mac_a] == 1, counts
assert counts[mac_b] == 1, counts
assert len(rows) == 2, rows
print(f"collision wire proof: {mac_a}=1 {mac_b}=1 boot=1 seq=1")
PY

log "duplicate case: stored then duplicate for one exact identity"
run_simulator_once duplicate --mode duplicate --boot-counter 2 --start-seq 1 --ack-timeout 15s \
  --device-mac "$MAC_A"
grep -q 'ACK stored' "$TMP/duplicate.log" || fail "duplicate case missing stored ACK"
grep -q 'ACK duplicate' "$TMP/duplicate.log" || fail "duplicate case missing duplicate ACK"

query_rows() {
  "${COMPOSE[@]}" exec -T db psql -U "$POSTGRES_USER" -d "$TEST_DB_NAME" -At -F $'\t' -v ON_ERROR_STOP=1 -c "$1"
}
collision_rows=$(query_rows "SELECT d.mac_address, mp.name, mp.shop_id, r.boot_counter, r.sequence FROM power_readings r JOIN devices d ON d.id = r.device_id JOIN measurement_points mp ON mp.id = r.measurement_point_id WHERE d.mac_address IN ('$MAC_A','$MAC_B') AND r.boot_counter = 1 AND r.sequence = 1 ORDER BY d.mac_address;")
printf '%s\n' "$collision_rows" >"$TMP/collision-rows.tsv"
python3 - "$TMP/collision-rows.tsv" "$MAC_A" "$MAC_B" "$MP_A" "$MP_B" "$SHOP_ID" <<'PY'
import sys
rows = [line.strip().split('\t') for line in open(sys.argv[1], encoding='utf-8') if line.strip()]
mac_a, mac_b, mp_a, mp_b, shop_id = sys.argv[2:]
assert len(rows) == 2, rows
assert [row[0] for row in rows] == sorted([mac_a, mac_b]), rows
assert all(row[3:] == ['1', '1'] for row in rows), rows
assert all(row[2] == shop_id for row in rows), rows
assert {row[0]: row[1] for row in rows} == {mac_a: mp_a, mac_b: mp_b}, rows
print(f"collision DB proof: each MAC mapped to its intended measurement point, boot=1 seq=1")
PY

duplicate_counts=$(query_rows "SELECT (SELECT count(*) FROM power_readings r JOIN devices d ON d.id = r.device_id WHERE d.mac_address = '$MAC_A' AND r.boot_counter = 2 AND r.sequence = 1), (SELECT count(*) FROM telemetry_ingest_keys k JOIN devices d ON d.id = k.device_id WHERE d.mac_address = '$MAC_A' AND k.boot_counter = 2 AND k.sequence = 1);")
[ "$duplicate_counts" = $'1\t1' ] || fail "duplicate DB proof expected one reading and one ingest key, got: $duplicate_counts"
printf 'duplicate DB proof: readings=1 ingest_keys=1\n'

log "reconnect isolation case: A reconnects while B remains healthy in the same process"
(env MQTT_BROKER_URL="$MQTT_BROKER_URL" MQTT_CA_FILE="$MQTT_CA_FILE" \
  MQTT_USERNAME="$MQTT_USERNAME" MQTT_PASSWORD="$MQTT_PASSWORD" \
  "$TMP/device-simulator" --mode reconnect --reconnect-device-mac "$MAC_A" \
  --publish-interval 5s --boot-counter 4 --start-seq 1 --ack-timeout 15s \
  --device-mac "$MAC_A" --device-mac "$MAC_B" >"$TMP/reconnect.log" 2>&1) &
SIM_B_PID=$!
deadline=$((SECONDS + 45))
B_PUBLISHED_PATTERN="PUBLISHED boot=4.*mac=$MAC_B"
while [ "$SECONDS" -lt "$deadline" ]; do
  if ! kill -0 "$SIM_B_PID" 2>/dev/null; then
    cat "$TMP/reconnect.log" >&2 || true
    fail "multi-device simulator exited before reconnect proof"
  fi
  if grep -Eq "ACK (stored|duplicate) mac=$MAC_A boot=4 seq=1" "$TMP/reconnect.log" 2>/dev/null && \
     grep -q "ACK stored mac=$MAC_B boot=4 seq=2" "$TMP/reconnect.log" 2>/dev/null && \
     [ "$(grep -c "$B_PUBLISHED_PATTERN" "$TMP/reconnect.log" 2>/dev/null || true)" -ge 2 ]; then
    break
  fi
  sleep 1
done
grep -q 'RECONNECT first telemetry retained' "$TMP/reconnect.log" || fail "A did not retain pending telemetry across disconnect"
grep -q 'RECONNECT connected and subscriptions restored' "$TMP/reconnect.log" || fail "A did not restore READY after reconnect"
grep -Eq "ACK (stored|duplicate) mac=$MAC_A boot=4 seq=1" "$TMP/reconnect.log" || fail "A did not receive a terminal ACK for replay"
grep -q "ACK stored mac=$MAC_A boot=4 seq=2" "$TMP/reconnect.log" || fail "A did not complete a fresh post-replay telemetry item"
grep -q "QUEUE pending=0 mac=$MAC_A" "$TMP/reconnect.log" || fail "A did not finish with an empty queue"
[ "$(grep -c "$B_PUBLISHED_PATTERN" "$TMP/reconnect.log" 2>/dev/null || true)" -ge 2 ] || fail "B did not continue publishing in the same process"
replay_ack=$(grep -E "ACK (stored|duplicate) mac=$MAC_A boot=4 seq=1" "$TMP/reconnect.log" | tail -1)
printf 'replay ACK proof: %s\n' "$replay_ack"

stop_pid "$SIM_B_PID"
wait "$SIM_B_PID" 2>/dev/null || true
SIM_B_PID=""
grep -q "QUEUE pending=0 mac=$MAC_B" "$TMP/reconnect.log" || fail "B did not finish with an empty queue after healthy ACKs"

python3 - "$TMP/telemetry.log" "$MAC_A" <<'PY'
import json
import sys
path, mac = sys.argv[1:]
rows = []
with open(path, encoding="utf-8") as stream:
    for line in stream:
        line = line.strip()
        if ' ' not in line:
            continue
        topic, payload = line.split(' ', 1)
        if topic != 'device/upload/data':
            continue
        try:
            value = json.loads(payload)
        except json.JSONDecodeError:
            continue
        if value.get("mac") == mac and value.get("boot_counter") == 4 and value.get("seq") == 1:
            rows.append(line)
assert len(rows) == 2, rows
assert rows[0] == rows[1], (rows[0], rows[1])
print(f"replay wire proof: exact A payload repeated twice for boot=4 seq=1")
PY
python3 - "$TMP/telemetry.log" "$MAC_B" <<'PY'
import json
import sys
path, mac = sys.argv[1:]
by_seq = {}
with open(path, encoding="utf-8") as stream:
    for line in stream:
        line = line.strip()
        if ' ' not in line:
            continue
        topic, payload = line.split(' ', 1)
        if topic != 'device/upload/data':
            continue
        value = json.loads(payload)
        if value.get("mac") == mac and value.get("boot_counter") == 4:
            by_seq[value["seq"]] = value
assert {1, 2}.issubset(by_seq), by_seq
first, second = by_seq[1], by_seq[2]
assert second["kwh"] > first["kwh"] > 0, (first, second)
assert second["energy_delta_kwh"] > 0, second
for value in (first, second):
    assert abs(value["p"] - value["v"] * value["c"] * value["pf"]) < 1e-6, value
print(f"generator isolation proof: B kWh increased from {first['kwh']} to {second['kwh']}")
PY
reconnect_rows=$(query_rows "SELECT count(*), (SELECT count(*) FROM telemetry_ingest_keys k JOIN devices d ON d.id = k.device_id WHERE d.mac_address = '$MAC_A' AND k.boot_counter = 4 AND k.sequence = 1) FROM power_readings r JOIN devices d ON d.id = r.device_id WHERE d.mac_address = '$MAC_A' AND r.boot_counter = 4 AND r.sequence = 1;")
[ "$reconnect_rows" = $'1\t1' ] || fail "reconnect DB proof expected one stored row and one ingest key, got: $reconnect_rows"
printf 'reconnect DB proof: A readings=1 ingest_keys=1\n'
b_rows=$(query_rows "SELECT count(*) FROM power_readings r JOIN devices d ON d.id = r.device_id WHERE d.mac_address = '$MAC_B' AND r.boot_counter = 4;")
[ "$b_rows" -ge 2 ] || fail "B persistence proof expected at least two readings, got: $b_rows"
printf 'reconnect DB proof: B readings=%s\n' "$b_rows"

log "PASS: real MQTT multi-device collision, duplicate, reconnect, replay, ACK, and persistence checks"
printf 'MAC_A=%s\nMAC_B=%s\nSHOP_ID=%s\n' "$MAC_A" "$MAC_B" "$SHOP_ID"
printf 'evidence=%s\n' "$TMP"
KEEP_TMP=1
exit 0
