#!/bin/sh
set -eu

# D6 operator workflow. The same target-bound drain, protected migration,
# pre-cutover, smoke, re-entry, gate, and explicit post-cutover path is used for
# rehearsal and the later production batch. Hooks are host-managed validated
# checks; this script never invents migration authority or runs production by
# default.
mode=${D6_OPERATOR_MODE:-rehearsal}
target=${D6_OPERATOR_TARGET:-rehearsal}
project=${D6_OPERATOR_PROJECT:?set isolated compose project}
app_compose=${D6_APP_COMPOSE_FILE:?set App Compose file}
base_app_env=${D6_APP_ENV_FILE:?set App Compose env file}
database_url=${D6_APPLICATION_DATABASE_URL:?set application database URL}
migration_database_url=${D6_MIGRATION_DATABASE_URL:?set protected migration database URL}
provider_database_url=${D6_PROVIDER_DATABASE_URL:?set provider database URL}
migration_database_password=${D6_MIGRATION_DATABASE_PASSWORD:?set restricted migration database password}
drain_command=${D6_DRAIN_COMMAND:?set validated target-bound d6-drain executable}
db_control_command=${D6_DB_VM_CONTROL:?set validated DB-VM control helper}
app_vm_control=${D6_APP_VM_CONTROL:?set validated App-VM control identity}
target_identity_file=${D6_TARGET_IDENTITY_FILE:?set host-managed target identity file}
app_role_identity_file=${D6_APP_VM_ROLE_IDENTITY_FILE:?set host-managed App-VM role identity file}
private_key_file=${D6_DRAIN_PRIVATE_KEY:?set host-managed D6 drain private key}
container_identity_file=${D6_CONTAINER_TARGET_IDENTITY_FILE:?set container-visible target identity file}
public_key_file=${D6_ADMISSION_PUBLIC_KEY:?set container-visible D6 admission public key}
open_proxy_config=${D6_POST_CUTOVER_PROXY_CONFIG:?set validated post-cutover nginx config}
[ -r "$base_app_env" ] || { echo 'App Compose environment file is unavailable' >&2; exit 2; }

validate_dsn_role() {
  dsn=$1
  expected_role=$2
  case "$dsn" in
    postgres://$expected_role@*|postgresql://$expected_role@*) ;;
    *) echo "DSN role preflight failed for expected role $expected_role" >&2; exit 2;;
  esac
}
normalize_dsn_identity() {
  printf '%s' "$1" | sed -E 's#^[a-z]+://[^@]+@##; s/[?].*$//'
}
read_app_env_value() {
  awk -F= -v key="$1" '$1 == key { sub(/^[^=]*=/, ""); print; exit }' "$base_app_env"
}
app_compose_database_url=$(read_app_env_value APPLICATION_DATABASE_URL)
app_compose_provider_url=$(read_app_env_value D1L_PROVIDER_DATABASE_URL)
[ -n "$app_compose_database_url" ] || { echo 'App Compose runtime database URL is missing' >&2; exit 2; }
[ -n "$app_compose_provider_url" ] || { echo 'App Compose provider database URL is missing' >&2; exit 2; }
validate_dsn_role "$database_url" poweriot_runtime
validate_dsn_role "$app_compose_database_url" poweriot_runtime
validate_dsn_role "$migration_database_url" d6_migrator
validate_dsn_role "$provider_database_url" d1l
validate_dsn_role "$app_compose_provider_url" d1l
[ "$(normalize_dsn_identity "$database_url")" = "$(normalize_dsn_identity "$app_compose_database_url")" ] || { echo 'operator and App Compose application DSN identities differ' >&2; exit 2; }
[ "$(normalize_dsn_identity "$provider_database_url")" = "$(normalize_dsn_identity "$app_compose_provider_url")" ] || { echo 'operator and App Compose provider DSN identities differ' >&2; exit 2; }
[ "$(normalize_dsn_identity "$database_url")" = "$(normalize_dsn_identity "$migration_database_url")" ] || { echo 'application and migration DSN identities differ' >&2; exit 2; }
[ "$(normalize_dsn_identity "$database_url")" != "$(normalize_dsn_identity "$provider_database_url")" ] || { echo 'application and provider DSN identities are merged' >&2; exit 2; }
echo BACKEND_RUNTIME_DSN_ROLE=poweriot_runtime
echo MIGRATION_DSN_ROLE=d6_migrator
echo PROVIDER_DB_DSN_ROLE=d1l

