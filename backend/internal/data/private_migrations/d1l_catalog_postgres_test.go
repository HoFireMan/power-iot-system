package migrations

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"power-iot-backend/internal/testsupport"
)

type d1LMutation struct {
	name  string
	apply func(*sql.DB) ([]byte, error)
	state D1LCatalogState
}

func installD1LTestCatalog(t *testing.T) (*testsupport.Database, string, []byte) {
	t.Helper()
	db, err := testsupport.New(context.Background(), os.Getenv("TEST_DATABASE_URL"), Up)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	target := []byte(strings.Repeat("t", 32))
	conn, err := sql.Open("postgres", db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Exec(string(d1LInstallerBytes)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(D1LManifestInsertSQL, target, d1LInstallerDigestBytes(), uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	return db, db.DSN(), target
}

func recognizeD1LDatabase(t *testing.T, dsn string, target []byte) D1LCatalogObservation {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	obs, err := RecognizeD1LCatalog(context.Background(), db, target, d1LInstallerDigestBytes())
	if err != nil && obs.State == D1LUnreadable {
		t.Fatalf("recognizer unreadable: %v", err)
	}
	return obs
}

func TestD1LPhysicalCatalogPositive(t *testing.T) {
	_, dsn, target := installD1LTestCatalog(t)
	if got := recognizeD1LDatabase(t, dsn, target); got.State != D1LExactReady {
		t.Fatalf("known-good catalog state=%s detail=%s", got.State, got.Detail)
	}
}

func TestD1LPhysicalCatalogPositiveAndNegativeMatrix(t *testing.T) {
	target := []byte(strings.Repeat("t", 32))
	mutations := []d1LMutation{
		{name: "bytea_to_text", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("ALTER TABLE security_control.control_schema_migrations ALTER COLUMN target_fingerprint TYPE text USING substring(encode(target_fingerprint, 'hex'), 1, 32)"); err != nil {
				return nil, err
			}
			textTarget := []byte(strings.Repeat("74", 16))
			_, err := db.Exec("UPDATE security_control.control_schema_migrations SET target_fingerprint = $1", textTarget)
			return textTarget, err
		}},
		{name: "uuid_to_text", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("ALTER TABLE security_control.control_schema_migrations ALTER COLUMN install_id TYPE text USING install_id::text")
			return target, err
		}},
		{name: "bigint_to_integer", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("ALTER TABLE security_control.admission_leases ALTER COLUMN generation TYPE integer USING generation::integer")
			return target, err
		}},
		{name: "timestamptz_to_timestamp", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("ALTER TABLE security_control.control_schema_migrations ALTER COLUMN installed_at TYPE timestamp USING installed_at AT TIME ZONE 'UTC'")
			return target, err
		}},
		{name: "boolean_to_text", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("ALTER TABLE security_control.control_schema_migrations DROP CONSTRAINT control_schema_migrations_dirty_check"); err != nil {
				return nil, err
			}
			if _, err := db.Exec("ALTER TABLE security_control.control_schema_migrations ALTER COLUMN dirty TYPE text USING dirty::text"); err != nil {
				return nil, err
			}
			_, err := db.Exec("ALTER TABLE security_control.control_schema_migrations ADD CONSTRAINT control_schema_migrations_dirty_check CHECK (dirty = 'false'::text)")
			return target, err
		}},
		{name: "domain_lookalike", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("CREATE DOMAIN public.d1l_text_lookalike AS text"); err != nil {
				return nil, err
			}
			_, err := db.Exec("ALTER TABLE security_control.admission_boundaries ALTER COLUMN outcome_code TYPE public.d1l_text_lookalike USING outcome_code::text::public.d1l_text_lookalike")
			return target, err
		}},
		{name: "typmod_text", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("ALTER TABLE security_control.admission_boundaries ALTER COLUMN outcome_code TYPE varchar(32) USING outcome_code::varchar")
			return target, err
		}},
		{name: "wrong_nullability", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("ALTER TABLE security_control.admission_boundaries ALTER COLUMN outcome_code SET NOT NULL")
			return target, err
		}},
		{name: "wrong_default", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("ALTER TABLE security_control.admission_leases ALTER COLUMN generation DROP DEFAULT")
			return target, err
		}},
		{name: "extra_column", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("ALTER TABLE security_control.admission_leases ADD COLUMN extra_column text")
			return target, err
		}},
		{name: "missing_column", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("ALTER TABLE security_control.admission_boundaries DROP COLUMN outcome_code CASCADE")
			return target, err
		}},
		{name: "wrong_check", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("ALTER TABLE security_control.admission_leases DROP CONSTRAINT admission_leases_generation_check"); err != nil {
				return nil, err
			}
			_, err := db.Exec("ALTER TABLE security_control.admission_leases ADD CONSTRAINT admission_leases_generation_check CHECK (generation >= 0)")
			return target, err
		}},
		{name: "wrong_membership_lhs", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("ALTER TABLE security_control.admission_leases DROP CONSTRAINT admission_leases_status_check"); err != nil {
				return nil, err
			}
			_, err := db.Exec("ALTER TABLE security_control.admission_leases ADD CONSTRAINT admission_leases_status_check CHECK (terminal_code = ANY (ARRAY['ISSUED'::text, 'ACTIVE'::text, 'QUARANTINE_PENDING'::text, 'QUARANTINED'::text, 'CONSUMED'::text, 'EXPIRED'::text, 'REVOKED'::text]))")
			return target, err
		}},
		{name: "wrong_constraint_kind", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("ALTER TABLE security_control.control_schema_migrations DROP CONSTRAINT control_schema_migrations_pkey"); err != nil {
				return nil, err
			}
			_, err := db.Exec("ALTER TABLE security_control.control_schema_migrations ADD CONSTRAINT control_schema_migrations_pkey CHECK (control_version = 1)")
			return target, err
		}},
		{name: "removed_check", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("ALTER TABLE security_control.admission_leases DROP CONSTRAINT admission_leases_generation_check")
			return target, err
		}},
		{name: "wrong_fk_action", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("ALTER TABLE security_control.admission_boundaries DROP CONSTRAINT admission_boundaries_lease_fk"); err != nil {
				return nil, err
			}
			_, err := db.Exec("ALTER TABLE security_control.admission_boundaries ADD CONSTRAINT admission_boundaries_lease_fk FOREIGN KEY (lease_id, generation, attempt_id) REFERENCES security_control.admission_leases (lease_id, generation, attempt_id) ON DELETE CASCADE ON UPDATE NO ACTION")
			return target, err
		}},
		{name: "wrong_fk_columns", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("ALTER TABLE security_control.admission_boundaries DROP CONSTRAINT admission_boundaries_lease_fk"); err != nil {
				return nil, err
			}
			_, err := db.Exec("ALTER TABLE security_control.admission_boundaries ADD CONSTRAINT admission_boundaries_lease_fk FOREIGN KEY (lease_id, attempt_id, generation) REFERENCES security_control.admission_leases (lease_id, attempt_id, generation) ON DELETE RESTRICT ON UPDATE RESTRICT")
			return target, err
		}},
		{name: "wrong_fk_deferrability", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("ALTER TABLE security_control.admission_boundaries DROP CONSTRAINT admission_boundaries_lease_fk"); err != nil {
				return nil, err
			}
			_, err := db.Exec("ALTER TABLE security_control.admission_boundaries ADD CONSTRAINT admission_boundaries_lease_fk FOREIGN KEY (lease_id, generation, attempt_id) REFERENCES security_control.admission_leases (lease_id, generation, attempt_id) ON DELETE RESTRICT ON UPDATE RESTRICT DEFERRABLE")
			return target, err
		}},
		{name: "missing_index", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("DROP INDEX security_control.admission_boundaries_one_open_per_lease_idx")
			return target, err
		}},
		{name: "wrong_unique", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("ALTER TABLE security_control.admission_leases DROP CONSTRAINT admission_leases_operation_generation_key"); err != nil {
				return nil, err
			}
			_, err := db.Exec("CREATE INDEX admission_leases_operation_generation_key ON security_control.admission_leases USING btree (operation_id, generation)")
			return target, err
		}},
		{name: "wrong_key_order", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("ALTER TABLE security_control.admission_leases DROP CONSTRAINT admission_leases_operation_generation_key"); err != nil {
				return nil, err
			}
			_, err := db.Exec("CREATE UNIQUE INDEX admission_leases_operation_generation_key ON security_control.admission_leases USING btree (generation, operation_id)")
			return target, err
		}},
		{name: "wrong_opclass_expression", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("ALTER TABLE security_control.admission_boundaries DROP CONSTRAINT admission_boundaries_boundary_nonce_key"); err != nil {
				return nil, err
			}
			_, err := db.Exec("CREATE UNIQUE INDEX admission_boundaries_boundary_nonce_key ON security_control.admission_boundaries USING btree ((boundary_nonce::text))")
			return target, err
		}},
		{name: "wrong_access_method", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("DROP INDEX security_control.admission_boundaries_one_open_per_lease_idx"); err != nil {
				return nil, err
			}
			_, err := db.Exec("CREATE INDEX admission_boundaries_one_open_per_lease_idx ON security_control.admission_boundaries USING hash (lease_id)")
			return target, err
		}},
		{name: "wrong_partial_predicate", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("DROP INDEX security_control.admission_boundaries_one_open_per_lease_idx"); err != nil {
				return nil, err
			}
			_, err := db.Exec("CREATE UNIQUE INDEX admission_boundaries_one_open_per_lease_idx ON security_control.admission_boundaries USING btree (lease_id ASC, generation ASC) WHERE status = 'COMMITTED'")
			return target, err
		}},
		{name: "changed_membership_predicate", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("DROP INDEX security_control.admission_leases_one_live_generation_idx"); err != nil {
				return nil, err
			}
			_, err := db.Exec("CREATE UNIQUE INDEX admission_leases_one_live_generation_idx ON security_control.admission_leases USING btree (operation_id) WHERE status = ANY (ARRAY['ISSUED'::text, 'ACTIVE'::text, 'REVOKED'::text])")
			return target, err
		}},
		{name: "wrong_index_include", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("DROP INDEX security_control.admission_boundaries_one_open_per_lease_idx"); err != nil {
				return nil, err
			}
			_, err := db.Exec("CREATE UNIQUE INDEX admission_boundaries_one_open_per_lease_idx ON security_control.admission_boundaries USING btree (lease_id ASC, generation ASC) INCLUDE (status) WHERE status = 'OPEN'")
			return target, err
		}},
		{name: "wrong_sequence", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("ALTER SEQUENCE security_control.admission_generation_seq INCREMENT BY 2")
			return target, err
		}},
		{name: "wrong_sequence_ownership", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("ALTER SEQUENCE security_control.admission_generation_seq OWNED BY NONE")
			return target, err
		}},
		{name: "wrong_sequence_cache", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("ALTER SEQUENCE security_control.admission_generation_seq CACHE 2")
			return target, err
		}},
		{name: "wrong_sequence_cycle", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("ALTER SEQUENCE security_control.admission_generation_seq CYCLE")
			return target, err
		}},
		{name: "wrong_sequence_type", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("ALTER SEQUENCE security_control.admission_generation_seq MAXVALUE 2147483647"); err != nil {
				return nil, err
			}
			_, err := db.Exec("ALTER SEQUENCE security_control.admission_generation_seq AS integer")
			return target, err
		}},
		{name: "wrong_column_collation", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("CREATE COLLATION public.d1l_c (provider = libc, locale = 'C')"); err != nil {
				return nil, err
			}
			_, err := db.Exec("ALTER TABLE security_control.admission_leases ALTER COLUMN status TYPE text COLLATE public.d1l_c USING status::text")
			return target, err
		}},
		{name: "wrong_relation_persistence", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("ALTER TABLE security_control.admission_boundaries DROP CONSTRAINT admission_boundaries_lease_fk"); err != nil {
				return nil, err
			}
			if _, err := db.Exec("ALTER TABLE security_control.admission_boundaries SET UNLOGGED"); err != nil {
				return nil, err
			}
			if _, err := db.Exec("ALTER TABLE security_control.admission_leases SET UNLOGGED"); err != nil {
				return nil, err
			}
			_, err := db.Exec("ALTER TABLE security_control.admission_boundaries ADD CONSTRAINT admission_boundaries_lease_fk FOREIGN KEY (lease_id, generation, attempt_id) REFERENCES security_control.admission_leases (lease_id, generation, attempt_id) ON DELETE RESTRICT ON UPDATE RESTRICT")
			return target, err
		}},
		{name: "missing_object", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("DROP SEQUENCE security_control.admission_generation_seq CASCADE")
			return target, err
		}},
		{name: "extra_object", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("CREATE TABLE security_control.extra_control_object (id integer)")
			return target, err
		}},
		{name: "extra_user_trigger", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("CREATE FUNCTION public.d1l_extra_trigger_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$"); err != nil {
				return nil, err
			}
			_, err := db.Exec("CREATE TRIGGER d1l_extra_trigger BEFORE INSERT ON security_control.admission_leases FOR EACH ROW EXECUTE FUNCTION public.d1l_extra_trigger_fn()")
			return target, err
		}},
		{name: "extra_user_rule", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("CREATE RULE d1l_gate_rule AS ON INSERT TO security_control.admission_leases DO ALSO NOTHING")
			return target, err
		}},
		{name: "extra_reserved_domain", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("CREATE DOMAIN security_control.d1l_extra_domain AS text")
			return target, err
		}},
		{name: "extra_reserved_collation", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("CREATE COLLATION security_control.d1l_extra_collation FROM pg_catalog.\"C\"")
			return target, err
		}},
		{name: "manifest_cardinality", state: D1LPartial, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("DELETE FROM security_control.control_schema_migrations")
			return target, err
		}},
		{name: "wrong_version", state: D1LWrongVersion, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("ALTER TABLE security_control.control_schema_migrations DROP CONSTRAINT control_schema_migrations_version_check"); err != nil {
				return nil, err
			}
			_, err := db.Exec("UPDATE security_control.control_schema_migrations SET control_version = 2")
			return target, err
		}},
		{name: "future_hybrid", state: D1LWrongVersion, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("ALTER TABLE security_control.control_schema_migrations DROP CONSTRAINT control_schema_migrations_version_check"); err != nil {
				return nil, err
			}
			if _, err := db.Exec("UPDATE security_control.control_schema_migrations SET control_version = 2"); err != nil {
				return nil, err
			}
			_, err := db.Exec("CREATE TABLE security_control.future_hybrid_object (id integer)")
			return target, err
		}},
		{name: "dirty", state: D1LWrongPhysicalDefinition, apply: func(db *sql.DB) ([]byte, error) {
			if _, err := db.Exec("ALTER TABLE security_control.control_schema_migrations DROP CONSTRAINT control_schema_migrations_dirty_check"); err != nil {
				return nil, err
			}
			_, err := db.Exec("UPDATE security_control.control_schema_migrations SET dirty = true")
			return target, err
		}},
		{name: "wrong_target", state: D1LWrongTarget, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("UPDATE security_control.control_schema_migrations SET target_fingerprint = decode(repeat('00', 32), 'hex')")
			return target, err
		}},
		{name: "wrong_installer", state: D1LWrongInstallerDigest, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("UPDATE security_control.control_schema_migrations SET installer_digest = decode(repeat('00', 32), 'hex')")
			return target, err
		}},
		{name: "partial_manifest_table", state: D1LPartial, apply: func(db *sql.DB) ([]byte, error) {
			_, err := db.Exec("DROP TABLE security_control.control_schema_migrations")
			return target, err
		}},
	}

	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			db, dsn, baseTarget := installD1LTestCatalog(t)
			conn, err := sql.Open("postgres", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			wantTarget, err := mutation.apply(conn)
			if err != nil {
				t.Fatal(err)
			}
			if wantTarget == nil {
				wantTarget = baseTarget
			}
			obs := recognizeD1LDatabase(t, dsn, wantTarget)
			if obs.State == D1LExactReady || obs.State != mutation.state {
				t.Fatalf("mutation=%s state=%s want=%s detail=%s", mutation.name, obs.State, mutation.state, obs.Detail)
			}
			t.Logf("database=%s mutation=%s state=%s", db.Name(), mutation.name, obs.State)
		})
	}
}

func TestD1LPhysicalCatalogIgnoresOutOfScopeRule(t *testing.T) {
	_, dsn, target := installD1LTestCatalog(t)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE RULE d1l_out_of_scope_rule AS ON INSERT TO public.users DO ALSO NOTHING"); err != nil {
		t.Fatal(err)
	}
	if got := recognizeD1LDatabase(t, dsn, target); got.State != D1LExactReady {
		t.Fatalf("out-of-scope rule state=%s detail=%s", got.State, got.Detail)
	}
}

func TestD1LPhysicalCatalogManifestLengthFailsClosed(t *testing.T) {
	_, dsn, target := installD1LTestCatalog(t)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("ALTER TABLE security_control.control_schema_migrations DROP CONSTRAINT control_schema_migrations_installer_digest_check"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE security_control.control_schema_migrations SET installer_digest = decode('00', 'hex')"); err != nil {
		t.Fatal(err)
	}
	obs := recognizeD1LDatabase(t, dsn, target)
	if obs.State == D1LExactReady || obs.State != D1LWrongPhysicalDefinition {
		t.Fatalf("malformed installer state=%s want=%s detail=%s", obs.State, D1LWrongPhysicalDefinition, obs.Detail)
	}
}
