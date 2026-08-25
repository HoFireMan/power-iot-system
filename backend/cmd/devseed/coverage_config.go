package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"power-iot-backend/internal/core/coverage"
	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/data/migrations"
)

const (
	coverageMaxIntervalConfigKey         = "coverage.max_interval_ms"
	coverageMaxIntervalConfigEnv         = "DEVSEED_COVERAGE_MAX_INTERVAL_MS"
	coverageMaxIntervalConfigDescription = "B-02 coverage maximum interval in milliseconds"
)

// parseCoverageMaxIntervalValue accepts only a base-10 integer in the range
// already established by the B-02 coverage validator. It deliberately has no
// production default: callers must provide a value explicitly.
func parseCoverageMaxIntervalValue(raw string) (int64, error) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, errors.New("coverage max interval must be a base-10 integer in milliseconds")
	}
	if value < coverage.MinIntervalMilliseconds {
		return 0, fmt.Errorf("coverage max interval must be at least %d milliseconds", coverage.MinIntervalMilliseconds)
	}
	return value, nil
}

func resolveCoverageMaxIntervalInput(flagValue string, flagSet bool, envValue string, envSet bool) (string, bool) {
	if flagSet {
		return flagValue, true
	}
	if envSet {
		return envValue, true
	}
	return "", false
}

func parseOptionalCoverageMaxIntervalValue(raw string, provided bool) (*int64, error) {
	if !provided {
		return nil, nil
	}
	value, err := parseCoverageMaxIntervalValue(raw)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// ensureCoverageMaxInterval is the development/test-only writer for the
// runtime-owned system_configs key. It is idempotent for the exact canonical
// value and refuses to overwrite an existing different value.
func ensureCoverageMaxInterval(ctx context.Context, db *gorm.DB, value *int64) error {
	if value == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if *value < coverage.MinIntervalMilliseconds {
		return fmt.Errorf("coverage max interval must be at least %d milliseconds", coverage.MinIntervalMilliseconds)
	}
	if db == nil {
		return errors.New("coverage configuration database is required")
	}
	canonical := strconv.FormatInt(*value, 10)
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := migrations.AcquireSharedWriterFenceOnGORM(ctx, tx); err != nil {
			return err
		}
		var config domain.SystemConfig
		result := tx.Where("key = ?", coverageMaxIntervalConfigKey).First(&config)
		switch {
		case errors.Is(result.Error, gorm.ErrRecordNotFound):
			createResult := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&domain.SystemConfig{
				Key:         coverageMaxIntervalConfigKey,
				Value:       canonical,
				Description: coverageMaxIntervalConfigDescription,
			})
			if createResult.Error != nil {
				return createResult.Error
			}
			if createResult.RowsAffected == 1 {
				return nil
			}
			if err := tx.Where("key = ?", coverageMaxIntervalConfigKey).First(&config).Error; err != nil {
				return err
			}
			if config.Value == canonical {
				return nil
			}
			return fmt.Errorf("coverage max interval configuration conflict")
		case result.Error != nil:
			return result.Error
		case config.Value == canonical:
			return nil
		default:
			return fmt.Errorf("coverage max interval configuration conflict")
		}
	})
}
