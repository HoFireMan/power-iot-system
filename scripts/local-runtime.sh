#!/usr/bin/env bash
set -Eeuo pipefail

# Power-IoT local runtime lifecycle. This script is deliberately conservative:
# it only manages identities proven to be ACTIVE_CANONICAL and never mutates a
# database or volume.

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
readonly REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd -P)"
discover_canonical_repo() {
  local discovered
  discovered="$(git -C "$REPO_ROOT" worktree list --porcelain 2>/dev/null | awk '$1 == "worktree" {path=$2} $1 == "branch" && $2 == "refs/heads/main" {print path; exit}')"
  printf '%s\n' "${discovered:-$REPO_ROOT}"
}
readonly CANONICAL_REPO_ROOT="${POWER_IOT_CANONICAL_REPO:-$(discover_canonical_repo)}"
readonly CONFIG_ROOT="${POWER_IOT_LOCAL_CONFIG_ROOT:-$CANONICAL_REPO_ROOT}"
readonly SECRET_ROOT="${POWER_IOT_LOCAL_SECRET_ROOT:-$HOME/.local/state/power-iot/dev-secrets}"
readonly STATE_FILE="${POWER_IOT_LOCAL_STATE_FILE:-$HOME/.local/state/power-iot/runbooks/local-runtime-state.md}"
readonly RUNTIME_STATE_ROOT="${POWER_IOT_LOCAL_RUNTIME_STATE_ROOT:-$HOME/.local/state/power-iot/runtime}"
readonly BACKEND_TARGET_STATE="$RUNTIME_STATE_ROOT/backend-target.env"
readonly SIMULATOR_BOOT_STATE="$RUNTIME_STATE_ROOT/simulator-boot-counter"
readonly BACKEND_SESSION="power-iot-backend"
readonly SIMULATOR_SESSION="power-iot-simulator"
readonly FLUTTER_SESSION="power-iot-flutter"
readonly UI_DB_CONTAINER="power_iot_ui_db"
readonly MQTT_CONTAINER="power_iot_mqtt"
readonly LEGACY_DB_CONTAINER="power_iot_db"
readonly DEFAULT_UI_DB_PORT="55435"
readonly DEFAULT_MQTT_PORT="8883"
readonly DEFAULT_BACKEND_PORT="8080"

log() { printf '%s\n' "$*"; }
warn() { printf 'WARN: %s\n' "$*" >&2; }
fail() { printf 'ERROR: %s\n' "$*" >&2; return 1; }

usage() {
  cat <<'EOF'
Usage: scripts/local-runtime.sh <command> [target]

Commands:
  status
  start core|telemetry|ui
  stop simulator|backend|ui|runtime
  restart backend|simulator
  logs backend|simulator
  help

Only ACTIVE_CANONICAL assets are managed. Databases and database volumes are
preserved and never removed or otherwise mutated by this tool.

status exits 0 when the inventory was produced (even when a component is
DEGRADED or UNKNOWN). Invalid commands and refused lifecycle actions exit 2.
EOF
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command is missing: $1"
}

valid_port() {
  [[ "$1" =~ ^[0-9]+$ ]] && (( 1 <= 10#$1 && 10#$1 <= 65535 ))
}

ui_db_port() {
  local value="${POWER_IOT_LOCAL_DB_PORT:-}"
  if [[ -z "$value" ]] && command -v docker >/dev/null 2>&1; then
    value="$(canonical_ui_db_port 2>/dev/null || true)"
  fi
  value="${value:-$DEFAULT_UI_DB_PORT}"
  valid_port "$value" || { warn "invalid POWER_IOT_LOCAL_DB_PORT; using $DEFAULT_UI_DB_PORT"; value="$DEFAULT_UI_DB_PORT"; }
  printf '%s\n' "$value"
}

mqtt_port() {
  local value="${POWER_IOT_LOCAL_MQTT_PORT:-$DEFAULT_MQTT_PORT}"
  valid_port "$value" || { warn "invalid POWER_IOT_LOCAL_MQTT_PORT; using $DEFAULT_MQTT_PORT"; value="$DEFAULT_MQTT_PORT"; }
  printf '%s\n' "$value"
}

backend_port() {
  local value="${POWER_IOT_BACKEND_PORT:-$DEFAULT_BACKEND_PORT}"
  valid_port "$value" || { warn "invalid POWER_IOT_BACKEND_PORT; using $DEFAULT_BACKEND_PORT"; value="$DEFAULT_BACKEND_PORT"; }
  printf '%s\n' "$value"
}

load_runtime_config() {
  local env_file="$CONFIG_ROOT/.env"
  local db_file="$SECRET_ROOT/ui-db.env"
  if [[ -r "$env_file" ]]; then
    set -a
    # shellcheck disable=SC1090
    . "$env_file"
    set +a
  fi
  if [[ -r "$db_file" ]]; then
    set -a
    # shellcheck disable=SC1090
    . "$db_file"
    set +a
  fi

  export HTTP_ADDR="${HTTP_ADDR:-:$(backend_port)}"
  export APP_ENV="${APP_ENV:-development}"
  export D6_RUNTIME_MODE="${D6_RUNTIME_MODE:-POST_CUTOVER}"
  export MQTT_BROKER_URL="${MQTT_BROKER_URL:-tls://127.0.0.1:$(mqtt_port)}"
  export MQTT_CA_FILE="${MQTT_CA_FILE:-$REPO_ROOT/infrastructure/mosquitto/certs/ca.crt}"
  export DEV_DEVICE_MAC="${DEV_DEVICE_MAC:-AABBCCDDEEFF}"
  if [[ -z "${JWT_ACTIVE_PRIVATE_KEY_FILE:-}" && -r "$SECRET_ROOT/jwt-active-private.pem" ]]; then
    export JWT_ACTIVE_PRIVATE_KEY_FILE="$SECRET_ROOT/jwt-active-private.pem"
  fi
}

secret_configured() {
  local key="$1"
  case "$key" in
    JWT_ACTIVE_PRIVATE_KEY_FILE) [[ -n "${JWT_ACTIVE_PRIVATE_KEY_FILE:-}" && -r "$JWT_ACTIVE_PRIVATE_KEY_FILE" ]] ;;
    MQTT_USERNAME|MQTT_PASSWORD|JWT_ACTIVE_KID) [[ -n "${!key:-}" ]] ;;
    DEVSEED_PASSWORD) [[ -r "$SECRET_ROOT/ui-login.env" ]] ;;
    DEVSEED_ADMIN_PASSWORD) [[ -r "$SECRET_ROOT/ui-admin-login.env" ]] ;;
    *) return 1 ;;
  esac
}

