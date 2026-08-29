#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
OP="$ROOT/scripts/local-runtime.sh"
WRAPPER="$ROOT/tools/windows/power-iot-local.bat"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass() { printf 'PASS %s\n' "$1"; }
fail() { printf 'FAIL %s\n' "$1" >&2; exit 1; }

"$OP" help >/dev/null || fail 'help succeeds'
pass 'help succeeds'
if "$OP" unknown-command >/dev/null 2>&1; then fail 'unknown command rejected'; fi
pass 'unknown command rejected'
if "$OP" start invalid-profile >/dev/null 2>&1; then fail 'invalid profile rejected'; fi
pass 'invalid profile rejected'

state="$TMP/state.md"
cat >"$state" <<'EOF'
- UI DB: schema state `CLEAN_B02`
- coverage.max_interval_ms = 5000
- Development Shop tariff: `LIGHTING_COMMERCIAL`
- Billing plan: `LIGHTING_COMMERCIAL_NON_TOU`
EOF
before="$(stat -c %Y "$state")"
POWER_IOT_LOCAL_STATE_FILE="$state" "$OP" status >/dev/null || fail 'status is readable'
after="$(stat -c %Y "$state")"
[[ "$before" == "$after" ]] || fail 'status is read-only'
pass 'status is read-only'
status_output="$(POWER_IOT_LOCAL_STATE_FILE="$state" "$OP" status)"
grep -Fq 'schema=CLEAN_B02 source=LOCAL_STATE' <<<"$status_output" || fail 'status schema source'
grep -Fq 'coverage.max_interval_ms=5000 source=LOCAL_STATE' <<<"$status_output" || fail 'status coverage source'
if ! grep -Fq 'db_target=UI_DB:' <<<"$status_output" && ! grep -Fq 'BACKEND_DB_TARGET_UNVERIFIED' <<<"$status_output"; then fail 'status backend target source'; fi
! grep -Fq 'energy-diagnosis' <<<"$status_output" || fail 'status exposed unrelated internals'
pass 'status distinguishes local state and proven target sources'

# Exercise Flutter command construction through the public start-ui seam with
# fake lifecycle dependencies. This catches the mobile configuration-name
# regression without starting an emulator.
flutter_command="$(
  source "$OP"
  require_command() { :; }
  start_core() { :; }
  refuse_unowned_session() { :; }
  owned_session() { return 1; }
  android_device() { printf 'emulator-5554\\n'; }
  update_state() { :; }
  tmux() { [[ "$1" == new-session ]] && printf '%s\\n' "$*"; }
  start_ui
)"
grep -Fq -- '--dart-define=POWER_IOT_BASE_URL=http://10.0.2.2:8080' <<<"$flutter_command" || fail 'Flutter uses POWER_IOT_BASE_URL'
! grep -Fq 'API_BASE_URL' <<<"$flutter_command" || fail 'Flutter does not use API_BASE_URL'
pass 'Flutter base URL key is POWER_IOT_BASE_URL'

# Fake Docker/session seams prove both legacy and mismatched canonical-port
# refusals happen before any Backend process launch.
backend_guard_test() {
  local configured="$1" published="$2" expected="$3"
  if POWER_IOT_LOCAL_DB_PORT="$configured" EXPECTED="$expected" PUBLISHED="$published" bash -c '
    source "$1"
    require_start_config() { :; }; require_command() { :; }
    canonical_ui_db() { :; }; canonical_ui_db_port() { printf "%s\\n" "$PUBLISHED"; }
    canonical_mqtt() { :; }; container_running() { :; }; wait_for() { :; }
    refuse_unowned_session() { :; }; owned_session() { return 1; }
    record_backend_target() { :; }; tmux() { printf "LAUNCHED\\n"; }
    POSTGRES_USER=test POSTGRES_PASSWORD=test POSTGRES_DB=power_iot
    export POSTGRES_USER POSTGRES_PASSWORD POSTGRES_DB
    start_backend
  ' _ "$OP" >"$TMP/backend-guard.out" 2>&1; then
    fail "$expected"
  fi
  ! grep -Fq LAUNCHED "$TMP/backend-guard.out" || fail "$expected launched Backend"
}
backend_guard_test 5432 55435 'legacy DB target refused'
pass 'POWER_IOT_LOCAL_DB_PORT=5432 is refused before launch'
backend_guard_test 55435 5432 'discovered legacy DB target refused'
pass 'discovered canonical endpoint on 5432 is refused before launch'
backend_guard_test 55434 55435 'canonical port mismatch refused'
pass 'configured DB port mismatch is refused before launch'

