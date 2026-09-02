package iot

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"power-iot-backend/internal/core/coverage"
	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/data/migrations"
)

type IngestResultStatus string

const (
	IngestStored            IngestResultStatus = "stored"
	IngestDuplicate         IngestResultStatus = "duplicate"
	IngestUnknownDevice     IngestResultStatus = "unknown_device"
	IngestUnknownAssignment IngestResultStatus = "unknown_assignment"
	IngestConflict          IngestResultStatus = "conflict"
	IngestInvalid           IngestResultStatus = "invalid"
	IngestFailed            IngestResultStatus = "failed"
)

type IngestResult struct{ Status IngestResultStatus }

var errCoverageAssignmentRollback = errors.New("coverage assignment missing; rollback")

type TelemetryIngestor struct {
	db                  *gorm.DB
	afterKeyClaim       func() error
	beforeDeviceLock    func(*gorm.DB) error
	afterDeviceLock     func() error
	coverageMaxInterval func(*gorm.DB) (int64, error)
}

func NewTelemetryIngestor(db *gorm.DB) *TelemetryIngestor { return &TelemetryIngestor{db: db} }

// SetCoverageMaxIntervalProvider is a test/configuration seam. A nil provider
// reads the required millisecond value from system_configs.
func (i *TelemetryIngestor) SetCoverageMaxIntervalProvider(provider func(*gorm.DB) (int64, error)) {
	if i != nil {
		i.coverageMaxInterval = provider
	}
}

func (i *TelemetryIngestor) Ingest(data MqttPayload, receivedAt time.Time) (IngestResult, error) {
	return i.IngestContext(context.Background(), data, receivedAt)
}

