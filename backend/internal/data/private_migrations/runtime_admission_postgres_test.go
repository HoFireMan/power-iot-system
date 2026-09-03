package migrations

import (
	"context"
	"errors"
	"os"
	"testing"

	"power-iot-backend/internal/testsupport"
)

func TestBootstrapAndAdmitCleanV6ServesWithoutReplay(t *testing.T) {
	db, err := testsupport.New(context.Background(), os.Getenv("TEST_DATABASE_URL"), Up)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := RunD5Migration(context.Background(), db.DSN(), trustedExternalWriterAdmissionForTest()); err != nil {
		t.Fatal(err)
	}
	report, err := BootstrapAndAdmit(context.Background(), db.DSN())
	if err != nil {
		t.Fatalf("clean V6 was refused: report=%+v err=%v", report, err)
	}
	if report.State != ProtectedStateCleanV6 || report.Disposition != RuntimeServeV6 {
		t.Fatalf("report=%+v, want clean V6/serve", report)
	}
}

func TestBootstrapAndAdmitCleanV5RequiresProtectedEntry(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	if err := Up(dsn); err != nil {
		t.Fatal(err)
	}
	report, err := BootstrapAndAdmit(context.Background(), dsn)
	if !errors.Is(err, ErrRuntimeProtectedMigrationRequired) {
		t.Fatalf("error=%v, want protected migration requirement", err)
	}
	if report.State != ProtectedStateCleanV5 || report.Disposition != RuntimeProtectedMigrationNeeded {
		t.Fatalf("report=%+v, want clean V5/protected migration required", report)
	}
	version, dirty, err := Version(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if version != 5 || dirty {
		t.Fatalf("startup admission changed schema: version=%d dirty=%t", version, dirty)
	}
}
