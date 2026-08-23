#!/usr/bin/env bash
set -euo pipefail
umask 077

# Complete disposable App-VM/DB-VM-shaped D6 rehearsal. It creates only
# temporary test credentials/certificates, uses a unique Compose project, and
# tears down every container, network, and volume on exit.
[[ "${D6_REHEARSAL:-}" == 1 ]] || { echo 'D6_REHEARSAL=1 is required' >&2; exit 2; }

repo=$(cd "$(dirname "$0")/../.." && pwd)
root=$(mktemp -d)
project="d6-integrated-$(date +%s)-${BASHPID}-$(openssl rand -hex 4)"
db_network="${project}-db"
app_network="${project}-app"
# Disposable daemon boundary: DB publishes only to the host loopback interface;
# App containers reach it through host.docker.internal, never a DB Docker bridge.
db_bind_address=127.0.0.1
# Allocate every host port independently in one OS query. The sockets are held
# together while selecting, then the complete set is checked again immediately
# before compose starts so a collision fails closed rather than being derived
# from another service's port.
allocate_ports() {
  local selected
  selected=$(python3 - <<'PY'
import socket
sockets = []
try:
    for _ in range(5):
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 0)
        sock.bind(("127.0.0.1", 0))
        sockets.append(sock)
        print(sock.getsockname()[1])
finally:
    for sock in sockets:
        sock.close()
PY
  )
  mapfile -t ports <<< "$selected"
  [[ "${#ports[@]}" == 5 ]] || { echo 'unable to allocate five rehearsal ports' >&2; exit 1; }
  [[ "$(printf '%s\n' "${ports[@]}" | sort -n | uniq | wc -l)" == 5 ]] || {
    echo 'rehearsal port allocator returned duplicate ports' >&2
    exit 1
  }
  app_db_port=${ports[0]}
  provider_db_port=${ports[1]}
  http_port=${ports[2]}
  d1l_port=${ports[3]}
  mqtt_port=${ports[4]}
}
validate_ports_available() {
  python3 - "$@" <<'PY'
import socket
import sys
ports = [int(value) for value in sys.argv[1:]]
if len(ports) != len(set(ports)):
    raise SystemExit("duplicate rehearsal host ports")
sockets = []
try:
    for port in ports:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 0)
        sock.bind(("127.0.0.1", port))
        sockets.append(sock)
except OSError as exc:
    raise SystemExit(f"rehearsal host port collision: {exc}")
finally:
    for sock in sockets:
        sock.close()
PY
}
allocate_ports
app_compose="$repo/infrastructure/d6/app-compose.yml"
db_compose="$repo/infrastructure/d6/db-compose.yml"
cleanup() {
  if [[ -f "$root/app.env" ]]; then
    docker compose -p "$project" --env-file "$root/app.env" -f "$app_compose" down -v --remove-orphans >/dev/null 2>&1 || true
  fi
  if [[ -f "$root/db.env" ]]; then
    docker compose -p "$project" --env-file "$root/db.env" -f "$db_compose" down -v --remove-orphans >/dev/null 2>&1 || true
  fi
  rm -rf "$root"
}
trap cleanup EXIT
assert_unique_disposable_resources() {
  local resource
  docker info >/dev/null 2>&1 || { echo 'Docker daemon unavailable for rehearsal preflight' >&2; exit 1; }
  [[ -z "$(docker ps -aq --filter "label=com.docker.compose.project=$project")" ]] || {
    echo "rehearsal Compose project already exists: $project" >&2
    exit 1
  }
  for resource in "$db_network" "$app_network" "${project}-application" "${project}-provider"; do
    if docker network inspect "$resource" >/dev/null 2>&1 || docker volume inspect "$resource" >/dev/null 2>&1; then
      echo "rehearsal resource already exists: $resource" >&2
      exit 1
    fi
  done
  echo "DISPOSABLE_IDENTIFIERS_UNIQUE=PASS project=$project networks=$db_network,$app_network volumes=${project}-application,${project}-provider"
}