func (i *TelemetryIngestor) IngestContext(ctx context.Context, data MqttPayload, receivedAt time.Time) (IngestResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validatePersistableTelemetry(data); err != nil {
		return IngestResult{Status: IngestInvalid}, err
	}
	if i == nil || i.db == nil {
		return IngestResult{Status: IngestFailed}, errors.New("database is not configured")
	}
	mac, err := NormalizeMAC(data.MacAddress)
	if err != nil {
		return IngestResult{Status: IngestInvalid}, err
	}
	data.MacAddress = mac
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	} else {
		receivedAt = receivedAt.UTC()
	}
	isCoverage := data.CoverageVersion.Present
	recordedAt := telemetryTimeAt(data.Timestamp, receivedAt)
	if isCoverage {
		recordedAt = time.UnixMilli(data.IntervalStartTS.Value).UTC()
	}
	result := IngestResult{Status: IngestFailed}

	err = i.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := migrations.AcquireSharedWriterFenceOnGORM(ctx, tx); err != nil {
			return err
		}
		var device domain.Device
		if i.beforeDeviceLock != nil {
			if err := i.beforeDeviceLock(tx); err != nil {
				return err
			}
		}
		if err := findDeviceForUpdate(tx, data.MacAddress, &device); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				result.Status = IngestUnknownDevice
				return nil
			}
			return err
		}
		if i.afterDeviceLock != nil {
			if err := i.afterDeviceLock(); err != nil {
				return err
			}
		}

		var digest []byte
		if isCoverage {
			maxInterval, err := i.loadCoverageMaxInterval(tx)
			if err != nil {
				return err
			}
			interval := coverage.Interval{
				StartMilliseconds: data.IntervalStartTS.Value,
				EndMilliseconds:   data.IntervalEndTS.Value,
				TimestampSeconds:  data.Timestamp,
			}
			if err := interval.Validate(maxInterval); err != nil {
				result.Status = IngestInvalid
				return err
			}
			hash := coverage.Digest(coverage.DigestInput{
				DeviceID: uint64(device.ID), ProfileVersion: data.CoverageVersion.Value,
				BootCounter: data.BootCounter, Sequence: data.Sequence,
				IntervalStartMs: data.IntervalStartTS.Value, IntervalEndMs: data.IntervalEndTS.Value,
				RecordedAt: recordedAt, EnergyDeltaKwh: energyDeltaValue(data.EnergyDeltaKwh),
			})
			digest = append([]byte(nil), hash[:]...)
		}

		key := domain.TelemetryIngestKey{
			ID: uuid.New(), DeviceID: device.ID, BootCounter: data.BootCounter,
			Sequence: data.Sequence, ReceivedAt: receivedAt,
			CanonicalCoverageDigest: digest,
		}
		keyInsert := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "device_id"}, {Name: "boot_counter"}, {Name: "sequence"}},
			DoNothing: true,
		}).Create(&key)
		if keyInsert.Error != nil {
			return keyInsert.Error
		}

		if keyInsert.RowsAffected == 0 {
			var existing domain.TelemetryIngestKey
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("device_id = ? AND boot_counter = ? AND sequence = ?", device.ID, data.BootCounter, data.Sequence).
				First(&existing).Error; err != nil {
				return err
			}
			if !isCoverage {
				if err := markDeviceSeen(tx, device.ID, receivedAt); err != nil {
					return err
				}
				result.Status = IngestDuplicate
				return nil
			}
			if !existing.ConflictDetected && len(existing.CanonicalCoverageDigest) == len(digest) && subtle.ConstantTimeCompare(existing.CanonicalCoverageDigest, digest) == 1 {
				if err := markDeviceSeen(tx, device.ID, receivedAt); err != nil {
					return err
				}
				result.Status = IngestDuplicate
				return nil
			}
			if !existing.ConflictDetected {
				if err := tx.Model(&domain.TelemetryIngestKey{}).Where("id = ?", existing.ID).Update("conflict_detected", true).Error; err != nil {
					return err
				}
			}
			if err := markDeviceSeen(tx, device.ID, receivedAt); err != nil {
				return err
			}
			result.Status = IngestConflict
			return nil
		}

		if i.afterKeyClaim != nil {
			if err := i.afterKeyClaim(); err != nil {
				return err
			}
		}

		var assignment domain.DeviceAssignment
		if isCoverage {
			start := recordedAt
			end := time.UnixMilli(data.IntervalEndTS.Value).UTC()
			var assignments []domain.DeviceAssignment
			query := tx.Where(
				"device_id = ? AND valid_from <= ? AND (valid_to IS NULL OR ? <= valid_to)",
				device.ID, start, end,
			).Limit(2).Find(&assignments)
			if query.Error != nil {
				return query.Error
			}
			if len(assignments) == 0 {
				result.Status = IngestUnknownAssignment
				return errCoverageAssignmentRollback
			}
			if len(assignments) != 1 {
				return errors.New("coverage assignment is ambiguous")
			}
			assignment = assignments[0]
		} else if err := findAssignmentAt(tx, device.ID, recordedAt, &assignment); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				result.Status = IngestUnknownAssignment
				return errCoverageAssignmentRollback
			}
			return err
		}

		measurementPointID := assignment.MeasurementPointID
		powerFactor := data.PowerFactor
		rssi, validSamples, invalidSamples := data.RSSI, data.ValidSamples, data.InvalidSamples
		reading := domain.PowerReading{
			Time: recordedAt, RecordedAt: recordedAt, ReceivedAt: receivedAt,
			MeasurementPointID: &measurementPointID, DeviceID: device.ID,
			Voltage: data.Voltage, Current: data.Current, Power: data.Power,
			ActivePower: data.Power, KwhTotal: data.KwhTotal, EnergyDeltaKwh: data.EnergyDeltaKwh,
			PowerFactor: &powerFactor, RSSI: &rssi, ProtocolVersion: data.ProtocolVersion,
			BootID: data.BootID, BootCounter: &data.BootCounter, Sequence: &data.Sequence,
			ValidSamples: &validSamples, InvalidSamples: &invalidSamples, FirmwareVersion: data.FirmwareVersion,
		}
		if isCoverage {
			version := data.CoverageVersion.Value
			start := recordedAt
			end := time.UnixMilli(data.IntervalEndTS.Value).UTC()
			reading.CoverageVersion = &version
			reading.IntervalStart = &start
			reading.IntervalEnd = &end
		}
		if err := tx.Create(&reading).Error; err != nil {
			return err
		}
		if err := markDeviceSeen(tx, device.ID, receivedAt); err != nil {
			return err
		}
		alertAt := recordedAt
		alertMeasurementPointID := measurementPointID
		if isCoverage {
			alertAt = time.Unix(data.Timestamp, 0).UTC()
			var alertAssignment domain.DeviceAssignment
			if err := findAssignmentAt(tx, device.ID, alertAt, &alertAssignment); err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				alertMeasurementPointID = uuid.Nil
			} else {
				alertMeasurementPointID = alertAssignment.MeasurementPointID
			}
		}
		if err := checkTelemetryAlerts(tx, device, data, alertAt, &alertMeasurementPointID); err != nil {
			return err
		}
		result.Status = IngestStored
		return nil
	})
	if err != nil {
		if errors.Is(err, errCoverageAssignmentRollback) {
			return result, nil
		}
		return IngestResult{Status: IngestFailed}, err
	}
	return result, nil
}

