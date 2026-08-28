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
cp "$ROOT/README.md" "$TMP/state.md"
before="$(stat -c %Y "$state")"
POWER_IOT_LOCAL_STATE_FILE="$state" "$OP" status >/dev/null || fail 'status is readable'
after="$(stat -c %Y "$state")"
[[ "$before" == "$after" ]] || fail 'status is read-only'
pass 'status is read-only'

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
