//go:build securityintegration

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"power-iot-backend/internal/data/reconciliation"
)

func operatorTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" || strings.Contains(strings.ToLower(dsn), "power_iot") {
		t.Fatal("dedicated TEST_DATABASE_URL is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db
}

type operatorFixture struct {
	client1 int64
	shop1   int64
	device  int64
}

func createOperatorFixture(t *testing.T, db *sql.DB) operatorFixture {
	t.Helper()
	if _, err := db.Exec(`TRUNCATE TABLE clients, users, devices, device_types RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	var f operatorFixture
	var client1, client2, shop1, shop2, userID int64
	if err := db.QueryRow(`INSERT INTO clients (code, name) VALUES ('operator-client-1', 'Operator Client 1') RETURNING id`).Scan(&client1); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO clients (code, name) VALUES ('operator-client-2', 'Operator Client 2') RETURNING id`).Scan(&client2); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO shops (client_id, code, name) VALUES ($1, 'operator-shop-1', 'Operator Shop 1') RETURNING id`, client1).Scan(&shop1); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO shops (client_id, code, name) VALUES ($1, 'operator-shop-2', 'Operator Shop 2') RETURNING id`, client2).Scan(&shop2); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO users (account, password_hash, name, current_shop_id, auth_enabled) VALUES ('operator-user', 'test-hash', 'Operator User', $1, true) RETURNING id`, shop1).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_shop_relations (user_id, shop_id) VALUES ($1, $2)`, userID, shop1); err != nil {
		t.Fatal(err)
	}
	var device int64
	if err := db.QueryRow(`INSERT INTO devices (shop_id, mac_address, name) VALUES ($1, 'AABBCCDDEEFF', 'Operator Device') RETURNING id`, shop2).Scan(&device); err != nil {
		t.Fatal(err)
	}
	var point string
	if err := db.QueryRow(`INSERT INTO measurement_points (shop_id, name) VALUES ($1, 'Operator Point') RETURNING id`, shop1).Scan(&point); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO device_assignments (device_id, measurement_point_id, valid_from) VALUES ($1, $2, $3)`, device, point, time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	f.client1, f.shop1, f.device = client1, shop1, device
	return f
}

func TestOperatorBoundaryDiagnosticThenExplicitMappingExecuteAcrossSnapshotTimes(t *testing.T) {
	db := operatorTestDB(t)
	f := createOperatorFixture(t, db)
	if _, err := db.Exec(`UPDATE shops SET client_id = NULL WHERE id = $1`, f.shop1); err != nil {
		t.Fatal(err)
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	var diagnosticOut, diagnosticErr bytes.Buffer
	if code := run([]string{"-database-url", dsn}, &diagnosticOut, &diagnosticErr); code != 0 {
		t.Fatalf("diagnostic code=%d out=%s err=%s", code, diagnosticOut.String(), diagnosticErr.String())
	}
	var diagnostic reconciliation.OperatorReport
	if err := json.Unmarshal(diagnosticOut.Bytes(), &diagnostic); err != nil {
		t.Fatal(err)
	}
	if diagnostic.Mode != reconciliation.OperatorDiagnostic || diagnostic.MappingBasisDigest == "" || len(diagnostic.RequiredExplicitMappings) == 0 {
		t.Fatalf("diagnostic report=%+v", diagnostic)
	}
	artifact := reconciliation.MappingArtifact{
		SchemaVersion: reconciliation.MappingSchema, Version: 5, SourceFactsDigest: diagnostic.MappingBasisDigest,
		Mappings: []reconciliation.MappingEntry{
			{Category: reconciliation.MappingShop, ShopID: uint(f.shop1), ClientID: uint(f.client1)},
			{Category: reconciliation.MappingDevice, DeviceID: uint(f.device), ClientID: uint(f.client1)},
		},
	}
	artifactBytes, err := artifact.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	mappingPath := filepath.Join(t.TempDir(), "mapping.json")
	if err := os.WriteFile(mappingPath, artifactBytes, 0600); err != nil {
		t.Fatal(err)
	}
	var executeOut, executeErr bytes.Buffer
	if code := run([]string{"-database-url", dsn, "-mapping-file", mappingPath, "-execute"}, &executeOut, &executeErr); code != 0 {
		t.Fatalf("execute code=%d out=%s err=%s", code, executeOut.String(), executeErr.String())
	}
	var executed reconciliation.OperatorReport
	if err := json.Unmarshal(executeOut.Bytes(), &executed); err != nil {
		t.Fatal(err)
	}
	if executed.Outcome != reconciliation.ExecutionCommittedAndVerified || !executed.PostCommitVerified || executed.FrozenAt.Equal(diagnostic.FrozenAt) {
		t.Fatalf("diagnostic/execute reports=%+v/%+v", diagnostic, executed)
	}
	var shopClient, owner int64
	if err := db.QueryRow(`SELECT client_id FROM shops WHERE id=$1`, f.shop1).Scan(&shopClient); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT inventory_owner_client_id FROM devices WHERE id=$1`, f.device).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if shopClient != f.client1 || owner != f.client1 {
		t.Fatalf("explicit mapping durable state shop/device=%d/%d want=%d", shopClient, owner, f.client1)
	}
}

