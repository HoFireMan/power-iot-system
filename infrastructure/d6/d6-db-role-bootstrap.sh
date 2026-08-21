#!/bin/sh
set -eu

# Target-bound DB-VM role bootstrap. The only privileged credential accepted by
# this one-time preflight is the host-managed bootstrap file; all D6 runtime,
# migration, control, and legacy credentials are separate restricted files.
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
compose_file=${D6_DB_COMPOSE_FILE:?set D6 DB Compose file}
env_file=${D6_DB_ENV_FILE:?set D6 DB Compose env file}
project=${D6_DB_PROJECT:?set D6 DB Compose project}
bootstrap_role=${D6_DB_BOOTSTRAP_ROLE:?set pre-existing DB bootstrap role}
bootstrap_password_file=${D6_DB_BOOTSTRAP_PASSWORD_FILE:?set restricted bootstrap password file}
role_password_dir=${D6_DB_ROLE_PASSWORD_DIR:?set restricted D6 role password directory}
role_identity=${D6_DB_LOCAL_ROLE_IDENTITY_FILE:?set DB-VM role identity file}
target=${D6_DB_TARGET:-tcrfid01}
app_database=${D6_APPLICATION_DB_NAME:-power_iot}

fail() { echo "$*" >&2; exit 2; }
read_secret() {
  path=$1
  [ -r "$path" ] || fail "required restricted credential is unavailable"
  value=$(cat -- "$path")
  [ -n "$value" ] || fail "required restricted credential is empty"
  printf '%s' "$value"
}

[ -r "$role_identity" ] || fail 'DB VM role identity is unavailable'
[ "$(cat -- "$role_identity")" = "target=$target
role=power-iot-db" ] || fail 'DB VM role identity mismatch'
if [ "${D6_DB_CONTROL_PRODUCTION:-NO}" = YES ]; then
  for directory in / /opt /opt/poweriot; do
    [ -d "$directory" ] || fail "required security directory is missing: $directory"
    [ "$(stat -c '%u' "$directory")" = 0 ] || fail "security directory must be root-owned: $directory"
    permissions=$(stat -c '%a' "$directory")
    groupworld=${permissions#?}
    case "$groupworld" in *[2367]*) fail "security directory is group/world writable: $directory";; esac
  done
  [ "$(stat -c '%u' "$role_identity")" = 0 ] || fail 'DB VM role identity must be root-owned'
  permissions=$(stat -c '%a' "$role_identity")
  groupworld=${permissions#?}
  case "$groupworld" in *[2367]*) fail 'DB VM role identity must not be group/world writable';; esac
fi

bootstrap_password=$(read_secret "$bootstrap_password_file")
poweriot_password=$(read_secret "$role_password_dir/poweriot")
poweriot_runtime_password=$(read_secret "$role_password_dir/poweriot_runtime")
d6_migrator_password=$(read_secret "$role_password_dir/d6_migrator")
d6_db_admin_password=$(read_secret "$role_password_dir/d6_db_admin")

case "$bootstrap_role" in poweriot|poweriot_runtime|d6_migrator|d6_db_admin) fail 'bootstrap role must be separate from D6 roles';; esac

db() { docker compose -p "$project" --env-file "$env_file" -f "$compose_file" "$@"; }
psql_bootstrap() {
  db exec -T application-db env PGPASSWORD="$bootstrap_password" \
    psql -X -v ON_ERROR_STOP=1 -U "$bootstrap_role" -d "$app_database" "$@"
}
psql_role() {
  role=$1
  password=$2
  shift 2
  db exec -T application-db env PGPASSWORD="$password" \
    psql -X -v ON_ERROR_STOP=1 -U "$role" -d "$app_database" "$@"
}

# A bootstrap credential is accepted only as a pre-existing DB superuser. It
# is never installed into the runtime package or used by the application.
bootstrap_state=$(psql_bootstrap -Atqc "SELECT rolsuper::text || ':' || rolcanlogin::text FROM pg_roles WHERE rolname='$bootstrap_role'")
[ "$bootstrap_state" = true:true ] || fail 'DB bootstrap role is unavailable or not a login superuser'

missing_roles=$(psql_bootstrap -Atqc "SELECT COALESCE(string_agg(role_name, ' ' ORDER BY role_name), '') FROM unnest(ARRAY['poweriot','poweriot_runtime','d6_migrator','d6_db_admin']) AS wanted(role_name) LEFT JOIN pg_roles AS r ON r.rolname=wanted.role_name WHERE r.rolname IS NULL")

# Create/validate the exact role contract without changing conflicting roles.
# The SQL is streamed into the DB container; the host path is never assumed to
# exist inside the container and the SQL/password values are not logged.
cat "$script_dir/d6-db-role-bootstrap.sql" | \
  db exec -T application-db env PGPASSWORD="$bootstrap_password" \
    psql -X -v ON_ERROR_STOP=1 -U "$bootstrap_role" -d "$app_database" \
      -v app_database="$app_database" >/dev/null

set_role_password() {
  role=$1
  password=$2
  {
    printf '%s\n' "\\password $role"
    printf '%s\n%s\n' "$password" "$password"
  } | db exec -T application-db env PGPASSWORD="$bootstrap_password" \
    psql -X -q -U "$bootstrap_role" -d "$app_database" >/dev/null
}
for role in $missing_roles; do
  case "$role" in
    poweriot) set_role_password "$role" "$poweriot_password" ;;
    poweriot_runtime) set_role_password "$role" "$poweriot_runtime_password" ;;
    d6_migrator) set_role_password "$role" "$d6_migrator_password" ;;
    d6_db_admin) set_role_password "$role" "$d6_db_admin_password" ;;
    '') ;;
    *) fail 'unexpected missing D6 role result' ;;
  esac