case "$mode" in
  rehearsal)
    [ "${D6_REHEARSAL:-}" = 1 ] || { echo 'D6_REHEARSAL=1 is required' >&2; exit 2; }
    [ "$target" = rehearsal ] || { echo 'rehearsal target must be rehearsal' >&2; exit 2; }
    migration_mode=-rehearsal
    migration_target=rehearsal
    ;;
  production)
    [ "$target" = tcrfid01 ] || { echo 'production target must be tcrfid01' >&2; exit 2; }
    [ "${A3_D6_PRODUCTION_EXECUTION_AUTHORIZED:-NO}" = YES ] || {
      echo 'A3_D6_PRODUCTION_EXECUTION_AUTHORIZED=YES is required' >&2
      exit 2
    }
    case "$db_control_command" in
      /opt/poweriot/*) ;;
      *) echo 'production DB_VM_CONTROL must be under /opt/poweriot' >&2; exit 2 ;;
    esac
    export D6_DB_CONTROL_PRODUCTION=YES
    migration_mode=-production
    migration_target=tcrfid01
    ;;
  *) echo 'D6_OPERATOR_MODE must be rehearsal or production' >&2; exit 2 ;;
esac

# All post-migration checks are explicit and must be supplied before drain.
for hook_name in D6_READINESS_COMMAND D6_CONTROLLED_DB_SMOKE_COMMAND D6_MQTTS_SMOKE_COMMAND D6_RESTART_REENTRY_COMMAND D6_FINAL_GATES_COMMAND D6_POST_CUTOVER_VERIFY_COMMAND; do
  eval "hook_value=\${$hook_name:-}"
  [ -x "$hook_value" ] || { echo "$hook_name must name an executable validated hook" >&2; exit 2; }
done
[ -f "$open_proxy_config" ] || { echo 'post-cutover proxy config is missing' >&2; exit 2; }
[ -x "$db_control_command" ] || { echo 'DB_VM_CONTROL must be executable' >&2; exit 2; }
[ -n "$app_vm_control" ] || { echo 'APP_VM_CONTROL must be set' >&2; exit 2; }
echo "APP_VM_CONTROL=$app_vm_control"
echo "DB_VM_CONTROL=$db_control_command"

app() { "$app_vm_control" compose -p "$project" --env-file "$app_env" -f "$app_compose" "$@"; }

# Runtime mode is restart/recreate based, fail-closed, and externally visible.
runtime_env=$(mktemp)
admission_file=$(mktemp)
migration_log=$(mktemp)
cleanup() { rm -f "$runtime_env" "$admission_file" "$migration_log"; }
trap cleanup EXIT
app_env="$base_app_env"
write_runtime_env() {
  mode_value=$1
  smoke_value=$2
  proxy_value=$3
  awk -v mode="$mode_value" -v smoke="$smoke_value" -v proxy="$proxy_value" '
    !/^D6_RUNTIME_MODE=/ && !/^D6_BOUNDED_SMOKE=/ && !/^POWERIOT_REVERSE_PROXY_CONFIG=/ { print }
    END { print "D6_RUNTIME_MODE=" mode; print "D6_BOUNDED_SMOKE=" smoke; print "POWERIOT_REVERSE_PROXY_CONFIG=" proxy }
  ' "$base_app_env" > "$runtime_env"
  app_env="$runtime_env"
  export D6_ACTIVE_APP_ENV="$app_env"
}
run_hook() { hook=$1; "$hook"; }

# Drain and protected D5-owned migration. The admission is always an inherited
# pipe authored by the actual d6-drain process, including rehearsal.
set +e
"$drain_command" -mode "$mode" -target "$target" -project "$project" \
  -app-compose "$app_compose" -app-env "$base_app_env" -app-vm-control-command "$app_vm_control" \
  -app-vm-role-identity-file "$app_role_identity_file" -db-control-command "$db_control_command" \
  -target-identity-file "$target_identity_file" -admission-private-key "$private_key_file" >"$admission_file"
drain_rc=$?
if [ "$drain_rc" -eq 0 ]; then
  cat "$admission_file" | app run --no-deps --interactive -T -e PGPASSWORD="$migration_database_password" --rm backend /usr/local/bin/d6-migrate "$migration_mode" -target "$migration_target" -execute -database-url "$migration_database_url" -trusted-drain-admission-fd 0 -admission-public-key "$public_key_file" -target-identity-file "$container_identity_file" >"$migration_log" 2>&1
  migration_rc=$?
else
  migration_rc=1
fi
set -e
[ "$migration_rc" -eq 0 ] && [ "$drain_rc" -eq 0 ] || { cat "$migration_log" >&2; echo "protected migration/drain failed migration_rc=$migration_rc drain_rc=$drain_rc" >&2; exit 1; }
echo 'PROTECTED_D6_OPERATOR_ENTRY=PASS'

precutover_policy=$($db_control_command precutover-policy)
echo "$precutover_policy" | grep -q 'PRE_CUTOVER_DB_POLICY=PASS' || { echo 'PRE_CUTOVER DB policy was not established' >&2; exit 1; }
echo "$precutover_policy"
echo 'PRE_CUTOVER_DB_POLICY=PASS'

# The DB-VM pre-CUTOVER policy keeps the direct application role fenced while
# the dedicated runtime role permits approved backend readiness/smoke.
# Start clean V6 in PRE_CUTOVER: readiness can be available, while HTTP and
# general MQTT writers remain denied. The open proxy is never used here.
write_runtime_env PRE_CUTOVER 0 "$(grep '^POWERIOT_REVERSE_PROXY_CONFIG=' "$base_app_env" | cut -d= -f2-)"
app up -d mqtt backend reverse-proxy >/dev/null
echo 'PRE_CUTOVER_RUNTIME=PASS'
run_hook "$D6_READINESS_COMMAND"
run_hook "$D6_CONTROLLED_DB_SMOKE_COMMAND"

# Explicit bounded MQTTS smoke is a separate mode: only the approved telemetry
# topic is admitted; general MQTT ingestion remains blocked.
write_runtime_env PRE_CUTOVER 1 "$(grep '^POWERIOT_REVERSE_PROXY_CONFIG=' "$base_app_env" | cut -d= -f2-)"
app up -d --force-recreate backend >/dev/null
echo 'BOUNDED_MQTTS_SMOKE_MODE=PASS'
run_hook "$D6_MQTTS_SMOKE_COMMAND"
write_runtime_env PRE_CUTOVER 0 "$(grep '^POWERIOT_REVERSE_PROXY_CONFIG=' "$base_app_env" | cut -d= -f2-)"
app up -d --force-recreate backend >/dev/null
run_hook "$D6_RESTART_REENTRY_COMMAND"
run_hook "$D6_FINAL_GATES_COMMAND"

# Only after every prior hook passes is the explicit POST_CUTOVER switch made.
# It changes both the backend mode and the proxy artifact, then recreates the
# runtime deterministically. The final hook proves general HTTP/MQTT enable.
write_runtime_env POST_CUTOVER 0 "$open_proxy_config"
app up -d --force-recreate backend reverse-proxy >/dev/null
echo 'POST_CUTOVER_RUNTIME=PASS'
run_hook "$D6_POST_CUTOVER_VERIFY_COMMAND"
echo 'GENERAL_WRITE_REOPEN=PASS'
postcutover_policy=$($db_control_command postcutover-policy)
echo "$postcutover_policy" | grep -q 'POST_CUTOVER_DB_POLICY=PASS' || { echo 'POST_CUTOVER DB policy was not established' >&2; exit 1; }
echo "$postcutover_policy"
echo 'POST_CUTOVER_DB_POLICY=PASS'
