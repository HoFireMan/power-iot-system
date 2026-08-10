package iot

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/data/migrations"
)

// IngestResultStatus is the semantic outcome of a telemetry transaction.
type IngestResultStatus string

const (
	IngestStored            IngestResultStatus = "stored"
	IngestDuplicate         IngestResultStatus = "duplicate"
	IngestUnknownDevice     IngestResultStatus = "unknown_device"
	IngestUnknownAssignment IngestResultStatus = "unknown_assignment"
	IngestInvalid           IngestResultStatus = "invalid"
	IngestFailed            IngestResultStatus = "failed"
)

// IngestResult is deliberately independent of GORM, SQL, and TimescaleDB.
type IngestResult struct {
	Status IngestResultStatus
}

// TelemetryIngestor owns the transactional identity, attribution, and
// persistence semantics for Protocol v1 telemetry. MQTT transport only needs
// to provide a validated payload and the original broker ingress time.
type TelemetryIngestor struct {
	db               *gorm.DB
	afterKeyClaim    func() error
	beforeDeviceLock func(*gorm.DB) error
	afterDeviceLock  func() error
}

func NewTelemetryIngestor(db *gorm.DB) *TelemetryIngestor {
	return &TelemetryIngestor{db: db}
}

// Ingest resolves historical attribution and atomically claims the protocol
// identity, writes the reading, updates device communication state, and runs
// existing transactional alert work. Terminal ACK mapping remains outside this
// method and therefore outside the database transaction callback.
func (i *TelemetryIngestor) Ingest(data MqttPayload, receivedAt time.Time) (IngestResult, error) {
	return i.IngestContext(context.Background(), data, receivedAt)
}

func (i *TelemetryIngestor) IngestContext(ctx context.Context, data MqttPayload, receivedAt time.Time) (IngestResult, error) {
	if ctx == nil {
		ctx = context.Background()
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
	recordedAt := telemetryTimeAt(data.Timestamp, receivedAt)
	result := IngestResult{Status: IngestFailed}

	err = i.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The shared fence is the first application/database operation. Device
		// serialization and every identity/assignment query follow admission.
		if err := migrations.AcquireSharedWriterFenceOnGORM(ctx, tx); err != nil {
			return err
		}
		var device domain.Device
		if i.beforeDeviceLock != nil {
			if err := i.beforeDeviceLock(tx); err != nil {
				return err
			}
		}
		// Device is the shared serialization row. It must be locked before any
		// assignment-dependent historical lookup; recordedAt remains the
		// ingress-captured payload time while this transaction may wait.
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

		var assignment domain.DeviceAssignment
		assignmentQuery := tx.Where(
			"device_id = ? AND valid_from <= ? AND (valid_to IS NULL OR ? < valid_to)",
			device.ID, recordedAt, recordedAt,
		).Order("valid_from DESC").First(&assignment)
		if errors.Is(assignmentQuery.Error, gorm.ErrRecordNotFound) {
			result.Status = IngestUnknownAssignment
			return nil
		}
		if assignmentQuery.Error != nil {
			return assignmentQuery.Error
		}

		bootCounter, sequence := data.BootCounter, data.Sequence
		key := domain.TelemetryIngestKey{
			ID:          uuid.New(),
			DeviceID:    device.ID,
			BootCounter: bootCounter,
			Sequence:    sequence,
			ReceivedAt:  receivedAt,
		}
		keyInsert := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "device_id"},
				{Name: "boot_counter"},
				{Name: "sequence"},
			},
			DoNothing: true,
		}).Create(&key)
		if keyInsert.Error != nil {
			return keyInsert.Error
		}

		if keyInsert.RowsAffected == 0 {
			if err := markDeviceSeen(tx, device.ID, receivedAt); err != nil {
				return err
			}
			result.Status = IngestDuplicate
			return nil
		}
		if i.afterKeyClaim != nil {
			if err := i.afterKeyClaim(); err != nil {
				return err
			}
		}

		measurementPointID := assignment.MeasurementPointID
		powerFactor, energyDelta := data.PowerFactor, data.EnergyDeltaKwh
		rssi, validSamples, invalidSamples := data.RSSI, data.ValidSamples, data.InvalidSamples
		reading := domain.PowerReading{
			Time:               recordedAt,
			RecordedAt:         recordedAt,
			ReceivedAt:         receivedAt,
			MeasurementPointID: &measurementPointID,
			DeviceID:           device.ID,
			Voltage:            data.Voltage,
			Current:            data.Current,
			Power:              data.Power,
			ActivePower:        data.Power,
			KwhTotal:           data.KwhTotal,
			EnergyDeltaKwh:     &energyDelta,
			PowerFactor:        &powerFactor,
			RSSI:               &rssi,
			ProtocolVersion:    data.ProtocolVersion,
			BootID:             data.BootID,
			BootCounter:        &bootCounter,
			Sequence:           &sequence,
			ValidSamples:       &validSamples,
			InvalidSamples:     &invalidSamples,
			FirmwareVersion:    data.FirmwareVersion,
		}
		if err := tx.Create(&reading).Error; err != nil {
			return err
		}
		if err := markDeviceSeen(tx, device.ID, receivedAt); err != nil {
			return err
		}
		if err := checkTelemetryAlerts(tx, device, data, recordedAt); err != nil {
			return err
		}
		result.Status = IngestStored
		return nil
	})
	if err != nil {
		return IngestResult{Status: IngestFailed}, err
	}
	return result, nil
}

func findDeviceForUpdate(tx *gorm.DB, mac string, device *domain.Device) error {
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Preload("AlertSettings").
		Where("upper(replace(replace(mac_address, ':', ''), '-', '')) = ?", mac).
		First(device).Error
}

func markDeviceSeen(tx *gorm.DB, deviceID uint, receivedAt time.Time) error {
	if err := tx.Model(&domain.Device{}).Where("id = ?", deviceID).Update("is_online", true).Error; err != nil {
		return err
	}
	return tx.Model(&domain.Device{}).
		Where("id = ? AND (last_seen IS NULL OR last_seen < ?)", deviceID, receivedAt).
		Update("last_seen", receivedAt).Error
}

func checkTelemetryAlerts(tx *gorm.DB, device domain.Device, data MqttPayload, recordedAt time.Time) error {
	settings := device.AlertSettings
	if settings.ID == 0 || !settings.IsEnabled {
		return nil
	}
	if settings.NonUsageStartTime == "" || settings.NonUsageEndTime == "" {
		return nil
	}
	currentHM := recordedAt.Format("15:04")
	inRange := false
	if settings.NonUsageStartTime > settings.NonUsageEndTime {
		inRange = currentHM >= settings.NonUsageStartTime || currentHM <= settings.NonUsageEndTime
	} else {
		inRange = currentHM >= settings.NonUsageStartTime && currentHM <= settings.NonUsageEndTime
	}
	if !inRange || data.Power <= 10.0 {
		return nil
	}
	alert := domain.AlertLog{
		DeviceID:  device.ID,
		Type:      "CURFEW_USAGE",
		Message:   fmt.Sprintf("非營業時間異常運轉 (偵測功率: %.2f W)", data.Power),
		Voltage:   data.Voltage,
		Current:   data.Current,
		Power:     data.Power,
		CreatedAt: recordedAt,
		IsRead:    false,
	}
	return tx.Create(&alert).Error
}
