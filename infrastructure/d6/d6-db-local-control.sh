#!/bin/sh
set -eu

# DB-VM-local control seam. It is intentionally narrow: the DB VM owns
# PostgreSQL inspection and the application-login policy; it never controls App
# VM containers or accepts credentials on its command line.
role_identity=${D6_DB_LOCAL_ROLE_IDENTITY_FILE:?set DB-VM role identity file}
compose_file=${D6_DB_COMPOSE_FILE:?set DB Compose file}
env_file=${D6_DB_ENV_FILE:?set DB Compose env file}
project=${D6_DB_PROJECT:?set DB Compose project}
admin_password_file=${D6_DB_ADMIN_PASSWORD_FILE:?set restricted DB admin password file}
provider_admin_password_file=${D6_PROVIDER_ADMIN_PASSWORD_FILE:?set restricted provider DB admin password file}
runtime_password_file=${D6_RUNTIME_DB_PASSWORD_FILE:?set restricted runtime DB password file}
migration_password_file=${D6_MIGRATION_DB_PASSWORD_FILE:?set restricted migration DB password file}
app_database=${D6_APPLICATION_DB_NAME:-power_iot}
provider_database=${D6_PROVIDER_DB_NAME:-d1l_provider}
app_role=${D6_APPLICATION_DB_ROLE:-poweriot}
runtime_role=${D6_RUNTIME_DB_ROLE:-poweriot_runtime}
migration_role=${D6_MIGRATION_DB_ROLE:-d6_migrator}
control_role=d6_db_admin
target=${D6_DB_TARGET:-tcrfid01}

fail() { echo "$*" >&2; exit 2; }
if [ "${D6_DB_CONTROL_PRODUCTION:-NO}" = YES ]; then
  for directory in / /opt /opt/poweriot; do
    [ -d "$directory" ] || fail "required security directory is missing: $directory"
    [ "$(stat -c '%u' "$directory")" = 0 ] || fail "security directory must be root-owned: $directory"
    perm=$(stat -c '%a' "$directory"); groupworld=${perm#?}
    case "$groupworld" in *[2367]*) fail "security directory is group/world writable: $directory";; esac
  done