func energyDeltaValue(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func (i *TelemetryIngestor) loadCoverageMaxInterval(tx *gorm.DB) (int64, error) {
	if i.coverageMaxInterval != nil {
		return i.coverageMaxInterval(tx)
	}
	var raw string
	if err := tx.Raw("SELECT value FROM system_configs WHERE key = ? FOR SHARE", "coverage.max_interval_ms").Row().Scan(&raw); err != nil {
		return 0, fmt.Errorf("coverage max interval configuration unavailable: %w", err)
	}
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < coverage.MinIntervalMilliseconds {
		return 0, errors.New("coverage max interval configuration is invalid")
	}
	return value, nil
}

func findDeviceForUpdate(tx *gorm.DB, mac string, device *domain.Device) error {
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("upper(replace(replace(mac_address, ':', ''), '-', '')) = ?", mac).
		First(device).Error
}

func findAssignmentAt(tx *gorm.DB, deviceID uint, eventTime time.Time, assignment *domain.DeviceAssignment) error {
	var assignments []domain.DeviceAssignment
	query := tx.Where(
		"device_id = ? AND valid_from <= ? AND (valid_to IS NULL OR ? < valid_to)",
		deviceID, eventTime, eventTime,
	).Limit(2).Find(&assignments)
	if query.Error != nil {
		return query.Error
	}
	if len(assignments) == 0 {
		return gorm.ErrRecordNotFound
	}
	if len(assignments) != 1 {
		return errors.New("assignment at event time is ambiguous")
	}
	*assignment = assignments[0]
	return nil
}

func markDeviceSeen(tx *gorm.DB, deviceID uint, receivedAt time.Time) error {
	if err := tx.Model(&domain.Device{}).Where("id = ?", deviceID).Update("is_online", true).Error; err != nil {
		return err
	}
	return tx.Model(&domain.Device{}).
		Where("id = ? AND (last_seen IS NULL OR last_seen < ?)", deviceID, receivedAt).
		Update("last_seen", receivedAt).Error
}

func checkTelemetryAlerts(tx *gorm.DB, device domain.Device, data MqttPayload, eventTime time.Time, measurementPointID *uuid.UUID) error {
	if measurementPointID == nil || *measurementPointID == uuid.Nil {
		// An active legacy policy without an assignment must fail closed rather
		// than fabricate a Device-centered alert identity.
		var legacy domain.DeviceAlertSetting
		if err := tx.First(&legacy, "device_id = ?", device.ID).Error; err == nil && legacy.IsEnabled && legacy.NonUsageStartTime != "" && legacy.NonUsageEndTime != "" && data.Power > 10 {
			return errors.New("telemetry alert measurement point assignment is unavailable")
		}
		return nil
	}
	// Lock the permanent identity before reading/updating lifecycle state. This
	// serializes different replacement devices serving the same MP as well as
	// concurrent deliveries from one device.
	var point domain.MeasurementPoint
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", *measurementPointID).Error; err != nil {
		return err
	}
	var settings domain.MeasurementPointAlertSetting
	settingQuery := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&settings, "measurement_point_id = ?", *measurementPointID)
	if settingQuery.Error != nil {
		// Older isolated compatibility tests predate the protected alert schema.
		// Production always has the MP table after MEASUREMENT_POINT_ALERTS is
		// admitted; this fallback is intentionally unavailable once it exists.
		if isMissingRelation(settingQuery.Error) {
			return checkLegacyTelemetryAlert(tx, device, data, eventTime, measurementPointID)
		}
		if errors.Is(settingQuery.Error, gorm.ErrRecordNotFound) {
			return nil
		}
		return settingQuery.Error
	}
	if !settings.IsEnabled || settings.NonUsageStartTime == "" || settings.NonUsageEndTime == "" {
		return nil
	}
	location, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		return err
	}
	currentHM := eventTime.In(location).Format("15:04")
	inWindow := inCurfewWindow(currentHM, settings.NonUsageStartTime, settings.NonUsageEndTime)
	active := inWindow && data.Power > 10.0

	var state domain.MeasurementPointCurfewState
	stateQuery := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&state, "measurement_point_id = ?", *measurementPointID)
	if stateQuery.Error != nil && !errors.Is(stateQuery.Error, gorm.ErrRecordNotFound) {
		return stateQuery.Error
	}
	if errors.Is(stateQuery.Error, gorm.ErrRecordNotFound) {
		state = domain.MeasurementPointCurfewState{MeasurementPointID: *measurementPointID}
	}
	if state.LastEventAt != nil && !eventTime.After(*state.LastEventAt) {
		return nil
	}
	previous := state.InCurfew
	state.InCurfew = active
	state.LastEventAt = &eventTime
	if err := tx.Save(&state).Error; err != nil {
		return err
	}
	if previous || !active {
		return nil
	}
	alert := domain.AlertLog{
		DeviceID: device.ID, MeasurementPointID: measurementPointID, LegacyUnresolved: false,
		Type:    "CURFEW_USAGE",
		Message: fmt.Sprintf("非營業時間異常運轉 (偵測功率: %.2f W)", data.Power),
		Voltage: data.Voltage, Current: data.Current, Power: data.Power,
		RecordedAt: eventTime, CreatedAt: eventTime, IsRead: false,
	}
	return createAlertLog(tx, alert)
}

