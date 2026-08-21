\set ON_ERROR_STOP on

-- Canonical D6 application-database role contract. Password values are supplied
-- Passwords are installed through psql's password prompt by the shell
-- wrapper, never through SQL text, command arguments, or command output.
DO $$
DECLARE
  expected record;
  actual record;
BEGIN
  FOR expected IN
    SELECT * FROM (VALUES
      ('poweriot',         true, false, false, false, true),
      ('poweriot_runtime', true, false, false, false, true),
      ('d6_migrator',      true, false, false, false, true),
      ('d6_db_admin',      true, false, true,  false, true)
    ) AS v(rolname, rolcanlogin, rolsuper, rolcreaterole, rolcreatedb, rolinherit)
  LOOP
    SELECT r.rolcanlogin, r.rolsuper, r.rolcreaterole, r.rolcreatedb, r.rolinherit,
           r.rolreplication, r.rolbypassrls
      INTO actual
      FROM pg_roles AS r
     WHERE r.rolname = expected.rolname;

    IF NOT FOUND THEN
      IF expected.rolname = 'poweriot' THEN
        EXECUTE 'CREATE ROLE poweriot LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE INHERIT NOREPLICATION NOBYPASSRLS';
      ELSIF expected.rolname = 'poweriot_runtime' THEN
        EXECUTE 'CREATE ROLE poweriot_runtime LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE INHERIT NOREPLICATION NOBYPASSRLS';
      ELSIF expected.rolname = 'd6_migrator' THEN
        EXECUTE 'CREATE ROLE d6_migrator LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE INHERIT NOREPLICATION NOBYPASSRLS';
      ELSE
        EXECUTE 'CREATE ROLE d6_db_admin LOGIN NOSUPERUSER NOCREATEDB CREATEROLE INHERIT NOREPLICATION NOBYPASSRLS';
      END IF;
    ELSE
      IF actual.rolcanlogin IS DISTINCT FROM expected.rolcanlogin
         OR actual.rolsuper IS DISTINCT FROM expected.rolsuper
         OR actual.rolcreaterole IS DISTINCT FROM expected.rolcreaterole
         OR actual.rolcreatedb IS DISTINCT FROM expected.rolcreatedb
         OR actual.rolinherit IS DISTINCT FROM expected.rolinherit
         OR actual.rolreplication
         OR actual.rolbypassrls THEN
        RAISE EXCEPTION 'required role % has conflicting or unsafe attributes', expected.rolname;
      END IF;
    END IF;
  END LOOP;
END $$;

-- The migration role may act through the non-login application owner role for
-- the existing schema and the protected D5 DDL, but the runtime role never
-- receives that owner membership.
GRANT poweriot TO d6_migrator;
GRANT pg_signal_backend, pg_read_all_stats, pg_read_all_data TO d6_db_admin;

GRANT CONNECT ON DATABASE :"app_database" TO poweriot_runtime, d6_migrator, d6_db_admin;
GRANT USAGE ON SCHEMA public TO poweriot_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO poweriot_runtime;
GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA public TO poweriot_runtime;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO poweriot_runtime;

ALTER DEFAULT PRIVILEGES FOR ROLE poweriot IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO poweriot_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE poweriot IN SCHEMA public
  GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO poweriot_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE poweriot IN SCHEMA public
  GRANT EXECUTE ON FUNCTIONS TO poweriot_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE d6_migrator IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO poweriot_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE d6_migrator IN SCHEMA public
  GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO poweriot_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE d6_migrator IN SCHEMA public
  GRANT EXECUTE ON FUNCTIONS TO poweriot_runtime;

-- The DB-control role is intentionally not a superuser. CREATEROLE plus the
-- narrowly required built-in memberships permits ALTER ROLE for the legacy
-- login and session inspection/termination without granting application data
-- ownership to the control credential.
