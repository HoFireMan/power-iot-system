package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// verifyMeasurementPointIdentityCatalog is part of B-02 serving admission.
// A database with metadata version 7 but without IDENT-002 must not be
// admitted: the active alert writer now requires the migrated columns.
func verifyMeasurementPointIdentityCatalog(ctx context.Context, q ProtectedMigrationQueryer) error {
	columns := []struct {
		table, name, dataType, nullable, defaultFragment string
	}{
		{"alert_logs", "measurement_point_id", "uuid", "YES", ""},
		{"alert_logs", "legacy_unresolved", "boolean", "NO", "true"},
		{"daily_usages", "measurement_point_id", "uuid", "YES", ""},
		{"daily_usages", "legacy_unresolved", "boolean", "NO", "true"},
		{"daily_usages", "device_id", "bigint", "YES", ""},
	}
	for _, column := range columns {
		var dataType, nullable string
		var columnDefault sql.NullString
		if err := q.QueryRowContext(ctx, `
SELECT data_type, is_nullable, column_default
FROM information_schema.columns
WHERE table_schema=current_schema() AND table_name=$1 AND column_name=$2`, column.table, column.name).Scan(&dataType, &nullable, &columnDefault); err != nil {
			return fmt.Errorf("IDENT-002 column %s.%s: %w", column.table, column.name, err)
		}
		if dataType != column.dataType || nullable != column.nullable {
			return fmt.Errorf("IDENT-002 column %s.%s has type/nullability %s/%s, want %s/%s", column.table, column.name, dataType, nullable, column.dataType, column.nullable)
		}
		if column.defaultFragment != "" && (!columnDefault.Valid || !strings.Contains(normalizeCatalogSQL(columnDefault.String), column.defaultFragment)) {
			return fmt.Errorf("IDENT-002 column %s.%s has unexpected default %q", column.table, column.name, columnDefault.String)
		}
	}

	for _, table := range []string{"alert_logs", "daily_usages"} {
		constraint := table + "_measurement_point_fk"
		var count int
		if err := q.QueryRowContext(ctx, `
SELECT count(*)
FROM pg_constraint AS c
JOIN pg_class AS t ON t.oid=c.conrelid
JOIN pg_namespace AS n ON n.oid=t.relnamespace
WHERE n.nspname=current_schema() AND t.relname=$1 AND c.conname=$2
  AND c.contype='f'
  AND pg_get_constraintdef(c.oid) ILIKE '%measurement_point_id%measurement_points%'`, table, constraint).Scan(&count); err != nil {
			return fmt.Errorf("IDENT-002 foreign key %s: %w", constraint, err)
		}
		if count != 1 {
			return fmt.Errorf("IDENT-002 foreign key %s count=%d", constraint, count)
		}
	}

	for _, table := range []string{"alert_logs", "daily_usages"} {
		constraint := table + "_identity_state_check"
		definition, err := b02ConstraintDefinition(ctx, q, table, constraint)
		if err != nil {
			return err
		}
		if canonicalIdentityExpression(definition) != "checkmeasurement_point_idisnull=legacy_unresolved" {
			return fmt.Errorf("IDENT-002 check %s has unexpected definition %q", constraint, definition)
		}
	}

	var unique bool
	var indexDefinition, predicate string
	if err := q.QueryRowContext(ctx, `
SELECT i.indisunique, pg_get_indexdef(i.indexrelid), pg_get_expr(i.indpred, i.indrelid)
FROM pg_index AS i
JOIN pg_class AS index_class ON index_class.oid=i.indexrelid
JOIN pg_namespace AS index_namespace ON index_namespace.oid=index_class.relnamespace
WHERE index_namespace.nspname=current_schema()
  AND index_class.relname='daily_usages_measurement_point_date_key'`).Scan(&unique, &indexDefinition, &predicate); err != nil {
		return fmt.Errorf("IDENT-002 daily usage uniqueness index: %w", err)
	}
	if !unique || !strings.Contains(normalizeCatalogSQL(indexDefinition), "(date,measurement_point_id)") {
		return fmt.Errorf("IDENT-002 daily usage uniqueness index is not unique MP/date: %q", indexDefinition)
	}
	if canonicalIdentityExpression(predicate) != "measurement_point_idisnotnullandnotlegacy_unresolved" {
		return fmt.Errorf("IDENT-002 daily usage uniqueness predicate=%q", predicate)
	}
	return nil
}

func canonicalIdentityExpression(value string) string {
	value = normalizeCatalogSQL(value)
	value = strings.ReplaceAll(value, "(", "")
	value = strings.ReplaceAll(value, ")", "")
	return value
}