func TestOperatorBoundaryDiagnosticThenExplicitIdempotentExecute(t *testing.T) {
	db := operatorTestDB(t)
	createOperatorFixture(t, db)
	dsn := os.Getenv("TEST_DATABASE_URL")
	var diagnosticOut, diagnosticErr bytes.Buffer
	if code := run([]string{"-database-url", dsn}, &diagnosticOut, &diagnosticErr); code != 0 {
		t.Fatalf("diagnostic code=%d out=%s err=%s", code, diagnosticOut.String(), diagnosticErr.String())
	}
	var diagnostic reconciliation.OperatorReport
	if err := json.Unmarshal(diagnosticOut.Bytes(), &diagnostic); err != nil {
		t.Fatal(err)
	}
	if diagnostic.Mode != reconciliation.OperatorDiagnostic || diagnostic.Outcome != "" {
		t.Fatalf("diagnostic report=%+v", diagnostic)
	}
	var nullOwners int
	if err := db.QueryRow(`SELECT count(*) FROM devices WHERE inventory_owner_client_id IS NULL`).Scan(&nullOwners); err != nil {
		t.Fatal(err)
	}
	if nullOwners != 1 {
		t.Fatalf("diagnostic mutated device provenance: null owners=%d", nullOwners)
	}
	var executeOut, executeErr bytes.Buffer
	if code := run([]string{"-database-url", dsn, "-execute"}, &executeOut, &executeErr); code != 0 {
		t.Fatalf("execute code=%d out=%s err=%s", code, executeOut.String(), executeErr.String())
	}
	var executed reconciliation.OperatorReport
	if err := json.Unmarshal(executeOut.Bytes(), &executed); err != nil {
		t.Fatal(err)
	}
	if executed.Mode != reconciliation.OperatorExecute || executed.Outcome != reconciliation.ExecutionCommittedAndVerified || !executed.PostCommitVerified {
		t.Fatalf("execute report=%+v", executed)
	}
	var rerunOut, rerunErr bytes.Buffer
	if code := run([]string{"-database-url", dsn, "-execute"}, &rerunOut, &rerunErr); code != 0 {
		t.Fatalf("rerun code=%d out=%s err=%s", code, rerunOut.String(), rerunErr.String())
	}
	var rerun reconciliation.OperatorReport
	if err := json.Unmarshal(rerunOut.Bytes(), &rerun); err != nil {
		t.Fatal(err)
	}
	if rerun.AppliedAffectedCounts[reconciliation.ExpectedCountInventoryOwnerUpdates] != 0 {
		t.Fatalf("rerun was not idempotent: %+v", rerun.AppliedAffectedCounts)
	}
}
