package migrations

import "testing"

func TestD1LPhysicalColumnDescriptorsAreClosedAndQualified(t *testing.T) {
	want := map[string]int{
		"control_schema_migrations": 6,
		"admission_leases":          16,
		"admission_boundaries":      11,
	}
	for table, count := range want {
		columns := d1lColumnDescriptors[table]
		if len(columns) != count {
			t.Fatalf("%s descriptors=%d want %d", table, len(columns), count)
		}
		for i, column := range columns {
			if column.name == "" || column.typeSchema != "pg_catalog" || column.typeName == "" || column.typmod != -1 {
				t.Fatalf("%s column %d is not an exact base-type descriptor: %+v", table, i, column)
			}
			if column.collationSchema != "" && column.collationSchema != "pg_catalog" {
				t.Fatalf("%s.%s has an unqualified collation", table, column.name)
			}
		}
	}
}

func TestD1LColumnDescriptorsRejectDomainAndUserTypeLookalikes(t *testing.T) {
	for table, columns := range d1lColumnDescriptors {
		for _, column := range columns {
			if column.typeSchema != "pg_catalog" {
				t.Errorf("%s.%s permits non-system type namespace %q", table, column.name, column.typeSchema)
			}
		}
	}
}

func TestD1LConstraintDescriptorsRequireExactKindsAndIdentity(t *testing.T) {
	for name, table := range d1lConstraints {
		descriptor, ok := d1lConstraintDescriptors[name]
		if !ok {
			t.Fatalf("constraint %s has no exact descriptor", name)
		}
		if descriptor.table != table || descriptor.contype == "" {
			t.Fatalf("constraint %s descriptor=%+v", name, descriptor)
		}
		if descriptor.contype == "p" || descriptor.contype == "u" {
			if descriptor.conindid != name || descriptor.conkey == "" {
				t.Fatalf("constraint %s does not bind its backing index and keys: %+v", name, descriptor)
			}
		}
		if descriptor.contype == "f" {
			if descriptor.referencedSchema != "security_control" || descriptor.referencedTable == "" || descriptor.confkey == "" || descriptor.confupdtype != "r" || descriptor.confdeltype != "r" || descriptor.confmatchtype != "s" {
				t.Fatalf("constraint %s foreign-key identity=%+v", name, descriptor)
			}
		}
	}
}
