package main

import (
	"context"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/data/migrations"
)

func TestInitDataParticipatesInSharedWriterFence(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; server PostgreSQL integration test not run")
	}
	if err := migrations.Up(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Where("key = ?", "carbon_factor").Delete(&domain.SystemConfig{}).Error; err != nil {
		t.Fatal(err)
	}
	fence, err := migrations.OpenExclusiveWriterFence(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		initData(db)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("initData crossed exclusive writer fence")
	case <-time.After(150 * time.Millisecond):
	}
	var count int64
	if err := db.Model(&domain.SystemConfig{}).Where("key = ?", "carbon_factor").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("initData wrote before shared admission")
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("initData did not proceed after exclusive release")
	}
	if err := db.Model(&domain.SystemConfig{}).Where("key = ?", "carbon_factor").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("initData count=%d, want one committed seed", count)
	}
	if err := db.Where("key = ?", "carbon_factor").Delete(&domain.SystemConfig{}).Error; err != nil {
		t.Fatal(err)
	}
}