fi
[ -r "$role_identity" ] || fail 'DB VM role identity is unavailable'
identity=$(cat "$role_identity")
[ "$identity" = "target=$target
role=power-iot-db" ] || fail 'DB VM role identity mismatch'
uid=$(stat -c '%u' "$role_identity")
perm=$(stat -c '%a' "$role_identity")
if [ "${D6_DB_CONTROL_PRODUCTION:-NO}" = YES ] && [ "$uid" != 0 ]; then
  fail 'DB VM role identity must be root-owned'
fi
groupworld=${perm#?}
case "$groupworld" in *[2367]*) fail 'DB VM role identity must not be group/world writable';; esac

[ -r "$admin_password_file" ] || fail 'DB admin password file is unavailable'
[ -r "$provider_admin_password_file" ] || fail 'provider DB admin password file is unavailable'
[ -r "$runtime_password_file" ] || fail 'runtime DB password file is unavailable'
[ -r "$migration_password_file" ] || fail 'migration DB password file is unavailable'
admin_password=$(cat "$admin_password_file")
provider_admin_password=$(cat "$provider_admin_password_file")
runtime_password=$(cat "$runtime_password_file")
migration_password=$(cat "$migration_password_file")
[ -n "$admin_password" ] || fail 'DB admin password is empty'
[ -n "$provider_admin_password" ] || fail 'provider DB admin password is empty'
[ -n "$runtime_password" ] || fail 'runtime DB password is empty'
[ -n "$migration_password" ] || fail 'migration DB password is empty'
db() { docker compose -p "$project" --env-file "$env_file" -f "$compose_file" "$@"; }
app_psql() {
  db exec -T application-db env PGPASSWORD="$admin_password" psql -U "$control_role" -d "$app_database" -Atqc "$1"
}
runtime_psql() {
  db exec -T application-db env PGPASSWORD="$runtime_password" psql -U "$runtime_role" -d "$app_database" -Atqc "$1"
}
migration_psql() {
  db exec -T application-db env PGPASSWORD="$migration_password" psql -U "$migration_role" -d "$app_database" -Atqc "$1"
}
provider_psql() {
  db exec -T d1l-provider-db env PGPASSWORD="$provider_admin_password" psql -U d1l -d "$provider_database" -Atqc "$1"
}
service_id() { db ps -q "$1"; }
require_services() {
  for service in application-db d1l-provider-db; do
    id=$(service_id "$service")
    [ -n "$id" ] || fail "expected DB service is missing: $service"
    [ "$(docker inspect -f '{{.State.Running}}' "$id")" = true ] || fail "DB service is not running: $service"
  done
}
require_roles() {
  roles=$(app_psql "SELECT rolname || ':' || rolcanlogin::text || ':' || rolsuper::text || ':' || rolcreaterole::text || ':' || rolcreatedb::text || ':' || rolinherit::text || ':' || rolreplication::text || ':' || rolbypassrls::text FROM pg_roles WHERE rolname IN ('$app_role','$runtime_role','$migration_role','$control_role') ORDER BY rolname")
  echo "$roles" | grep -Eq "^$app_role:(true|false):false:false:false:true:false:false$" || fail 'legacy application role is missing or unsafe'
  for expected in \
    "$runtime_role:true:false:false:false:true:false:false" \
    "$migration_role:true:false:false:false:true:false:false" \
    "$control_role:true:false:true:false:true:false:false"; do
    echo "$roles" | grep -Fqx "$expected" || fail 'required D6 role is missing or unsafe'
  done
  runtime_memberships=$(app_psql "SELECT count(*) FROM pg_auth_members AS m JOIN pg_roles AS r ON r.oid=m.member WHERE r.rolname='$runtime_role'")
  [ "$runtime_memberships" = 0 ] || fail 'runtime DB role has unexpected membership'
  migration_memberships=$(app_psql "SELECT string_agg(g.rolname, ',' ORDER BY g.rolname) FROM pg_auth_members AS m JOIN pg_roles AS r ON r.oid=m.member JOIN pg_roles AS g ON g.oid=m.roleid WHERE r.rolname='$migration_role'")
  [ "$migration_memberships" = "$app_role" ] || fail 'migration DB role membership is unsafe'
}
require_direct_enabled() {
  [ "$(app_psql "SELECT rolcanlogin FROM pg_roles WHERE rolname='$app_role'")" = t ] || fail 'application direct-writer role is not available before drain'
}
require_direct_disabled() {
  [ "$(app_psql "SELECT rolcanlogin FROM pg_roles WHERE rolname='$app_role'")" = f ] || fail 'application direct-writer role remains enabled'
}
require_private_bind() {
  service=$1
  id=$(service_id "$service")
  bindings=$(docker inspect -f '{{json .HostConfig.PortBindings}}' "$id")
  case "$bindings" in
    *0.0.0.0*|*':::'*|*'"HostIp":"::"'*) fail "$service is publicly bound";;
  esac
}
require_database_identities() {
  [ "$(app_psql "SELECT current_database()")" = "$app_database" ] || fail 'application DB identity mismatch'
  [ "$(provider_psql "SELECT current_database()")" = "$provider_database" ] || fail 'provider DB identity mismatch'
}

case "${1:-}" in
  preflight)
    require_services; require_roles; require_direct_enabled; require_database_identities
    require_private_bind application-db; require_private_bind d1l-provider-db
    echo DB_VM_ROLE_PREFLIGHT=PASS
    ;;
  disable-writers)
    require_services; require_roles
    app_psql "ALTER ROLE $app_role NOLOGIN; SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename='$app_role' AND pid <> pg_backend_pid();" >/dev/null
    require_roles; require_direct_disabled
    echo DIRECT_WRITER_CONTROL_SPLIT_VM=PASS
    ;;
  sessions)
    count=$(app_psql "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND usename='$app_role' AND pid <> pg_backend_pid()")
    case "$count" in 0) echo 0;; *) echo "$count"; exit 1;; esac
    ;;
  no-public-db-port)
    require_private_bind application-db
    require_private_bind d1l-provider-db
    echo PUBLIC_DB_EXPOSURE=NO
    ;;
  inspect)
    require_services; require_roles; require_database_identities; require_direct_disabled
    require_private_bind application-db; require_private_bind d1l-provider-db
    echo DB_VM_CONTROL=PASS
    ;;
  precutover-policy)
    require_roles; require_direct_disabled
    echo PRE_CUTOVER_DB_POLICY=PASS
    echo PRE_CUTOVER_DIRECT_WRITER_DENIED=PASS
    ;;
  postcutover-policy)
    require_services; require_roles; require_direct_disabled
    [ "$(runtime_psql 'SELECT current_user')" = "$runtime_role" ] || fail 'runtime role cannot access application DB after cutover'
    [ "$(migration_psql 'SELECT current_user')" = "$migration_role" ] || fail 'migration role cannot access application DB after cutover'
    echo LEGACY_DIRECT_SQL_POST_CUTOVER=BLOCKED
    echo POST_CUTOVER_DB_POLICY=PASS
    ;;
  *) fail 'unsupported DB VM control action';;
esac