# A healthy existing session without matching operator target metadata is not
# treated as canonical.
if EXPECTED=unverified bash -c '
  source "$1"
  require_start_config() { :; }; require_command() { :; }
  canonical_ui_db() { :; }; canonical_ui_db_port() { printf "55435\\n"; }
  canonical_mqtt() { :; }; container_running() { :; }; wait_for() { :; }
  refuse_unowned_session() { :; }; owned_session() { return 0; }
  backend_target_proven() { return 1; }; tmux() { printf "LAUNCHED\\n"; }
  POSTGRES_USER=test POSTGRES_PASSWORD=test POSTGRES_DB=power_iot
  export POSTGRES_USER POSTGRES_PASSWORD POSTGRES_DB
  start_backend
' _ "$OP" >"$TMP/backend-existing.out" 2>&1; then fail 'unverified existing Backend accepted'; fi
! grep -Fq LAUNCHED "$TMP/backend-existing.out" || fail 'unverified existing Backend launched duplicate'
 grep -Fq BACKEND_DB_TARGET_UNVERIFIED "$TMP/backend-existing.out" || fail 'unverified Backend diagnostic'
pass 'existing Backend requires proven DB target'

# A mismatched session must not be killed merely because its name is familiar.
mkdir -p "$TMP/bin"
cat >"$TMP/bin/tmux" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  has-session) exit 0 ;;
  display-message) printf '/tmp/unrelated-project\n' ;;
  list-panes) printf '999999\n' ;;
  kill-session) printf 'KILL\n' >>"${FAKE_TMUX_LOG:?}" ;;
esac
EOF
chmod +x "$TMP/bin/tmux"
if PATH="$TMP/bin:$PATH" FAKE_TMUX_LOG="$TMP/tmux.log" POWER_IOT_LOCAL_STATE_FILE="$TMP/state.md" "$OP" stop simulator >/dev/null 2>&1; then fail 'unknown process ownership refused'; fi
[[ ! -s "$TMP/tmux.log" ]] || fail 'unowned process was not killed'
pass 'unknown process ownership refuses kill'

# Boot allocation is non-secret, monotonic, atomic, and independent of the
# real simulator process. Parallel callers must receive distinct values.
boot_root="$TMP/runtime"
first="$(POWER_IOT_LOCAL_RUNTIME_STATE_ROOT="$boot_root" bash -c 'source "$1"; allocate_simulator_boot' _ "$OP")"
second="$(POWER_IOT_LOCAL_RUNTIME_STATE_ROOT="$boot_root" bash -c 'source "$1"; allocate_simulator_boot' _ "$OP")"
[[ "$first" == 1 && "$second" == 2 ]] || fail 'monotonic simulator boot allocation'
pass 'simulator boot counter is monotonic'
mapfile -t boots < <(seq 1 12 | xargs -P 12 -I{} env POWER_IOT_LOCAL_RUNTIME_STATE_ROOT="$boot_root" bash -c 'source "$1"; allocate_simulator_boot' _ "$OP" | sort -n)
[[ "${#boots[@]}" == 12 ]] || fail 'concurrent boot allocation count'
[[ "$(printf '%s\n' "${boots[@]}" | uniq | wc -l)" == 12 ]] || fail 'concurrent boot allocation reused a value'
[[ "$(stat -c %a "$boot_root/simulator-boot-counter")" == 600 ]] || fail 'boot counter permissions'
pass 'concurrent simulator boot allocation is unique and private'

# New-start health accepts stored ACK only; duplicate ACK is diagnostic but
# insufficient for a newly allocated boot.
for ack in duplicate stored; do
  if [[ "$ack" == stored ]]; then expected=0; else expected=1; fi
  if SIM_ACK="$ack" bash -c '
    source "$1"
    tmux() { [[ "$1" == capture-pane ]] && printf "ACK %s mac=AABBCCDDEEFF boot=7 seq=0\n" "$SIM_ACK"; }
    simulator_stored_ack_for_boot 7
  ' _ "$OP" >/dev/null 2>&1; then result=0; else result=1; fi
  [[ "$result" == "$expected" ]] || fail "new simulator ACK $ack classification"