done

# A newly initialized disposable database is owned by its bootstrap role. It
# is safe to hand ownership to poweriot only while the database is empty. An
# existing populated database must already have the accepted owner or fails
# closed rather than rewriting unknown production ownership.
database_owner=$(psql_bootstrap -Atqc "SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname=current_database()")
non_poweriot_objects=$(psql_bootstrap -Atqc "SELECT count(*) FROM pg_class AS c JOIN pg_namespace AS n ON n.oid=c.relnamespace JOIN pg_roles AS r ON r.oid=c.relowner WHERE n.nspname !~ '^pg_' AND n.nspname !~ '^_' AND n.nspname <> 'information_schema' AND c.relkind IN ('r','p','S','v','m','f') AND r.rolname <> 'poweriot' AND NOT EXISTS (SELECT 1 FROM pg_depend AS d WHERE d.classid='pg_class'::regclass AND d.objid=c.oid AND d.deptype='e')")
if [ "$database_owner" != poweriot ]; then
  [ "$non_poweriot_objects" = 0 ] || fail 'application database ownership conflicts with the accepted poweriot owner'
  psql_bootstrap -c "ALTER DATABASE \"$app_database\" OWNER TO poweriot" >/dev/null
fi
schema_owner=$(psql_bootstrap -Atqc "SELECT r.rolname FROM pg_namespace AS n JOIN pg_roles AS r ON r.oid=n.nspowner WHERE n.nspname='public'")
if [ "$schema_owner" != poweriot ]; then
  [ "$non_poweriot_objects" = 0 ] || fail 'public schema ownership conflicts with the accepted poweriot owner'
  psql_bootstrap -c 'ALTER SCHEMA public OWNER TO poweriot' >/dev/null
fi

# Verify every supplied credential through its intended identity. Passwords are
# passed only through PGPASSWORD and are never emitted.
[ "$(psql_role poweriot "$poweriot_password" -Atqc 'SELECT current_user')" = poweriot ] || fail 'poweriot credential verification failed'
[ "$(psql_role poweriot_runtime "$poweriot_runtime_password" -Atqc 'SELECT current_user')" = poweriot_runtime ] || fail 'runtime credential verification failed'
[ "$(psql_role d6_migrator "$d6_migrator_password" -Atqc 'SELECT current_user')" = d6_migrator ] || fail 'migration credential verification failed'
[ "$(psql_role d6_db_admin "$d6_db_admin_password" -Atqc 'SELECT current_user')" = d6_db_admin ] || fail 'DB-control credential verification failed'

# Re-read the security-critical role attributes after all idempotent grants.
roles=$(psql_bootstrap -Atqc "SELECT rolname || ':' || rolcanlogin::text || ':' || rolsuper::text || ':' || rolcreaterole::text || ':' || rolcreatedb::text || ':' || rolinherit::text || ':' || rolreplication::text || ':' || rolbypassrls::text FROM pg_roles WHERE rolname IN ('poweriot','poweriot_runtime','d6_migrator','d6_db_admin') ORDER BY rolname")
for expected in \
  'd6_db_admin:true:false:true:false:true:false:false' \
  'd6_migrator:true:false:false:false:true:false:false' \
  'poweriot:true:false:false:false:true:false:false' \
  'poweriot_runtime:true:false:false:false:true:false:false'; do
  echo "$roles" | grep -Fqx "$expected" || fail 'D6 role attribute verification failed'
done

# Confirm the runtime role is not an owner/admin membership and the migration
# role has only the intended application-owner membership.
runtime_memberships=$(psql_bootstrap -Atqc "SELECT count(*) FROM pg_auth_members AS m JOIN pg_roles AS r ON r.oid=m.member JOIN pg_roles AS g ON g.oid=m.roleid WHERE r.rolname='poweriot_runtime'")
[ "$runtime_memberships" = 0 ] || fail 'runtime role has unexpected role membership'
migrator_memberships=$(psql_bootstrap -Atqc "SELECT string_agg(g.rolname, ',' ORDER BY g.rolname) FROM pg_auth_members AS m JOIN pg_roles AS r ON r.oid=m.member JOIN pg_roles AS g ON g.oid=m.roleid WHERE r.rolname='d6_migrator'")
[ "$migrator_memberships" = poweriot ] || fail 'migration role has unexpected role membership'
control_memberships=$(psql_bootstrap -Atqc "SELECT string_agg(g.rolname, ',' ORDER BY g.rolname) FROM pg_auth_members AS m JOIN pg_roles AS r ON r.oid=m.member JOIN pg_roles AS g ON g.oid=m.roleid WHERE r.rolname='d6_db_admin'")
[ "$control_memberships" = pg_read_all_data,pg_read_all_stats,pg_signal_backend ] || fail 'DB-control role has unexpected role membership'

printf '%s\n' \
  'PRODUCTION_ROLE_PROVISIONING=PASS' \
  'ROLE_PROVISIONING_IDEMPOTENT=PASS' \
  'ROLE_CONFLICT_FAIL_CLOSED=PASS' \
  'BACKEND_RUNTIME_ROLE=poweriot_runtime' \
  'MIGRATION_ROLE=d6_migrator' \
  'DB_CONTROL_ROLE=d6_db_admin' \
  'LEGACY_DIRECT_SQL_PRE_CUTOVER=AVAILABLE_UNTIL_DRAIN'
