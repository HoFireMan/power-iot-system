package main

import (
	"context"
	"os"
	"testing"
	"time"

	"gorm.io/gorm"

	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/data/private_migrations"
)

func TestCoverageConfigPersistenceSemantics(t *testing.T) {
	db := openCoverageConfigTestDB(t)

	if err := ensureCoverageMaxInterval(context.Background(), db, nil); err != nil {
		t.Fatalf("omitted configuration: %v", err)
	}
	assertCoverageConfigCount(t, db, 0)

	fiveSeconds := int64(5000)
	if err := ensureCoverageMaxInterval(context.Background(), db, &fiveSeconds); err != nil {
		t.Fatalf("create configuration: %v", err)
	}
	assertCoverageConfig(t, db, "5000")

	if err := ensureCoverageMaxInterval(context.Background(), db, &fiveSeconds); err != nil {
		t.Fatalf("idempotent configuration: %v", err)
	}
	assertCoverageConfig(t, db, "5000")

	oneSecond := int64(1000)
	if err := ensureCoverageMaxInterval(context.Background(), db, &oneSecond); err == nil {
		t.Fatal("different configuration value was accepted")
	}
	assertCoverageConfig(t, db, "5000")
}

func TestCoverageConfigRejectsInvalidPersistenceValueWithoutMutation(t *testing.T) {
	db := openCoverageConfigTestDB(t)
	for _, value := range []int64{999, 0, -1} {
		if err := ensureCoverageMaxInterval(context.Background(), db, &value); err == nil {
			t.Fatalf("invalid value %d was accepted", value)
		}
	}
	assertCoverageConfigCount(t, db, 0)
}

func TestCoverageConfigUsesSharedWriterFence(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; devseed PostgreSQL integration test not run")
	}
	if err := migrations.Up(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := openDevseedDatabase(dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Where("key = ?", coverageMaxIntervalConfigKey).Delete(&domain.SystemConfig{}).Error; err != nil {
			t.Errorf("cleanup coverage config: %v", err)
		}
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	if err := db.Where("key = ?", coverageMaxIntervalConfigKey).Delete(&domain.SystemConfig{}).Error; err != nil {
		t.Fatal(err)
	}

	fence, err := migrations.OpenExclusiveWriterFence(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	value := int64(5000)
	resultCh := make(chan error, 1)
	go func() {
		resultCh <- ensureCoverageMaxInterval(context.Background(), db, &value)
	}()
	select {
	case err := <-resultCh:
		t.Fatalf("configuration write crossed exclusive fence: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	assertCoverageConfigCount(t, db, 0)
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("configuration write did not proceed after exclusive release")
	}
	assertCoverageConfig(t, db, "5000")
}

func openCoverageConfigTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; devseed PostgreSQL integration test not run")
	}
	if err := migrations.Up(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := openDevseedDatabase(dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Where("key = ?", coverageMaxIntervalConfigKey).Delete(&domain.SystemConfig{}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Where("key = ?", coverageMaxIntervalConfigKey).Delete(&domain.SystemConfig{}).Error; err != nil {
			t.Errorf("cleanup coverage config: %v", err)
		}
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	return db
}

func assertCoverageConfig(t *testing.T, db *gorm.DB, want string) {
	t.Helper()
	var config domain.SystemConfig
	if err := db.Where("key = ?", coverageMaxIntervalConfigKey).First(&config).Error; err != nil {
		t.Fatal(err)
	}
	if config.Value != want {
		t.Fatalf("coverage config value=%q, want %q", config.Value, want)
	}
	if config.Description != coverageMaxIntervalConfigDescription {
		t.Fatalf("coverage config description=%q, want %q", config.Description, coverageMaxIntervalConfigDescription)
	}
}

func assertCoverageConfigCount(t *testing.T, db *gorm.DB, want int64) {
	t.Helper()
	var count int64
	if err := db.Model(&domain.SystemConfig{}).Where("key = ?", coverageMaxIntervalConfigKey).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("coverage config count=%d, want %d", count, want)
	}
}
