package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/data/migrations"
)

func TestDevseedFixturePhaseParticipatesInSharedWriterFence(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; devseed PostgreSQL integration test not run")
	}
	if err := migrations.Up(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	mac := strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))[:12]
	fence, err := migrations.OpenExclusiveWriterFence(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	resultCh := make(chan error, 1)
	go func() {
		_, _, err := seedFixtures(context.Background(), db, mac, "fenced-devseed", 0, "", "")
		resultCh <- err
	}()
	select {
	case err := <-resultCh:
		t.Fatalf("devseed fixture phase crossed exclusive fence: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	var count int64
	if err := db.Model(&domain.Device{}).Where("mac_address = ?", mac).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("devseed fixture wrote before shared admission")
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("devseed fixture phase did not proceed after exclusive release")
	}
	if err := db.Where("mac_address = ?", mac).Delete(&domain.Device{}).Error; err != nil {
		t.Fatal(err)
	}
}
