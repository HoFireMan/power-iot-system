package reconciliation

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"power-iot-backend/internal/data/reconciliation/sourceowner"
)

type ownerFixtureConnector struct {
	facts      FactSet
	observedAt time.Time
}

func (c ownerFixtureConnector) Connect(context.Context) (driver.Conn, error) {
	return &ownerFixtureConn{facts: c.facts, observedAt: c.observedAt}, nil
}

func (ownerFixtureConnector) Driver() driver.Driver { return ownerFixtureDriver{} }

type ownerFixtureDriver struct{}

func (ownerFixtureDriver) Open(string) (driver.Conn, error) {
	return nil, fmt.Errorf("owner fixture requires sql.OpenDB connector")
}

type ownerFixtureConn struct {
	facts      FactSet
	observedAt time.Time
}

func (c *ownerFixtureConn) Prepare(string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}

func (c *ownerFixtureConn) Close() error { return nil }

func (c *ownerFixtureConn) Begin() (driver.Tx, error) {
	return &ownerFixtureTx{conn: c}, nil
}

func (c *ownerFixtureConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &ownerFixtureTx{conn: c}, nil
}

func (c *ownerFixtureConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	return ownerFixtureRowsFor(c.facts, c.observedAt, query)
}

func (c *ownerFixtureConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, fmt.Errorf("owner fixture rejects writes")
}

type ownerFixtureTx struct{ conn *ownerFixtureConn }

func (tx *ownerFixtureTx) Commit() error   { return nil }
func (tx *ownerFixtureTx) Rollback() error { return nil }
func (tx *ownerFixtureTx) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return tx.conn.QueryContext(context.Background(), query, args)
}
func (tx *ownerFixtureTx) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return tx.conn.ExecContext(context.Background(), query, args)
}

type ownerFixtureRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *ownerFixtureRows) Columns() []string { return r.columns }
func (r *ownerFixtureRows) Close() error      { return nil }
func (r *ownerFixtureRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	row := r.values[r.index]
	if len(dest) != len(row) {
		return fmt.Errorf("owner fixture columns=%d destination=%d", len(row), len(dest))
	}
	copy(dest, row)
	r.index++
	return nil
}

func ownerFixtureRowsFor(facts FactSet, observedAt time.Time, query string) (driver.Rows, error) {
	lower := strings.ToLower(query)
	switch {
	case strings.Contains(lower, "transaction_timestamp"):
		return &ownerFixtureRows{columns: []string{"transaction_timestamp"}, values: [][]driver.Value{{observedAt}}}, nil
	case strings.Contains(lower, "count(*)") && strings.Contains(lower, "schema_migrations"):
		return &ownerFixtureRows{columns: []string{"count"}, values: [][]driver.Value{{int64(1)}}}, nil
	case strings.Contains(lower, "select version, dirty"):
		return &ownerFixtureRows{columns: []string{"version", "dirty"}, values: [][]driver.Value{{int64(5), false}}}, nil
	case strings.Contains(lower, "from clients"):
		values := make([][]driver.Value, 0, len(facts.Clients))
		for _, row := range facts.Clients {
			values = append(values, []driver.Value{int64(row.ID)})
		}
		return &ownerFixtureRows{columns: []string{"id"}, values: values}, nil
	case strings.Contains(lower, "from shops"):
		values := make([][]driver.Value, 0, len(facts.Shops))
		for _, row := range facts.Shops {
			values = append(values, []driver.Value{int64(row.ID), uintValue(row.ClientID)})
		}
		return &ownerFixtureRows{columns: []string{"id", "client_id"}, values: values}, nil
	case strings.Contains(lower, "from users"):
		values := make([][]driver.Value, 0, len(facts.Users))
		for _, row := range facts.Users {
			values = append(values, []driver.Value{int64(row.ID), uintValue(row.CurrentShopID), row.AuthEnabled})
		}
		return &ownerFixtureRows{columns: []string{"id", "current_shop_id", "auth_enabled"}, values: values}, nil
	case strings.Contains(lower, "from user_shop_relations"):
		values := make([][]driver.Value, 0, len(facts.UserShopRelations))
		for _, row := range facts.UserShopRelations {
			values = append(values, []driver.Value{int64(row.ID), int64(row.UserID), int64(row.ShopID)})
		}
		return &ownerFixtureRows{columns: []string{"id", "user_id", "shop_id"}, values: values}, nil
	case strings.Contains(lower, "from devices"):
		values := make([][]driver.Value, 0, len(facts.Devices))
		for _, row := range facts.Devices {
			values = append(values, []driver.Value{int64(row.ID), int64(row.ShopID), uintValue(row.InventoryOwnerClientID)})
		}
		return &ownerFixtureRows{columns: []string{"id", "shop_id", "inventory_owner_client_id"}, values: values}, nil
	case strings.Contains(lower, "from measurement_points"):
		values := make([][]driver.Value, 0, len(facts.MeasurementPoints))
		for _, row := range facts.MeasurementPoints {
			values = append(values, []driver.Value{row.ID.String(), int64(row.ShopID)})
		}
		return &ownerFixtureRows{columns: []string{"id", "shop_id"}, values: values}, nil
	case strings.Contains(lower, "from device_assignments"):
		values := make([][]driver.Value, 0, len(facts.DeviceAssignments))
		for _, row := range facts.DeviceAssignments {
			values = append(values, []driver.Value{row.ID.String(), int64(row.DeviceID), row.MeasurementPointID.String(), row.ValidFrom, timeValue(row.ValidTo)})
		}
		return &ownerFixtureRows{columns: []string{"id", "device_id", "measurement_point_id", "valid_from", "valid_to"}, values: values}, nil
	case strings.Contains(lower, "from admin_binding_operations"):
		return &ownerFixtureRows{columns: []string{"id", "operation_id", "idempotency_key", "operation", "actor_id", "scope_key", "scope_snapshot", "canonical_request_hash", "client_id", "committed_response", "committed_at", "created_at"}, values: nil}, nil
	case strings.Contains(lower, "from admin_binding_audits"):
		return &ownerFixtureRows{columns: []string{"id", "operation_id", "request_identity", "action", "actor_id", "scope_key", "scope_snapshot", "occurred_at", "effective_at", "client_id", "shop_id", "measurement_point_id", "device_id", "device_serial_number", "device_mac", "old_measurement_point_id", "new_measurement_point_id", "old_assignment_id", "new_assignment_id", "reason", "metadata"}, values: nil}, nil
	default:
		return nil, fmt.Errorf("owner fixture does not recognize query %q", query)
	}
}

func uintValue(value *uint) driver.Value {
	if value == nil {
		return nil
	}
	return int64(*value)
}

func timeValue(value *time.Time) driver.Value {
	if value == nil {
		return nil
	}
	return *value
}

func trustedSnapshotForTest(t *testing.T, facts FactSet, request ...ProtectedAdmissionContext) TrustedSourceSnapshot {
	t.Helper()
	binding := sourceowner.NewInvocationBinding(uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"), uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"))
	if len(request) > 0 {
		binding = sourceowner.NewInvocationBinding(request[0].OperationID, request[0].AttemptID)
	}
	sqlDB := sql.OpenDB(ownerFixtureConnector{facts: facts, observedAt: facts.AsOf})
	t.Cleanup(func() { _ = sqlDB.Close() })
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	owner := sourceowner.NewPostgresSourceOwner(gdb)
	snapshot, err := owner.CollectTrustedV5(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