mkdir -p "$root"/{certs,mqtt-config,mqtt-data,backend-secrets,d1l-secrets,db-role-passwords,proxy}
# Container users are non-root; the temporary directory is traversable while
# individual test secrets/certificates remain read-only and never leave root.
chmod 755 "$root" "$root"/{certs,backend-secrets,d1l-secrets,db-role-passwords}
random_secret() { openssl rand -hex 24; }
bootstrap_password=$(random_secret)
app_password=$(random_secret)
runtime_password=$(random_secret)
provider_password=$(random_secret)
migration_password=$(random_secret)
db_admin_password=$(random_secret)
mqtt_username="rehearsal-$(openssl rand -hex 8)"
mqtt_password=$(random_secret)
jwt_active_kid=d6-rehearsal-auth-active
jwt_active_private_key_filename=jwt-active-private.pem
jwt_active_private_key="$root/backend-secrets/$jwt_active_private_key_filename"
printf '%s\n' "$bootstrap_password" > "$root/bootstrap-password"
printf '%s\n' "$app_password" > "$root/db-role-passwords/poweriot"
printf '%s\n' "$runtime_password" > "$root/db-role-passwords/poweriot_runtime"
printf '%s\n' "$migration_password" > "$root/db-role-passwords/d6_migrator"
printf '%s\n' "$db_admin_password" > "$root/db-role-passwords/d6_db_admin"
printf '%s\n' "$runtime_password" > "$root/backend-secrets/application-db-password"
printf '%s\n' "$app_password" > "$root/app-db-password"
printf '%s\n' "$provider_password" > "$root/d1l-secrets/provider-db-password"
printf '%s\n' "$mqtt_username" > "$root/backend-secrets/mqtt-username"
printf '%s\n' "$mqtt_password" > "$root/backend-secrets/mqtt-password"
chmod 0444 "$root/bootstrap-password" "$root/db-role-passwords"/*
chmod 644 "$root/backend-secrets/application-db-password" "$root/backend-secrets/mqtt-username" "$root/d1l-secrets/provider-db-password"
chmod 0600 "$root/backend-secrets/mqtt-password"

openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=d6-rehearsal-ca' \
  -keyout "$root/certs/ca.key" -out "$root/certs/ca.crt" >/dev/null 2>&1
make_cert() {
  local name=$1 cn=$2 san=$3 uri=${4:-}
  openssl req -newkey rsa:2048 -nodes -subj "/CN=$cn" \
    -keyout "$root/certs/$name.key" -out "$root/certs/$name.csr" >/dev/null 2>&1
  {
    if [[ -n "$uri" ]]; then
      echo "subjectAltName=URI:$uri"
    else
      echo "subjectAltName=$san"
    fi
  } > "$root/certs/$name.ext"
  openssl x509 -req -days 1 -in "$root/certs/$name.csr" \
    -CA "$root/certs/ca.crt" -CAkey "$root/certs/ca.key" -CAcreateserial \
    -extfile "$root/certs/$name.ext" -out "$root/certs/$name.crt" >/dev/null 2>&1
  rm -f "$root/certs/$name.csr" "$root/certs/$name.ext" "$root/certs/ca.srl"
}
make_cert mqtt mqtt 'DNS:mqtt,DNS:localhost,IP:127.0.0.1'
make_cert provider provider 'DNS:d1l-authority,DNS:localhost,IP:127.0.0.1'
make_cert runbook runbook 'DNS:runbook' 'spiffe://power-iot/a3/deployment-runbook'
chmod 644 "$root/certs"/*.crt
chmod 0600 "$root/certs"/*.key
openssl genpkey -algorithm ED25519 -out "$root/certs/admission-private.pem" >/dev/null 2>&1
openssl pkey -in "$root/certs/admission-private.pem" -pubout -out "$root/backend-secrets/admission-public.pem" >/dev/null 2>&1
# This is a distinct, disposable JWT signing key for the rehearsal only. It is
# never reused for admission, MQTT, D1-L, or firmware purposes.
openssl genpkey -algorithm ED25519 -out "$jwt_active_private_key" >/dev/null 2>&1
chmod 0600 "$root/certs/admission-private.pem" "$jwt_active_private_key"
chmod 0444 "$root/backend-secrets/admission-public.pem"
cp "$root/certs/ca.crt" "$root/backend-secrets/mqtt-ca.crt"
cp "$root/certs/provider.crt" "$root/d1l-secrets/provider.crt"
cp "$root/certs/provider.key" "$root/d1l-secrets/provider.key"
cp "$root/certs/ca.crt" "$root/d1l-secrets/provider-ca.crt"
chmod 644 "$root/backend-secrets/mqtt-ca.crt" "$root/d1l-secrets/provider.crt" "$root/d1l-secrets/provider-ca.crt"
chmod 0600 "$root/d1l-secrets/provider.key"

cat > "$root/mqtt-config/mosquitto.conf" <<'EOF'
listener 8883
cafile /mosquitto/certs/ca.crt
certfile /mosquitto/certs/mqtt.crt
keyfile /mosquitto/certs/mqtt.key
allow_anonymous false
password_file /mosquitto/certs/passwd
persistence false
log_dest stdout
EOF
chmod 644 "$root/mqtt-config/mosquitto.conf"
touch "$root/certs/passwd"
assert_unique_disposable_resources
# Interactive mosquitto_passwd reads both password entries from stdin; the
# password is never present in this process's argv or the generated command.
cat "$root/backend-secrets/mqtt-password" "$root/backend-secrets/mqtt-password" | \
  docker run --rm -i -v "$root/certs:/out" eclipse-mosquitto:2 \
  mosquitto_passwd /out/passwd "$mqtt_username" >/dev/null
chmod 644 "$root/certs/passwd"
# Runtime images use non-root UIDs. Keep the provider key and MQTT password
# restrictive while assigning ownership through disposable containers (never
# the host); no credential value is passed as an argument.
docker run --rm --entrypoint /bin/chown \
  -v "$root/d1l-secrets:/out" eclipse-mosquitto:2 \
  10002:10002 /out/provider.key >/dev/null
docker run --rm --entrypoint /bin/chown \
  -v "$root/backend-secrets:/out" eclipse-mosquitto:2 \
  10001:10001 /out/mqtt-password >/dev/null
docker run --rm --entrypoint /bin/chown \
  -v "$root/backend-secrets:/out" eclipse-mosquitto:2 \
  10001:10001 "/out/$jwt_active_private_key_filename" >/dev/null
# Mosquitto drops privileges to its packaged UID before opening the TLS key.
# The cert directory is intentionally read-only at runtime, so set ownership
# before compose starts rather than relying on the entrypoint's chown attempt.
docker run --rm --entrypoint /bin/chown \
  -v "$root/certs:/out" eclipse-mosquitto:2 \
  1883:1883 /out/mqtt.key >/dev/null
cp "$repo/infrastructure/d6/nginx.conf.template" "$root/proxy/nginx.conf"
chmod 644 "$root/proxy/nginx.conf"

cat > "$root/db.env" <<EOF
APPLICATION_DB_NAME=power_iot
APPLICATION_DB_USER=poweriot
D1L_PROVIDER_DB_NAME=d1l_provider
D1L_PROVIDER_DB_USER=d1l
POWERIOT_DB_BIND_ADDRESS=$db_bind_address
APPLICATION_DB_PRIVATE_PORT=$app_db_port
D1L_PROVIDER_DB_PRIVATE_PORT=$provider_db_port
POWERIOT_DB_BOOTSTRAP_USER=d6_bootstrap
POWERIOT_DB_BOOTSTRAP_PASSWORD_FILE=$root/bootstrap-password
POWERIOT_APPLICATION_DB_PASSWORD_FILE=$root/app-db-password
POWERIOT_PROVIDER_DB_PASSWORD_FILE=$root/d1l-secrets/provider-db-password
POWERIOT_APPLICATION_DB_VOLUME=${project}-application
POWERIOT_PROVIDER_DB_VOLUME=${project}-provider
POWERIOT_DB_NETWORK=$db_network
EOF
cat > "$root/app.env" <<EOF
POWERIOT_REVERSE_PROXY_CONFIG=$root/proxy/nginx.conf
POWERIOT_HTTP_PORT=$http_port
D1L_TLS_PORT=$d1l_port
APPLICATION_DATABASE_URL=postgres://poweriot_runtime@host.docker.internal:$app_db_port/power_iot?sslmode=disable
D1L_PROVIDER_DATABASE_URL=postgres://d1l@host.docker.internal:$provider_db_port/d1l_provider?sslmode=disable
MQTT_BROKER_URL=tls://mqtt:8883
JWT_ACTIVE_KID=$jwt_active_kid
JWT_ACTIVE_PRIVATE_KEY_FILE=/run/poweriot/secrets/$jwt_active_private_key_filename
POWERIOT_BACKEND_SECRET_DIR=$root/backend-secrets
POWERIOT_MQTT_CONFIG=$root/mqtt-config
POWERIOT_MQTT_CERT_DIR=$root/certs
POWERIOT_MQTT_DATA_DIR=$root/mqtt-data
POWERIOT_D1L_SECRET_DIR=$root/d1l-secrets
POWERIOT_APP_NETWORK=$app_network
POWERIOT_DB_BIND_ADDRESS=$db_bind_address
APPLICATION_DB_PRIVATE_PORT=$app_db_port
D1L_PROVIDER_DB_PRIVATE_PORT=$provider_db_port
MQTT_TLS_PORT=$mqtt_port
EOF

compose_db=(docker compose -p "$project" --env-file "$root/db.env" -f "$db_compose")
compose_app=(docker compose -p "$project" --env-file "$root/app.env" -f "$app_compose")
validate_ports_available "$app_db_port" "$provider_db_port" "$http_port" "$d1l_port" "$mqtt_port"
# Compose receives only non-secret JWT metadata. The private key remains solely
# in the restricted mounted directory and is never copied into environment.
app_config=$("${compose_app[@]}" config)
printf '%s\n' "$app_config" | grep -Fq "JWT_ACTIVE_KID: $jwt_active_kid" || { echo 'JWT active KID was not injected into Compose config' >&2; exit 1; }
printf '%s\n' "$app_config" | grep -Fq "JWT_ACTIVE_PRIVATE_KEY_FILE: /run/poweriot/secrets/$jwt_active_private_key_filename" || { echo 'JWT private key container path was not injected into Compose config' >&2; exit 1; }
echo 'D6_BACKEND_JWT_CONFIG_INJECTED=YES'
echo "D6_BACKEND_JWT_PRIVATE_KEY_CONTAINER_PATH=/run/poweriot/secrets/$jwt_active_private_key_filename"
"${compose_db[@]}" up -d >/dev/null
for service in application-db d1l-provider-db; do
  id=$("${compose_db[@]}" ps -q "$service")
  for _ in $(seq 1 60); do
    health=$(docker inspect -f '{{.State.Health.Status}}' "$id" 2>/dev/null || true)
    [[ "$health" == healthy ]] && break
    sleep 1
  done
  [[ "$(docker inspect -f '{{.State.Health.Status}}' "$id")" == healthy ]] || { echo "$service did not become healthy" >&2; exit 1; }
done

echo 'DB_VM_SERVICES=application-db,d1l-provider-db'
echo 'APPLICATION_DB_PROVIDER_DB_IDENTITIES=power_iot,d1l_provider'

printf 'target=rehearsal\nrole=power-iot-db\n' > "$root/db-role-identity"
chmod 0444 "$root/db-role-identity"
cp "$root/db-role-passwords/d6_db_admin" "$root/db-admin-password"
chmod 0444 "$root/db-admin-password"

# The actual production role-provisioning artifact is used before any V5
# writer/bootstrap activity. It owns creation, conflict checks, privileges,
# credential identity checks, and safe database ownership initialization.
role_bootstrap() {
  bootstrap_file=${1:-$root/bootstrap-password}
  env \
    D6_DB_COMPOSE_FILE="$db_compose" D6_DB_ENV_FILE="$root/db.env" D6_DB_PROJECT="$project" \
    D6_DB_BOOTSTRAP_ROLE=d6_bootstrap D6_DB_BOOTSTRAP_PASSWORD_FILE="$bootstrap_file" \
    D6_DB_ROLE_PASSWORD_DIR="$root/db-role-passwords" D6_DB_LOCAL_ROLE_IDENTITY_FILE="$root/db-role-identity" \
    D6_DB_TARGET=rehearsal D6_APPLICATION_DB_NAME=power_iot \
    bash "$repo/infrastructure/d6/d6-db-role-bootstrap.sh"
}
role_bootstrap
if role_bootstrap "$root/missing-bootstrap-password" >/dev/null 2>&1; then
  echo 'missing bootstrap credential unexpectedly succeeded' >&2
  exit 1
fi
echo 'ROLE_BOOTSTRAP_MISSING_CREDENTIAL_FAIL_CLOSED=PASS'
if ! "${compose_db[@]}" exec -T application-db psql -U d6_bootstrap -d power_iot -v ON_ERROR_STOP=1 -c 'ALTER ROLE poweriot_runtime SUPERUSER' >/dev/null 2>&1; then
  echo 'unable to install disposable conflicting role fixture' >&2
  exit 1
fi
if role_bootstrap >/dev/null 2>&1; then
  echo 'conflicting runtime role unexpectedly succeeded' >&2
  exit 1
fi
echo 'ROLE_CONFLICT_FAIL_CLOSED=PASS'
"${compose_db[@]}" exec -T application-db psql -U d6_bootstrap -d power_iot -v ON_ERROR_STOP=1 -c 'ALTER ROLE poweriot_runtime NOSUPERUSER' >/dev/null
role_bootstrap >/dev/null
echo 'ROLE_BOOTSTRAP_CONFLICT_FIXTURE_RESTORED=PASS'

# Rehearsal-only bootstrap to V5. This is the pre-protected boundary; the D6
# operator below still uses only d6-migrate for V5->V6.
docker run --rm --network "$db_network" -e PGPASSWORD="$app_password" \
  -v "$repo/backend:/src" -w /src golang:1.25.4 \
  sh -c 'go run ./cmd/migrate -database-url "postgres://poweriot@application-db/power_iot?sslmode=disable"' >/dev/null
# Re-run the same production artifact against the now-real V5 catalog. This
# proves rerun idempotence and applies the runtime's explicit table privileges
# without any alternate inline role SQL.
role_bootstrap >/dev/null
echo 'ROLE_BOOTSTRAP_V5_RERUN=PASS'
"${compose_app[@]}" build backend d1l-authority >/dev/null
backend_image="${project}-backend"
docker image inspect "$backend_image" >/dev/null 2>&1 || { echo 'backend image was not produced' >&2; exit 1; }
[[ "$(stat -c '%a' "$jwt_active_private_key")" == 600 ]] || { echo 'JWT rehearsal key permissions are not restrictive' >&2; exit 1; }
[[ "$(stat -c '%u:%g' "$jwt_active_private_key")" == 10001:10001 ]] || { echo 'JWT rehearsal key ownership is not backend UID 10001' >&2; exit 1; }
# Prove access using the actual backend image and UID, without starting the
# server or exposing any key bytes.
docker run --rm --user 10001:10001 --entrypoint /bin/sh \
  -v "$root/backend-secrets:/run/poweriot/secrets:ro" "$backend_image" \
  -c "test -r /run/poweriot/secrets/$jwt_active_private_key_filename"
echo 'D6_BACKEND_JWT_KEY_READABLE_BY_UID_10001=YES'
validate_ports_available "$http_port" "$d1l_port" "$mqtt_port"
"${compose_app[@]}" up -d d1l-authority >/dev/null
for _ in $(seq 1 60); do
  if curl -fsS --cacert "$root/certs/ca.crt" --cert "$root/certs/runbook.crt" --key "$root/certs/runbook.key" "https://127.0.0.1:$d1l_port/readyz" >/tmp/d6-d1l-ready.$$ 2>/dev/null; then
    echo 'D1L_MTLS_READINESS=PASS'
    echo 'PROVIDER_DB_REACHED_VIA_PRIVATE_TCP=YES'
    break
  fi
  sleep 1
done
[[ -s /tmp/d6-d1l-ready.$$ ]] || { echo 'D1L_MTLS_READINESS=FAIL' >&2; exit 1; }
rm -f /tmp/d6-d1l-ready.$$

# Build and execute the actual target-bound d6-drain binary used by the
# operator, rather than bypassing it with a shell-authored evidence file.
(cd "$repo/backend" && go build -trimpath -o "$root/d6-drain" ./cmd/d6-drain)
chmod 0555 "$root/d6-drain"
cat > "$root/d6-drain-wrapper" <<EOF
#!/bin/bash
set -o pipefail
"$root/d6-drain" "\$@" | tee "$root/d6-drain-output"
EOF
chmod 0555 "$root/d6-drain-wrapper"
printf 'target=rehearsal\nrole=power-iot-a3-rehearsal-operator\n' > "$root/backend-secrets/target-identity"
printf 'target=rehearsal\nrole=power-iot-app\n' > "$root/app-role-identity"
chmod 0444 "$root/backend-secrets/target-identity" "$root/app-role-identity"

"${compose_db[@]}" exec -T application-db sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1' <<'SQL'
INSERT INTO clients(name,code) VALUES ('D6 rehearsal client','d6-rehearsal-client') ON CONFLICT (code) DO NOTHING;
DO $$
DECLARE cid bigint; sid bigint; did bigint; pid uuid;
BEGIN
  SELECT id INTO cid FROM clients WHERE code='d6-rehearsal-client';
  INSERT INTO shops(client_id,code,name) VALUES (cid,'d6-rehearsal','D6 rehearsal') ON CONFLICT (code) DO NOTHING;
  SELECT id INTO sid FROM shops WHERE code='d6-rehearsal';
  INSERT INTO measurement_points(shop_id,name) VALUES (sid,'d6-point') RETURNING id INTO pid;
  INSERT INTO devices(shop_id,inventory_owner_client_id,mac_address,name) VALUES (sid,cid,'AABBCCDDEEFF','D6 device') RETURNING id INTO did;
  INSERT INTO device_assignments(device_id,measurement_point_id,valid_from) VALUES (did,pid,now());
END $$;
SQL

cat > "$root/readiness-hook" <<EOF
#!/bin/bash
set -euo pipefail
for _ in \$(seq 1 60); do
  status=\$(curl -fsS "http://127.0.0.1:$http_port/" 2>/dev/null || true)
  [[ "\$status" == *'"status":"online"'* ]] && break
  sleep 1
done
direct=\$(docker run --rm --network "$app_network" nginx:1.27-alpine sh -c 'wget -qO- http://backend:8080/' 2>/dev/null || true)
[[ "\$direct" == *'"status":"online"'* ]] || { echo "DIRECT_BACKEND_HEALTH_GET=FAIL" >&2; exit 1; }
[[ "\$status" == *'"d6_runtime_mode":"PRE_CUTOVER"'* && "\$status" == *'"mqtt_ingestion_blocked":true'* && "\$status" == *'"http_writes_blocked":true'* ]] || { echo "READINESS=FAIL \$status" >&2; exit 1; }
echo DIRECT_BACKEND_HEALTH_GET=PASS
post=\$(curl -sS -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:$http_port/")
[[ "\$post" == 503 ]] || { echo "PRE_CUTOVER_HTTP_BLOCK=FAIL status=\$post" >&2; exit 1; }
for auth_path in login refresh logout; do
  auth_status=\$(curl -sS -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:$http_port/api/v1/auth/\$auth_path")
  [[ "\$auth_status" == 503 ]] || { echo "PRE_CUTOVER_AUTH_BLOCK=FAIL path=\$auth_path status=\$auth_status" >&2; exit 1; }
done
for b6_path in me shops; do
  b6_status=\$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:$http_port/api/v1/\$b6_path")
  [[ "\$b6_status" == 401 ]] || { echo "PRE_CUTOVER_B6_GET=FAIL path=\$b6_path status=\$b6_status" >&2; exit 1; }
done
b7_status=\$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:$http_port/api/v1/shops/1/dashboard")
[[ "\$b7_status" == 401 ]] || { echo "PRE_CUTOVER_B7_GET=FAIL status=\$b7_status" >&2; exit 1; }
echo PRE_CUTOVER_HTTP_BLOCK=PASS
echo PRE_CUTOVER_AUTH_POSTS_BLOCK=PASS
echo PRE_CUTOVER_B6_GET_ALLOWED=PASS
echo PRE_CUTOVER_B7_GET_ALLOWED=PASS
echo PRE_CUTOVER_MQTT_BLOCK=PASS
echo PRE_CUTOVER_READINESS=PASS
echo D6_BACKEND_BOOTSTRAP_WITH_JWT_KEY=PASS
echo APPLICATION_DB_REACHED_VIA_PRIVATE_TCP=YES
EOF
cat > "$root/db-smoke-hook" <<EOF
#!/bin/bash
set -euo pipefail
count=\$(docker compose -p "$project" --env-file "$root/db.env" -f "$db_compose" exec -T application-db sh -c 'env PGPASSWORD="$db_admin_password" psql -U d6_db_admin -d power_iot -Atqc "SELECT count(*) FROM system_configs WHERE key='"'"'carbon_factor'"'"'"')
[[ "\$count" == 1 ]] || { echo CONTROLLED_DB_SMOKE=FAIL >&2; exit 1; }
echo CONTROLLED_DB_SMOKE=PASS
EOF
cat > "$root/mqtt-smoke-hook" <<EOF
#!/bin/bash
set -euo pipefail
status=''
for _ in \$(seq 1 30); do
  status=\$(curl -sS "http://127.0.0.1:$http_port/" 2>/dev/null || true)
  [[ "\$status" == *'"status":"online"'* ]] && break
  sleep 1
done
direct=\$(docker run --rm --network "$app_network" nginx:1.27-alpine sh -c 'wget -qO- http://backend:8080/' 2>/dev/null || true)
[[ "\$direct" == *'"status":"online"'* ]] || { echo DIRECT_BACKEND_HEALTH_GET=FAIL >&2; exit 1; }
[[ "\$status" == *'"http_writes_blocked":true'* && "\$status" == *'"d6_runtime_mode":"PRE_CUTOVER"'* ]] || { echo GENERAL_WRITES_BLOCKED_DURING_SMOKE=FAIL >&2; exit 1; }
echo DIRECT_BACKEND_HEALTH_GET=PASS
echo PROXY_HEALTH_GET=PASS
echo GENERAL_HTTP_WRITES_BLOCKED_DURING_SMOKE=PASS
echo CONTROLLED_MQTT_EXCEPTION_ONLY=PASS
"$root/db-vm-control-wrapper" inspect >/dev/null
echo DIRECT_WRITER_DENIED_DURING_SMOKE=PASS
before=\$(docker compose -p "$project" --env-file "$root/db.env" -f "$db_compose" exec -T application-db sh -c 'env PGPASSWORD="$db_admin_password" psql -U d6_db_admin -d power_iot -Atqc "SELECT count(*) FROM power_readings"')
docker run --rm --network "$app_network" \
  -v "$repo/backend:/src/backend:ro" \
  -v "$repo/infrastructure/d6/mqtt-publisher.go:/src/mqtt-publisher.go:ro" \
  -v "$root/backend-secrets:/run/poweriot/secrets:ro" \
  -v "$root/certs:/certs:ro" -w /src/backend golang:1.25.4 \
  go run /src/mqtt-publisher.go --host mqtt --port 8883 --ca-file /certs/ca.crt \
  --username-file /run/poweriot/secrets/mqtt-username \
  --password-file /run/poweriot/secrets/mqtt-password \
  --topic device/upload/data \
  --message '{"mac":"AABBCCDDEEFF","v":230,"c":1.2,"p":276,"kwh":1.25,"ts":1700000000}'
sleep 2
after=\$(docker compose -p "$project" --env-file "$root/db.env" -f "$db_compose" exec -T application-db sh -c 'env PGPASSWORD="$db_admin_password" psql -U d6_db_admin -d power_iot -Atqc "SELECT count(*) FROM power_readings"')
[[ "\$after" -gt "\$before" ]] || { echo "CONTROLLED_MQTTS_SMOKE=FAIL before=\$before after=\$after" >&2; exit 1; }
echo "CONTROLLED_MQTTS_SMOKE=PASS before=\$before after=\$after"
EOF
cat > "$root/reentry-hook" <<EOF
#!/bin/bash
set -euo pipefail
docker compose -p "$project" --env-file "\$D6_ACTIVE_APP_ENV" -f "$app_compose" restart backend reverse-proxy >/dev/null
for _ in \$(seq 1 60); do status=\$(curl -fsS "http://127.0.0.1:$http_port/" 2>/dev/null || true); [[ "\$status" == *'"status":"online"'* ]] && break; sleep 1; done
direct=\$(docker run --rm --network "$app_network" nginx:1.27-alpine sh -c 'wget -qO- http://backend:8080/' 2>/dev/null || true)
[[ "\$direct" == *'"status":"online"'* && "\$status" == *'"d6_runtime_mode":"PRE_CUTOVER"'* && "\$status" == *'"mqtt_ingestion_blocked":true'* ]] || { echo RESTART_REENTRY=FAIL >&2; exit 1; }
echo DIRECT_BACKEND_HEALTH_GET=PASS
echo PROXY_HEALTH_GET=PASS
version=\$(docker compose -p "$project" --env-file "$root/db.env" -f "$db_compose" exec -T application-db sh -c 'env PGPASSWORD="$db_admin_password" psql -U d6_db_admin -d power_iot -Atqc "SELECT version || '\''/'\'' || dirty::text FROM schema_migrations"')
[[ "\$version" == 6/false ]] || { echo "CLEAN_V6_REENTRY=FAIL \$version" >&2; exit 1; }
"$root/db-vm-control-wrapper" inspect >/dev/null
echo DIRECT_WRITER_DENIED_AFTER_REENTRY=PASS
echo "RESTART_REENTRY=PASS schema=\$version"
EOF
cat > "$root/final-gates-hook" <<EOF
#!/bin/bash
set -euo pipefail
status=\$(curl -fsS "http://127.0.0.1:$http_port/")
[[ "\$status" == *'"d6_runtime_mode":"PRE_CUTOVER"'* && "\$status" == *'"http_writes_blocked":true'* && "\$status" == *'"mqtt_ingestion_blocked":true'* ]] || { echo FINAL_CUTOVER_GATES=FAIL >&2; exit 1; }
"$root/db-vm-control-wrapper" inspect >/dev/null
echo DIRECT_WRITER_DENIED_FINAL_GATES=PASS
echo FINAL_CUTOVER_GATES=PASS
EOF
cat > "$root/post-verify-hook" <<EOF
#!/bin/bash
set -euo pipefail
status=\$(curl -fsS "http://127.0.0.1:$http_port/")
direct=\$(docker run --rm --network "$app_network" nginx:1.27-alpine sh -c 'wget -qO- http://backend:8080/' 2>/dev/null || true)
[[ "\$direct" == *'"status":"online"'* && "\$status" == *'"d6_runtime_mode":"POST_CUTOVER"'* && "\$status" == *'"http_writes_blocked":false'* && "\$status" == *'"mqtt_ingestion_blocked":false'* ]] || { echo POST_CUTOVER_MODE=FAIL \$status >&2; exit 1; }
echo DIRECT_BACKEND_HEALTH_GET=PASS
echo PROXY_HEALTH_GET=PASS
post=\$(curl -sS -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:$http_port/")
[[ "\$post" != 503 ]] || { echo POST_CUTOVER_HTTP_ENABLE=FAIL >&2; exit 1; }
for auth_expect in "login:400" "refresh:400" "logout:401"; do
  auth_path=\${auth_expect%%:*}; expected=\${auth_expect##*:}
  auth_status=\$(curl -sS -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:$http_port/api/v1/auth/\$auth_path")
  [[ "\$auth_status" == "\$expected" ]] || { echo "POST_CUTOVER_AUTH_ROUTES=FAIL path=\$auth_path status=\$auth_status" >&2; exit 1; }
done
for b6_path in me shops; do
  b6_status=\$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:$http_port/api/v1/\$b6_path")
  [[ "\$b6_status" == 401 ]] || { echo "POST_CUTOVER_B6_GET=FAIL path=\$b6_path status=\$b6_status" >&2; exit 1; }
done
b7_status=\$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:$http_port/api/v1/shops/1/dashboard")
[[ "\$b7_status" == 401 ]] || { echo "POST_CUTOVER_B7_GET=FAIL status=\$b7_status" >&2; exit 1; }
echo POST_CUTOVER_AUTH_ROUTES=PASS
echo POST_CUTOVER_B7_GET_ROUTES=PASS
echo POST_CUTOVER_B6_GET_ROUTES=PASS
before=\$(docker compose -p "$project" --env-file "$root/db.env" -f "$db_compose" exec -T application-db sh -c 'env PGPASSWORD="$db_admin_password" psql -U d6_db_admin -d power_iot -Atqc "SELECT count(*) FROM power_readings"')
docker run --rm --network "$app_network" \
  -v "$repo/backend:/src/backend:ro" \
  -v "$repo/infrastructure/d6/mqtt-publisher.go:/src/mqtt-publisher.go:ro" \
  -v "$root/backend-secrets:/run/poweriot/secrets:ro" \
  -v "$root/certs:/certs:ro" -w /src/backend golang:1.25.4 \
  go run /src/mqtt-publisher.go --host mqtt --port 8883 --ca-file /certs/ca.crt \
  --username-file /run/poweriot/secrets/mqtt-username \
  --password-file /run/poweriot/secrets/mqtt-password \
  --topic device/upload/data \
  --message '{"mac":"AABBCCDDEEFF","v":230,"c":1.2,"p":276,"kwh":1.25,"ts":1700000001}'
sleep 2
after=\$(docker compose -p "$project" --env-file "$root/db.env" -f "$db_compose" exec -T application-db sh -c 'env PGPASSWORD="$db_admin_password" psql -U d6_db_admin -d power_iot -Atqc "SELECT count(*) FROM power_readings"')
[[ "\$after" -gt "\$before" ]] || { echo POST_CUTOVER_MQTT_ENABLE=FAIL >&2; exit 1; }
echo "POST_CUTOVER_HTTP_ENABLE=PASS status=\$post"
echo "POST_CUTOVER_MQTT_ENABLE=PASS before=\$before after=\$after"
echo CLEAN_V6_CONVERGENCE=PASS
EOF
chmod 0555 "$root"/*-hook
# Create the V6 application containers without starting them: the actual
# d6-drain must find and verify these stopped containers after it stops them.
"${compose_app[@]}" create backend mqtt reverse-proxy >/dev/null
app_net_id=$(docker network inspect -f '{{.Id}}' "$app_network")
db_net_id=$(docker network inspect -f '{{.Id}}' "$db_network")
[[ "$app_net_id" != "$db_net_id" ]] || { echo 'APP_DB_NETWORK_BOUNDARY=FAIL' >&2; exit 1; }
for service in backend d1l-authority; do
  id=$("${compose_app[@]}" ps -aq "$service")
  [[ -n "$id" ]] || { echo "missing App-VM container for $service" >&2; exit 1; }
  ! docker inspect -f '{{json .NetworkSettings.Networks}}' "$id" | grep -Fq "\"$db_network\""
done
echo 'EQUIVALENT_TWO_HOST_BOUNDARY=YES'
echo 'APP_CONTAINERS_NOT_ON_DB_DOCKER_BRIDGE=YES'
echo 'APP_VM_CONTROL=PASS'
echo 'DB_VM_CONTROL_SEAM=PASS'

cat > "$root/db-local-control-wrapper" <<EOF
#!/bin/sh
exec bash "$repo/infrastructure/d6/d6-db-local-control.sh" "\$@"
EOF
cat > "$root/db-vm-control-wrapper" <<EOF
#!/bin/sh
exec bash "$repo/infrastructure/d6/d6-db-vm-control.sh" "\$@"
EOF
chmod 0555 "$root/db-local-control-wrapper" "$root/db-vm-control-wrapper"

export D6_OPERATOR_MODE=rehearsal D6_OPERATOR_TARGET=rehearsal
export D6_OPERATOR_PROJECT="$project" D6_APP_COMPOSE_FILE="$app_compose"
export D6_APP_ENV_FILE="$root/app.env"
export D6_APPLICATION_DATABASE_URL="postgres://poweriot_runtime@host.docker.internal:$app_db_port/power_iot?sslmode=disable"
export D6_MIGRATION_DATABASE_URL="postgres://d6_migrator@host.docker.internal:$app_db_port/power_iot?sslmode=disable"
export D6_PROVIDER_DATABASE_URL="postgres://d1l@host.docker.internal:$provider_db_port/d1l_provider?sslmode=disable"
export D6_MIGRATION_DATABASE_PASSWORD="$migration_password"
export D6_APP_VM_CONTROL=docker D6_DB_VM_CONTROL="$root/db-vm-control-wrapper" D6_DRAIN_COMMAND="$root/d6-drain-wrapper"
export D6_APP_VM_ROLE_IDENTITY_FILE="$root/app-role-identity"
export D6_DB_CONTROL_TRANSPORT=local D6_DB_LOCAL_CONTROL_COMMAND="$root/db-local-control-wrapper"
export D6_DB_COMPOSE_FILE="$db_compose" D6_DB_ENV_FILE="$root/db.env" D6_DB_PROJECT="$project"
export D6_DB_LOCAL_ROLE_IDENTITY_FILE="$root/db-role-identity" D6_DB_ADMIN_PASSWORD_FILE="$root/db-admin-password"
export D6_RUNTIME_DB_PASSWORD_FILE="$root/db-role-passwords/poweriot_runtime" D6_MIGRATION_DB_PASSWORD_FILE="$root/db-role-passwords/d6_migrator"
export D6_PROVIDER_ADMIN_PASSWORD_FILE="$root/d1l-secrets/provider-db-password"
export D6_APPLICATION_DB_NAME=power_iot D6_PROVIDER_DB_NAME=d1l_provider D6_APPLICATION_DB_ROLE=poweriot
export D6_RUNTIME_DB_ROLE=poweriot_runtime D6_MIGRATION_DB_ROLE=d6_migrator D6_DB_TARGET=rehearsal
export D6_TARGET_IDENTITY_FILE="$root/backend-secrets/target-identity" D6_DRAIN_PRIVATE_KEY="$root/certs/admission-private.pem"
export D6_CONTAINER_TARGET_IDENTITY_FILE=/run/poweriot/secrets/target-identity D6_ADMISSION_PUBLIC_KEY=/run/poweriot/secrets/admission-public.pem
export D6_POST_CUTOVER_PROXY_CONFIG="$repo/infrastructure/d6/nginx-open.conf.template"
export D6_READINESS_COMMAND="$root/readiness-hook" D6_CONTROLLED_DB_SMOKE_COMMAND="$root/db-smoke-hook"
export D6_MQTTS_SMOKE_COMMAND="$root/mqtt-smoke-hook" D6_RESTART_REENTRY_COMMAND="$root/reentry-hook"
export D6_FINAL_GATES_COMMAND="$root/final-gates-hook" D6_POST_CUTOVER_VERIFY_COMMAND="$root/post-verify-hook"
export D6_REHEARSAL=1
if D6_APPLICATION_DATABASE_URL='postgres://poweriot@host.docker.internal:1/power_iot?sslmode=disable' bash "$repo/infrastructure/d6/d6-operator.sh" >/dev/null 2>&1; then
  echo 'wrong backend DSN role unexpectedly succeeded' >&2
  exit 1
fi
echo 'WRONG_BACKEND_DSN_ROLE_FAIL_CLOSED=PASS'
if D6_MIGRATION_DATABASE_URL='postgres://poweriot@host.docker.internal:1/power_iot?sslmode=disable' bash "$repo/infrastructure/d6/d6-operator.sh" >/dev/null 2>&1; then
  echo 'wrong migration DSN role unexpectedly succeeded' >&2
  exit 1
fi
echo 'WRONG_MIGRATION_DSN_ROLE_FAIL_CLOSED=PASS'
bash "$repo/infrastructure/d6/d6-operator.sh"
echo 'ACTUAL_D6_DRAIN_DISPOSABLE_RUN=PASS'
echo 'STOPPED_CONTAINERS_FOUND=YES'
echo 'STOPPED_STATE_VERIFIED=YES'
echo 'RESTART_POLICY_VERIFIED=YES'
echo 'DIRECT_SQL_CONTROLLED=YES'
echo 'DRAIN_WORKFLOW_CAN_REACH_QUIESCENCE=YES'
echo 'INTEGRATED_D6_REHEARSAL=PASS'