require_start_config() {
  load_runtime_config
  local missing=()
  [[ -r "$CONFIG_ROOT/.env" ]] || missing+=("$CONFIG_ROOT/.env")
  [[ -r "$SECRET_ROOT/ui-db.env" ]] || missing+=("$SECRET_ROOT/ui-db.env")
  [[ -n "${POSTGRES_USER:-}" ]] || missing+=(POSTGRES_USER)
  [[ -n "${POSTGRES_PASSWORD:-}" ]] || missing+=(POSTGRES_PASSWORD)
  [[ -n "${POSTGRES_DB:-}" ]] || missing+=(POSTGRES_DB)
  secret_configured MQTT_USERNAME || missing+=(MQTT_USERNAME)
  secret_configured MQTT_PASSWORD || missing+=(MQTT_PASSWORD)
  secret_configured JWT_ACTIVE_KID || missing+=(JWT_ACTIVE_KID)
  secret_configured JWT_ACTIVE_PRIVATE_KEY_FILE || missing+=(JWT_ACTIVE_PRIVATE_KEY_FILE)
  if ((${#missing[@]})); then
    fail "local runtime configuration is incomplete; provide protected sources for: ${missing[*]}"
    return 1
  fi
}

container_field() {
  local container="$1" field="$2"
  docker inspect -f "$field" "$container" 2>/dev/null
}

container_exists() { docker inspect "$1" >/dev/null 2>&1; }
container_running() { [[ "$(container_field "$1" '{{.State.Running}}')" == true ]]; }

canonical_ui_db_identity() {
  require_command docker
  container_exists "$UI_DB_CONTAINER" || { warn "$UI_DB_CONTAINER is absent"; return 1; }
  [[ "$(container_field "$UI_DB_CONTAINER" '{{.Config.Image}}')" == timescale/timescaledb:2.17.2-pg15 ]] || return 1
  local mounts
  mounts="$(container_field "$UI_DB_CONTAINER" '{{range .Mounts}}{{.Name}}:{{.Destination}} {{end}}')"
  [[ "$mounts" == *"power_iot_ui_pgdata:/var/lib/postgresql/data"* ]] || return 1
}

canonical_ui_db_port() {
  canonical_ui_db_identity || return 1
  local published
  published="$(container_field "$UI_DB_CONTAINER" '{{(index (index .NetworkSettings.Ports "5432/tcp") 0).HostPort}}')"
  valid_port "$published" || return 1
  printf '%s\n' "$published"
}

canonical_ui_db() {
  local published
  published="$(canonical_ui_db_port)" || return 1
  [[ "$published" != 5432 ]]
}

validate_backend_db_target() {
  local published configured
  published="$(canonical_ui_db_port)" || { fail 'canonical UI DB published port is unavailable'; return 2; }
  if [[ "$published" == 5432 ]]; then
    fail 'LEGACY_DB_TARGET_REFUSED: canonical UI DB resolves to legacy port 5432'
    return 2
  fi
  configured="${POWER_IOT_LOCAL_DB_PORT:-$published}"
  valid_port "$configured" || { fail 'configured UI DB port is invalid'; return 2; }
  if [[ "$configured" == 5432 ]]; then
    fail 'LEGACY_DB_TARGET_REFUSED: POWER_IOT_LOCAL_DB_PORT=5432 is forbidden'
    return 2
  fi
  if [[ "$configured" != "$published" ]]; then
    fail "CANONICAL_UI_DB_PORT_MISMATCH: configured port does not match published port"
    return 2
  fi
  # This is intentionally constructed in memory and never printed.
  PROVEN_DB_HOST='127.0.0.1'
  PROVEN_DB_PORT="$published"
  PROVEN_DATABASE_URL="postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@${PROVEN_DB_HOST}:${PROVEN_DB_PORT}/${POSTGRES_DB}?sslmode=disable"
  [[ "$PROVEN_DATABASE_URL" == *"@127.0.0.1:${PROVEN_DB_PORT}/${POSTGRES_DB}?sslmode=disable"* ]] || {
    fail 'BACKEND_DB_TARGET_UNVERIFIED: constructed target is not the proven UI DB endpoint'
    return 2
  }
}

canonical_mqtt() {
  require_command docker
  container_exists "$MQTT_CONTAINER" || { warn "$MQTT_CONTAINER is absent"; return 1; }
  [[ "$(container_field "$MQTT_CONTAINER" '{{.Config.Image}}')" == eclipse-mosquitto:2 ]] || return 1
  [[ "$(container_field "$MQTT_CONTAINER" '{{index .Config.Labels "com.docker.compose.project"}}')" == infrastructure ]] || return 1
  [[ "$(container_field "$MQTT_CONTAINER" '{{index .Config.Labels "com.docker.compose.service"}}')" == mqtt ]] || return 1
  [[ "$(container_field "$MQTT_CONTAINER" '{{index .Config.Labels "com.docker.compose.project.working_dir"}}')" == "$CANONICAL_REPO_ROOT/infrastructure" ]] || return 1
  [[ "$(container_field "$MQTT_CONTAINER" '{{json .NetworkSettings.Ports}}')" == *"8883"* ]] || return 1
}

state_value() {
  local file="$1" key="$2"
  [[ -r "$file" ]] || return 1
  awk -F= -v wanted="$key" '$1 == wanted {sub(/^[^=]*=/, ""); print; exit}' "$file"
}

record_backend_target() {
  local db_port="$1" session_repo pane_pid tmp
  mkdir -p "$RUNTIME_STATE_ROOT"
  chmod 700 "$RUNTIME_STATE_ROOT"
  session_repo="$(session_path "$BACKEND_SESSION")"
  pane_pid="$(session_pane_pid "$BACKEND_SESSION")"
  tmp="${BACKEND_TARGET_STATE}.tmp.$$"
  umask 077
  {
    printf 'backend_session=%s\n' "$BACKEND_SESSION"
    printf 'backend_repo=%s\n' "$session_repo"
    printf 'backend_db_host=127.0.0.1\n'
    printf 'backend_db_port=%s\n' "$db_port"
    printf 'backend_pane_pid=%s\n' "$pane_pid"
  } >"$tmp"
  chmod 600 "$tmp"
  mv -f "$tmp" "$BACKEND_TARGET_STATE"
}

backend_target_proven() {
  local current_pid current_repo
  validate_backend_db_target || return $?
  [[ "$(state_value "$BACKEND_TARGET_STATE" backend_session || true)" == "$BACKEND_SESSION" ]] || return 1
  current_repo="$(session_path "$BACKEND_SESSION")"
  [[ -n "$current_repo" ]] || return 1
  path_matches "$current_repo" "$REPO_ROOT/backend" || return 1
  [[ "$(state_value "$BACKEND_TARGET_STATE" backend_repo || true)" == "$current_repo" ]] || return 1
  [[ "$(state_value "$BACKEND_TARGET_STATE" backend_db_host || true)" == "$PROVEN_DB_HOST" ]] || return 1
  [[ "$(state_value "$BACKEND_TARGET_STATE" backend_db_port || true)" == "$PROVEN_DB_PORT" ]] || return 1
  current_pid="$(session_pane_pid "$BACKEND_SESSION")"
  [[ -n "$current_pid" && "$(state_value "$BACKEND_TARGET_STATE" backend_pane_pid || true)" == "$current_pid" ]]
}

container_health() {
  local name="$1"
  local status health
  status="$(container_field "$name" '{{.State.Status}}' 2>/dev/null || printf unknown)"
  health="$(container_field "$name" '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' 2>/dev/null || printf unknown)"
  if [[ "$status" != running ]]; then
    printf 'STOPPED'
  elif [[ "$health" == healthy || "$health" == none ]]; then
    printf 'RUNNING'
  else
    printf 'DEGRADED'
  fi
}

start_canonical_container() {
  local name="$1" verify="$2"
  "$verify" || { fail "refusing to manage $name: ACTIVE_CANONICAL provenance is not proven"; return 2; }
  if container_running "$name"; then
    log "$name: already running (no recreation)"
    return 0
  fi
  docker start "$name" >/dev/null || { fail "failed to start canonical container $name"; return 1; }
  log "$name: started"
}

stop_canonical_container() {
  local name="$1" verify="$2"
  "$verify" || { fail "refusing to manage $name: ACTIVE_CANONICAL provenance is not proven"; return 2; }
  if ! container_running "$name"; then
    log "$name: already stopped"
    return 0
  fi
  docker stop "$name" >/dev/null || { fail "failed to stop canonical container $name"; return 1; }
  log "$name: stopped"
}

wait_for() {
  local description="$1" command="$2" attempts="${3:-20}"
  local i
  for ((i=1; i<=attempts; i++)); do
    if eval "$command" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  fail "$description did not become ready within ${attempts}s"
}

backend_url() { printf 'http://127.0.0.1:%s/' "$(backend_port)"; }
backend_health_body() { curl --max-time 2 -fsS "$(backend_url)"; }

backend_health_ok() {
  local body
  body="$(backend_health_body 2>/dev/null)" || return 1
  [[ "$body" == *'"db":"connected"'* ]] || return 1
  [[ "$body" == *'"mqtt_ready":true'* ]] || return 1
  [[ "$body" == *'"mqtt_ingestion_blocked":false'* ]] || return 1
}

backend_health_label() {
  local body
  body="$(backend_health_body 2>/dev/null)" || { printf 'UNKNOWN'; return 0; }
  if [[ "$body" == *'"db":"connected"'* && "$body" == *'"mqtt_ready":true'* && "$body" == *'"mqtt_ingestion_blocked":false'* ]]; then
    printf 'RUNNING'
  elif [[ "$body" == *'"db":"connected"'* ]]; then
    printf 'DEGRADED'
  else
    printf 'UNKNOWN'
  fi
}

backend_ingestion_label() {
  local body
  body="$(backend_health_body 2>/dev/null)" || { printf 'UNKNOWN'; return 0; }
  if [[ "$body" == *'"mqtt_ingestion_blocked":false'* ]]; then printf 'ENABLED';
  elif [[ "$body" == *'"mqtt_ingestion_blocked":true'* ]]; then printf 'BLOCKED';
  else printf 'UNKNOWN'; fi
}

session_exists() { tmux has-session -t "$1" 2>/dev/null; }
session_path() { tmux display-message -p -t "$1" '#{pane_current_path}' 2>/dev/null; }
session_pane_pid() { tmux list-panes -t "$1" -F '#{pane_pid}' 2>/dev/null | head -n 1; }

# A tmux name alone is not ownership. The session path and a descendant's
# canonical working directory are both required before a process is stopped.
descendant_has_cwd() {
  local root="$1" expected="$2" pattern="$3" pid cwd args
  cwd="$(readlink "/proc/$root/cwd" 2>/dev/null || true)"
  args="$(tr '\0' ' ' <"/proc/$root/cmdline" 2>/dev/null || true)"
  if path_matches "$cwd" "$expected" && [[ "$args" == *"$pattern"* ]]; then return 0; fi
  for pid in $(ps -eo pid=,ppid= | awk -v root="$root" '$2 == root {print $1}'); do
    cwd="$(readlink "/proc/$pid/cwd" 2>/dev/null || true)"
    args="$(tr '\0' ' ' <"/proc/$pid/cmdline" 2>/dev/null || true)"
    if path_matches "$cwd" "$expected" && [[ "$args" == *"$pattern"* ]]; then return 0; fi
    if descendant_has_cwd "$pid" "$expected" "$pattern"; then return 0; fi
  done
  return 1
}

path_matches() {
  local actual="$1" expected="$2" alternate
  alternate="$CANONICAL_REPO_ROOT/${expected#"$REPO_ROOT/"}"
  [[ "$actual" == "$expected" || "$actual" == "$alternate" ]]
}

owned_session() {
  local session="$1" expected="$2" pattern="$3" root
  session_exists "$session" || return 1
  path_matches "$(session_path "$session")" "$expected" || return 1
  root="$(session_pane_pid "$session")"
  [[ -n "$root" ]] || return 1
  descendant_has_cwd "$root" "$expected" "$pattern"
}

refuse_unowned_session() {
  local session="$1" expected="$2" pattern="$3"
  if session_exists "$session" && ! owned_session "$session" "$expected" "$pattern"; then
    fail "$session exists but PROCESS_OWNERSHIP_UNVERIFIED; refusing to stop or duplicate it"
    return 2
  fi
  return 0
}

start_backend() {
  require_start_config
  require_command tmux
  canonical_ui_db || { fail "refusing Backend start: canonical UI DB provenance is not proven"; return 2; }
  container_running "$UI_DB_CONTAINER" || { fail "canonical UI DB is not running"; return 1; }
  canonical_mqtt || { fail "refusing Backend start: canonical MQTT provenance is not proven"; return 2; }
  container_running "$MQTT_CONTAINER" || { fail "canonical MQTT is not running"; return 1; }
  wait_for "canonical MQTT" "[[ \"\$(container_health '$MQTT_CONTAINER')\" == RUNNING ]]" 5 || return 1
  validate_backend_db_target || return $?
  refuse_unowned_session "$BACKEND_SESSION" "$REPO_ROOT/backend" 'cmd/server' || return $?
  if owned_session "$BACKEND_SESSION" "$REPO_ROOT/backend" 'cmd/server'; then
    backend_target_proven || { fail 'BACKEND_DB_TARGET_UNVERIFIED: existing Backend launch metadata does not prove the UI DB target'; return 2; }
    if backend_health_ok; then log 'Backend: already healthy (no duplicate)'; return 0; fi
    warn 'canonical Backend session exists but is not healthy; refusing duplicate start'
    return 1
  fi
  local db_port="$PROVEN_DB_PORT" mqtt="$(mqtt_port)" port="$(backend_port)" env_file="$CONFIG_ROOT/.env" db_file="$SECRET_ROOT/ui-db.env"
  local mqtt_ca_file="${MQTT_CA_FILE:-$CONFIG_ROOT/infrastructure/mosquitto/certs/ca.crt}" jwt_private_file="${JWT_ACTIVE_PRIVATE_KEY_FILE:-}"
  local command
  if [[ "$mqtt_ca_file" != /* ]]; then mqtt_ca_file="$CONFIG_ROOT/${mqtt_ca_file#./}"; fi
  if [[ -n "$jwt_private_file" && "$jwt_private_file" != /* ]]; then jwt_private_file="$CONFIG_ROOT/${jwt_private_file#./}"; fi
  command="cd '$REPO_ROOT/backend' && set -a && . '$env_file' && . '$db_file' && set +a && export DATABASE_URL=\"postgres://\${POSTGRES_USER}:\${POSTGRES_PASSWORD}@127.0.0.1:$db_port/\${POSTGRES_DB}?sslmode=disable\" HTTP_ADDR=:$port D6_RUNTIME_MODE=POST_CUTOVER MQTT_BROKER_URL=tls://127.0.0.1:$mqtt MQTT_CA_FILE='$mqtt_ca_file' JWT_ACTIVE_PRIVATE_KEY_FILE='$jwt_private_file' && exec go run ./cmd/server"
  tmux new-session -d -s "$BACKEND_SESSION" -c "$REPO_ROOT/backend" "$command" || { fail 'failed to create canonical Backend session'; return 1; }
  record_backend_target "$db_port"
  wait_for 'Backend health' 'backend_health_ok' 30
}

start_mqtt() { start_canonical_container "$MQTT_CONTAINER" canonical_mqtt; }
start_ui_db() { start_canonical_container "$UI_DB_CONTAINER" canonical_ui_db; }

start_core() {
  require_start_config || return $?
  start_ui_db || return $?
  wait_for 'UI DB container' "[[ \"\$(container_health '$UI_DB_CONTAINER')\" == RUNNING ]]" 15 || return 1
  start_mqtt || return $?
  wait_for 'MQTT container' "[[ \"\$(container_health '$MQTT_CONTAINER')\" == RUNNING ]]" 15 || return 1
  start_backend || return $?
  update_state 'start core'
}

simulator_ack_ok() {
  local capture
  capture="$(tmux capture-pane -p -t "$SIMULATOR_SESSION" -S -200 2>/dev/null || true)"
  [[ "$capture" == *'ACK stored'* || "$capture" == *'ACK duplicate'* ]]
}

simulator_stored_ack_for_boot() {
  local boot="$1" capture
  capture="$(tmux capture-pane -p -t "$SIMULATOR_SESSION" -S -200 2>/dev/null || true)"
  [[ "$capture" == *"ACK stored"*"boot=$boot"* ]]
}

read_simulator_boot() {
  local value
  if [[ -r "$SIMULATOR_BOOT_STATE" ]]; then
    value="$(<"$SIMULATOR_BOOT_STATE")"
    [[ "$value" =~ ^[0-9]+$ ]] && printf '%s\n' "$value" && return 0
  fi
  printf 'unknown\n'
  return 1
}

allocate_simulator_boot() {
  local directory="$RUNTIME_STATE_ROOT" lock="${SIMULATOR_BOOT_STATE}.lock" attempts=0 acquired=0 current next tmp
  mkdir -p "$directory"
  chmod 700 "$directory"
  while ((attempts < 200)); do
    if (umask 077 && mkdir "$lock" 2>/dev/null); then
      acquired=1
      break
    fi
    # A lock with no live owner is intentionally not removed here: removing
    # it while another allocator is between mkdir and pid write could allow
    # duplicate counters. A stale lock fails closed after the bounded wait.
    attempts=$((attempts + 1))
    sleep 0.05
  done
  ((acquired == 1)) || { fail 'SIMULATOR_BOOT_COUNTER_LOCK_TIMEOUT'; return 1; }
  printf '%s\n' "$$" >"$lock/pid"
  trap 'rm -f "$lock/pid"; rmdir "$lock" 2>/dev/null || true' RETURN
  current=0
  if [[ -e "$SIMULATOR_BOOT_STATE" ]]; then
    current="$(<"$SIMULATOR_BOOT_STATE")"
    [[ "$current" =~ ^[0-9]+$ ]] || { fail 'simulator boot counter state is invalid'; return 1; }
  fi
  next=$((current + 1))
  tmp="${SIMULATOR_BOOT_STATE}.tmp.$$"
  printf '%s\n' "$next" >"$tmp"
  chmod 600 "$tmp"
  mv -f "$tmp" "$SIMULATOR_BOOT_STATE"
  printf '%s\n' "$next"
  trap - RETURN
  rm -f "$lock/pid"
  rmdir "$lock"
}

start_telemetry() {
  start_core || return $?
  require_command tmux
  require_start_config
  refuse_unowned_session "$SIMULATOR_SESSION" "$REPO_ROOT/tools/device-simulator" 'go run' || return $?
  if owned_session "$SIMULATOR_SESSION" "$REPO_ROOT/tools/device-simulator" 'go run'; then
    if simulator_ack_ok; then log 'Simulator: already running with application ACK (no duplicate)'; else log 'Simulator: already running; ACK not yet observed'; fi
    return 0
  fi
  local env_file="$CONFIG_ROOT/.env" mac="${DEV_DEVICE_MAC:-AABBCCDDEEFF}" normalized_mac boot_counter
  normalized_mac="${mac//:/}"
  normalized_mac="${normalized_mac//-/}"
  normalized_mac="${normalized_mac// /}"
  [[ "$normalized_mac" =~ ^[A-Fa-f0-9]{12}$ ]] || { fail 'DEV_DEVICE_MAC is not a valid local device identity'; return 2; }
  boot_counter="$(allocate_simulator_boot)" || { fail 'SIMULATOR_BOOT_COUNTER_ALLOCATION_FAILED'; return 1; }
  tmux new-session -d -s "$SIMULATOR_SESSION" -c "$REPO_ROOT/tools/device-simulator" \
    "set -a && . '$env_file' && set +a && exec go run . --mode continuous --device-mac '$mac' --publish-interval 5s --coverage-profile --clock-synchronized=true --boot-counter '$boot_counter' --start-seq 0" \
    || { fail 'failed to create canonical Simulator session'; return 1; }
  wait_for 'Simulator stored application ACK' "simulator_stored_ack_for_boot '$boot_counter'" 30 || return 1
  update_state 'start telemetry'
}

android_device() {
  local wanted="${POWER_IOT_ANDROID_DEVICE:-emulator-5554}" line device state
  require_command adb || return 1
  while IFS= read -r line; do
    device="${line%%[[:space:]]*}"
    state="${line#*[[:space:]]}"
    if [[ "$device" == "$wanted" && "$state" == device* ]]; then printf '%s\n' "$device"; return 0; fi
  done < <(adb devices 2>/dev/null | tail -n +2)
  return 1
}

start_ui() {
  start_core || return $?
  require_command tmux
  refuse_unowned_session "$FLUTTER_SESSION" "$REPO_ROOT/mobile" 'flutter_tools.snapshot' || return $?
  if owned_session "$FLUTTER_SESSION" "$REPO_ROOT/mobile" 'flutter_tools.snapshot'; then
    log 'Flutter: already running (no duplicate)'
    return 0
  fi
  local device
  device="$(android_device)" || { fail 'no approved Android Emulator is connected; reuse an existing emulator or set POWER_IOT_ANDROID_DEVICE'; return 1; }
  require_command flutter
  tmux new-session -d -s "$FLUTTER_SESSION" -c "$REPO_ROOT/mobile" \
    "exec flutter run --no-pub -d '$device' --dart-define=POWER_IOT_BASE_URL=http://10.0.2.2:$(backend_port)" \
    || { fail 'failed to create canonical Flutter session'; return 1; }
  update_state 'start ui'
  log 'Flutter: started against the canonical Backend endpoint'
}

stop_session() {
  local session="$1" path="$2" pattern="$3"
  require_command tmux
  session_exists "$session" || { log "$session: already stopped"; return 0; }
  owned_session "$session" "$path" "$pattern" || { fail "$session: PROCESS_OWNERSHIP_UNVERIFIED; refusing to stop"; return 2; }
  tmux kill-session -t "$session" || { fail "failed to stop owned session $session"; return 1; }
  log "$session: stopped"
}

stop_simulator() { stop_session "$SIMULATOR_SESSION" "$REPO_ROOT/tools/device-simulator" 'go run'; update_state 'stop simulator'; }
stop_backend() { stop_session "$BACKEND_SESSION" "$REPO_ROOT/backend" 'cmd/server'; update_state 'stop backend'; }
stop_ui() { stop_session "$FLUTTER_SESSION" "$REPO_ROOT/mobile" 'flutter_tools.snapshot'; update_state 'stop ui'; }

stop_runtime() {
  local result=0
  stop_ui || result=$?
  stop_simulator || result=$?
  stop_backend || result=$?
  # MQTT is canonical and ephemeral; the UI DB and legacy DB are deliberately
  # absent from this function. In particular, stop runtime preserves all DBs.
  if canonical_mqtt; then
    stop_canonical_container "$MQTT_CONTAINER" canonical_mqtt || result=$?
  else
    fail 'refusing to stop MQTT: ACTIVE_CANONICAL provenance is not proven' || true
    result=2
  fi
  update_state 'stop runtime'
  return "$result"
}

restart_backend() {
  stop_backend || return $?
  start_core
}

restart_simulator() {
  stop_simulator || return $?
  start_telemetry
}

redact_output() {
  python3 -c '
import os, sys
text = sys.stdin.read()
for key in ("POSTGRES_PASSWORD", "MQTT_PASSWORD", "JWT_ACTIVE_PRIVATE_KEY", "DEVSEED_PASSWORD", "DEVSEED_ADMIN_PASSWORD"):
    value = os.environ.get(key, "")
    if value:
        text = text.replace(value, "[REDACTED]")
print(text, end="")
'
}

logs_session() {
  local session="$1" path="$2" pattern="$3"
  require_command tmux
  owned_session "$session" "$path" "$pattern" || { fail "$session: PROCESS_OWNERSHIP_UNVERIFIED; refusing to show logs"; return 2; }
  tmux capture-pane -p -t "$session" -S -200 | redact_output
}

update_state() {
  local action="$1" dir
  dir="$(dirname -- "$STATE_FILE")"
  mkdir -p "$dir"
  if [[ -w "$STATE_FILE" || ! -e "$STATE_FILE" ]]; then
    {
      printf '\n## Operator observation\n'
      printf '%s\n' "- Last operator action: $action"
      printf '%s\n' "- Last operator observation time: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
      printf '%s\n' "- Managed database policy: UI DB preserved; legacy DB preserve-only"
      printf '%s\n' "- Managed runtime identities: ACTIVE_CANONICAL only"
    } >>"$STATE_FILE" || warn "could not update local runtime state: $STATE_FILE"
  else
    warn "could not update local runtime state: $STATE_FILE"
  fi
}

status_component() {
  local name="$1"
  shift
  printf '%-16s ' "$name"
  "$@" || printf 'UNKNOWN'
  printf '\n'
}

local_state_fact() {
  local fact="$1"
  [[ -r "$STATE_FILE" ]] || return 1
  case "$fact" in
    schema) awk '/schema state/{if (index($0, "CLEAN_B02")) value="CLEAN_B02"} END{if (value) print value}' "$STATE_FILE" | tail -n 1 ;;
    coverage) awk -F'= ' '/coverage.max_interval_ms/{value=$2} END{if (value) print value}' "$STATE_FILE" | tr -d '`' ;;
    tariff) awk -F': ' '/Development Shop tariff/{value=$2} END{if (value) print value}' "$STATE_FILE" | tr -d '`' ;;
    billing) awk -F': ' '/Billing plan/{value=$2} END{if (value) print value}' "$STATE_FILE" | tr -d '`' ;;
    *) return 1 ;;
  esac
}

status() {
  local simulator_boot schema_fact coverage_fact tariff_fact billing_fact
  # This function intentionally has no writes, starts, stops, or database
  # connections. Each probe is isolated so one unavailable tool is reportable.
  load_runtime_config >/dev/null 2>&1 || true
  schema_fact="$(local_state_fact schema || true)"
  coverage_fact="$(local_state_fact coverage || true)"
  tariff_fact="$(local_state_fact tariff || true)"
  billing_fact="$(local_state_fact billing || true)"
  printf 'Repository\n'
  printf '  root=%s\n' "$REPO_ROOT"
  printf '  branch='; git -C "$REPO_ROOT" branch --show-current 2>/dev/null || printf UNKNOWN; printf '\n'
  printf '  HEAD='; git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || printf UNKNOWN; printf '\n'
  printf '  origin/main='; git -C "$REPO_ROOT" rev-parse --short origin/main 2>/dev/null || printf UNKNOWN; printf '\n'

  printf 'Services\n'
  if canonical_ui_db; then status_component 'UI DB' container_health "$UI_DB_CONTAINER"; else printf '%-16s UNKNOWN (not proven canonical)\n' 'UI DB'; fi
  if canonical_ui_db; then printf '  UI DB endpoint=127.0.0.1:%s source=LIVE_VERIFIED\n' "$(ui_db_port)"; printf '  schema=%s source=%s\n' "${schema_fact:-UNKNOWN}" "$([[ -n "$schema_fact" ]] && printf LOCAL_STATE || printf UNKNOWN)"; fi
  if container_exists "$LEGACY_DB_CONTAINER"; then printf '%-16s PRESERVE_ONLY port=5432\n' 'Legacy DB'; else printf '%-16s PRESERVE_ONLY (not inspected)\n' 'Legacy DB'; fi
  if canonical_mqtt; then status_component MQTT container_health "$MQTT_CONTAINER"; printf '  MQTT endpoint=tls://127.0.0.1:%s source=LIVE_VERIFIED\n' "$(mqtt_port)"; else printf '%-16s UNKNOWN (not proven canonical)\n' MQTT; fi

  if session_exists "$BACKEND_SESSION" && owned_session "$BACKEND_SESSION" "$REPO_ROOT/backend" 'cmd/server'; then
    if backend_target_proven; then
      printf '%-16s %s health=%s source=LIVE_VERIFIED db_target=UI_DB:%s source=LOCAL_STATE mqtt_ingestion=%s\n' Backend "$(backend_health_label)" "$(backend_health_label)" "$PROVEN_DB_PORT" "$(backend_ingestion_label)"
    else
      printf '%-16s UNKNOWN BACKEND_DB_TARGET_UNVERIFIED health=%s\n' Backend "$(backend_health_label)"
    fi
  elif session_exists "$BACKEND_SESSION"; then
    printf '%-16s UNKNOWN PROCESS_OWNERSHIP_UNVERIFIED\n' Backend
  else
    printf '%-16s %s (canonical session absent)\n' Backend "$(backend_health_label)"
  fi

  if session_exists "$SIMULATOR_SESSION" && owned_session "$SIMULATOR_SESSION" "$REPO_ROOT/tools/device-simulator" 'go run'; then
    simulator_boot="$(read_simulator_boot || true)"
    if simulator_stored_ack_for_boot "$simulator_boot"; then
      printf '%-16s RUNNING mode=continuous interval=5s coverage=yes ACK=STORED\n' Simulator
    elif simulator_ack_ok; then
      printf '%-16s RUNNING mode=continuous interval=5s coverage=yes ACK=DUPLICATE (existing diagnostic)\n' Simulator
    else
      printf '%-16s DEGRADED mode=continuous interval=5s coverage=yes ACK=UNKNOWN\n' Simulator
    fi
  elif session_exists "$SIMULATOR_SESSION"; then
    printf '%-16s UNKNOWN PROCESS_OWNERSHIP_UNVERIFIED\n' Simulator
  else
    printf '%-16s STOPPED\n' Simulator
  fi

  if adb devices 2>/dev/null | grep -qE '^emulator-[0-9]+[[:space:]]+device$'; then
    printf '%-16s RUNNING device=%s (reused; not operator-owned)\n' 'Android Emulator' "$(adb devices 2>/dev/null | awk '/^emulator-[0-9]+[[:space:]]+device$/{print $1; exit}')"
  else
    printf '%-16s STOPPED\n' 'Android Emulator'
  fi
  if session_exists "$FLUTTER_SESSION" && owned_session "$FLUTTER_SESSION" "$REPO_ROOT/mobile" 'flutter_tools.snapshot'; then
    printf '%-16s RUNNING base_url=http://10.0.2.2:%s\n' Flutter "$(backend_port)"
  elif session_exists "$FLUTTER_SESSION"; then
    printf '%-16s UNKNOWN PROCESS_OWNERSHIP_UNVERIFIED\n' Flutter
  else
    printf '%-16s STOPPED\n' Flutter
  fi

  printf 'Configuration\n'
  printf '  schema=%s source=%s\n' "${schema_fact:-UNKNOWN}" "$([[ -n "$schema_fact" ]] && printf LOCAL_STATE || printf UNKNOWN)"
  printf '  coverage.max_interval_ms=%s source=%s\n' "${coverage_fact:-UNKNOWN}" "$([[ -n "$coverage_fact" ]] && printf LOCAL_STATE || printf UNKNOWN)"
  printf '  shop_tariff=%s source=%s\n' "${tariff_fact:-UNKNOWN}" "$([[ -n "$tariff_fact" ]] && printf LOCAL_STATE || printf UNKNOWN)"
  printf '  billing_plan=%s source=%s\n' "${billing_fact:-UNKNOWN}" "$([[ -n "$billing_fact" ]] && printf LOCAL_STATE || printf UNKNOWN)"
  for key in MQTT_USERNAME MQTT_PASSWORD JWT_ACTIVE_KID JWT_ACTIVE_PRIVATE_KEY_FILE DEVSEED_PASSWORD DEVSEED_ADMIN_PASSWORD; do
    if secret_configured "$key"; then printf '  %-28s CONFIGURED\n' "$key"; else printf '  %-28s MISSING\n' "$key"; fi
  done
  return 0
}

main() {
  local command="${1:-help}" target="${2:-}"
  case "$command" in
    help|-h|--help) usage ;;
    status) [[ -z "$target" ]] || { usage >&2; return 2; }; status ;;
    start)
      case "$target" in core) start_core ;; telemetry) start_telemetry ;; ui) start_ui ;; *) fail "invalid start profile: ${target:-<missing>}; expected core, telemetry, or ui"; return 2 ;; esac
      ;;
    stop)
      case "$target" in simulator) stop_simulator ;; backend) stop_backend ;; ui) stop_ui ;; runtime) stop_runtime ;; *) fail "invalid stop target: ${target:-<missing>}; expected simulator, backend, ui, or runtime"; return 2 ;; esac
      ;;
    restart)
      case "$target" in backend) restart_backend ;; simulator) restart_simulator ;; *) fail "invalid restart target: ${target:-<missing>}; expected backend or simulator"; return 2 ;; esac
      ;;
    logs)
      case "$target" in backend) logs_session "$BACKEND_SESSION" "$REPO_ROOT/backend" 'cmd/server' ;; simulator) logs_session "$SIMULATOR_SESSION" "$REPO_ROOT/tools/device-simulator" 'go run' ;; *) fail "invalid logs target: ${target:-<missing>}; expected backend or simulator"; return 2 ;; esac
      ;;
    *) fail "unknown command: $command"; usage >&2; return 2 ;;
  esac
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