func createAlertLog(tx *gorm.DB, alert domain.AlertLog) error {
	if err := tx.Create(&alert).Error; err != nil {
		var pgErr *pq.Error
		if errors.As(err, &pgErr) && string(pgErr.Code) == "42703" {
			return tx.Exec(`INSERT INTO alert_logs (device_id, measurement_point_id, legacy_unresolved, type, message, voltage, current, power, is_read, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, alert.DeviceID, alert.MeasurementPointID, alert.LegacyUnresolved, alert.Type, alert.Message, alert.Voltage, alert.Current, alert.Power, alert.IsRead, alert.CreatedAt).Error
		}
		return err
	}
	return nil
}

func inCurfewWindow(current, start, end string) bool {
	if start > end {
		return current >= start || current <= end
	}
	return current >= start && current <= end
}

// isMissingRelation keeps pre-feature test schemas usable without masking
// ordinary query failures. PostgreSQL's undefined_table is 42P01.
func isMissingRelation(err error) bool {
	var pgErr *pq.Error
	return errors.As(err, &pgErr) && string(pgErr.Code) == "42P01"
}

func checkLegacyTelemetryAlert(tx *gorm.DB, device domain.Device, data MqttPayload, eventTime time.Time, measurementPointID *uuid.UUID) error {
	var settings domain.DeviceAlertSetting
	if err := tx.First(&settings, "device_id = ?", device.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if !settings.IsEnabled || settings.NonUsageStartTime == "" || settings.NonUsageEndTime == "" {
		return nil
	}
	if !inCurfewWindow(eventTime.Format("15:04"), settings.NonUsageStartTime, settings.NonUsageEndTime) || data.Power <= 10 {
		return nil
	}
	alert := domain.AlertLog{DeviceID: device.ID, MeasurementPointID: measurementPointID, LegacyUnresolved: false, Type: "CURFEW_USAGE", Message: fmt.Sprintf("非營業時間異常運轉 (偵測功率: %.2f W)", data.Power), Voltage: data.Voltage, Current: data.Current, Power: data.Power, RecordedAt: eventTime, CreatedAt: eventTime}
	return createAlertLog(tx, alert)
}