done
pass 'new simulator requires stored application ACK'

# Exercise the complete new-simulator startup health seam: duplicate-only
# output fails, while a stored ACK for the allocated boot succeeds.
for ack in duplicate stored; do
  if SIM_ACK="$ack" bash -c '
    source "$1"
    start_core() { :; }; require_command() { :; }; require_start_config() { :; }
    refuse_unowned_session() { :; }; owned_session() { return 1; }
    allocate_simulator_boot() { printf "7\\n"; }; update_state() { :; }
    tmux() { if [[ "$1" == capture-pane ]]; then printf "ACK %s mac=AABBCCDDEEFF boot=7 seq=0\\n" "$SIM_ACK"; fi; }
    wait_for() { eval "$2"; }
    start_telemetry
  ' _ "$OP" >/dev/null 2>&1; then result=0; else result=1; fi
  if [[ "$ack" == stored ]]; then expected=0; else expected=1; fi
  [[ "$result" == "$expected" ]] || fail "new simulator startup ACK $ack acceptance"
done
pass 'new simulator startup requires stored ACK'
grep -Fq -- '--boot-counter' "$OP" && grep -Fq -- '--start-seq 0' "$OP" || fail 'simulator boot/sequence flags'
pass 'simulator uses allocated boot counter and start-seq 0'

# Required guards and lifecycle intent are static and do not require stopping
# the user's currently running services.
for forbidden in 'docker rm' 'docker volume rm' 'docker network rm' 'docker system prune' 'docker compose down' 'down -v' 'migration-force'; do
  ! grep -Fq "$forbidden" "$OP" || fail "forbidden destructive operation absent: $forbidden"
done
pass 'no destructive database/container command'
grep -Fq "Backend: already healthy (no duplicate)" "$OP" || fail 'idempotent start guard'
grep -Fq 'canonical_ui_db' "$OP" || fail 'canonical UI DB guard'
grep -Fq 'LEGACY_DB_CONTAINER' "$OP" || fail 'legacy DB is named preserve-only'
pass 'idempotent and canonical-only guards'
grep -Fq "'ACK stored'" "$OP" && grep -Fq "'ACK duplicate'" "$OP" || fail 'application ACK health guard'
pass 'simulator health requires application ACK'

# Missing protected configuration is actionable and does not start anything.
mkdir -p "$TMP/empty"
if POWER_IOT_LOCAL_CONFIG_ROOT="$TMP/empty" POWER_IOT_LOCAL_SECRET_ROOT="$TMP/empty" "$OP" start core >"$TMP/missing.out" 2>&1; then fail 'missing configuration rejected'; fi
grep -Fq 'local runtime configuration is incomplete' "$TMP/missing.out" || fail 'missing configuration diagnostic'
pass 'missing config gives actionable error'

# The Windows file is only argument forwarding and optional distro selection.
grep -Fq '%*' "$WRAPPER" || fail 'Windows wrapper forwards arguments'
grep -Fq 'POWER_IOT_WSL_DISTRO' "$WRAPPER" || fail 'Windows wrapper supports optional distro'
! grep -Eq 'docker|55435|8883|8080' "$WRAPPER" || fail 'Windows wrapper contains runtime logic'
pass 'Windows wrapper forwards arguments'

# Stop-runtime's source block contains no database target and explicitly
# preserves the UI database policy.
stop_runtime_block="$(awk '/^stop_runtime\(\)/,/^restart_backend\(\)/' "$OP")"
! grep -Fq 'UI_DB_CONTAINER' <<<"$stop_runtime_block" || fail 'stop runtime targets UI DB'
! grep -Fq 'LEGACY_DB_CONTAINER' <<<"$stop_runtime_block" || fail 'stop runtime targets legacy DB'
grep -Fq 'preserves all DBs' <<<"$stop_runtime_block" || fail 'stop runtime preservation note'
pass 'stop runtime preserves UI and legacy databases'

printf 'Operator shell tests passed\n'
