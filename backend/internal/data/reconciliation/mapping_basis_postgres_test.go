//go:build securityintegration

package reconciliation

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)

func TestMappingArtifactTemporalBoundaryRejectsWithoutWrites(t *testing.T) {
	db := protectedTestDB(t)
	fixture := createProtectedFixture(t, db, false)
	if _, err := db.Exec(`UPDATE shops SET client_id = NULL WHERE id = $1`, fixture.shop1); err != nil {
		t.Fatal(err)
	}
	validTo := time.Now().UTC().Add(time.Hour)
	if _, err := db.Exec(`UPDATE device_assignments SET valid_to = $1 WHERE id = $2`, validTo, fixture.assignment); err != nil {
		t.Fatal(err)
	}
	diagnostic, err := DiagnoseV5(context.Background(), os.Getenv("TEST_DATABASE_URL"), nil)
	if err != nil {
		t.Fatal(err)
	}
	artifact := &MappingArtifact{SchemaVersion: MappingSchema, Version: 5, SourceFactsDigest: diagnostic.MappingBasisDigest, Mappings: []MappingEntry{
		{Category: MappingShop, ShopID: uint(fixture.shop1), ClientID: uint(fixture.client1)},
		{Category: MappingDevice, DeviceID: uint(fixture.device), ClientID: uint(fixture.client1)},
	}}
	executor := NewProtectedExecutor(nil)
	executor.hooks.FrozenTime = func(actual time.Time) time.Time {
		return diagnostic.FrozenAt.Add(2 * time.Hour)
	}
	report, err := executor.Execute(context.Background(), os.Getenv("TEST_DATABASE_URL"), artifact)
	if err == nil || report.Outcome != ExecutionNotCommitted {
		t.Fatalf("temporal stale report=%+v err=%v", report, err)
	}
	var shopClient sql.NullInt64
	if err := db.QueryRow(`SELECT client_id FROM shops WHERE id=$1`, fixture.shop1).Scan(&shopClient); err != nil {
		t.Fatal(err)
	}
	var owner sql.NullInt64
	if err := db.QueryRow(`SELECT inventory_owner_client_id FROM devices WHERE id=$1`, fixture.device).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if shopClient.Valid || owner.Valid {
		t.Fatalf("temporal stale path wrote shop/device=%v/%v", shopClient, owner)
	}
}
