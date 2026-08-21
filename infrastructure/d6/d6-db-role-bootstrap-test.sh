#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
artifact="$root/d6-db-role-bootstrap.sh"
sql="$root/d6-db-role-bootstrap.sql"
rehearsal="$root/d6-rehearsal.sh"
control="$root/d6-db-local-control.sh"
operator="$root/d6-operator.sh"

[ -x "$artifact" ] || { echo 'role bootstrap artifact is not executable' >&2; exit 1; }
[ -r "$sql" ] || { echo 'role bootstrap SQL is missing' >&2; exit 1; }
for role in poweriot poweriot_runtime d6_migrator d6_db_admin; do
  grep -Fq "'$role'" "$sql" || { echo "role missing from SQL contract: $role" >&2; exit 1; }
done
grep -Fq 'required role % has conflicting or unsafe attributes' "$sql"
grep -Fq 'ROLE_BOOTSTRAP_MISSING_CREDENTIAL_FAIL_CLOSED=PASS' "$rehearsal"
grep -Fq 'ROLE_CONFLICT_FAIL_CLOSED=PASS' "$rehearsal"
grep -Fq 'ROLE_BOOTSTRAP_V5_RERUN=PASS' "$rehearsal"
grep -Fq 'WRONG_BACKEND_DSN_ROLE_FAIL_CLOSED=PASS' "$rehearsal"
grep -Fq 'WRONG_MIGRATION_DSN_ROLE_FAIL_CLOSED=PASS' "$rehearsal"
if grep -Fq 'CREATE ROLE' "$rehearsal"; then
  echo 'rehearsal contains inline role creation' >&2
  exit 1
fi
if grep -Fq 'ALTER ROLE $app_role LOGIN' "$control"; then
  echo 'post-cutover direct login reopening remains in DB control' >&2
  exit 1
fi
grep -Fq 'poweriot_runtime' "$operator"
grep -Fq 'd6_migrator' "$operator"

echo ROLE_BOOTSTRAP_SOURCE_TEST=PASS
