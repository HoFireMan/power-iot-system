// Package persistence contains narrow database capabilities used by domain
// needs. It intentionally does not expose a generic repository or own a
// transaction; callers pass the transaction that must include their mutation,
// audit, and idempotency rows.
package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"power-iot-backend/internal/core/domain"
)

var (
	ErrIdempotencyKeyReused      = errors.New("idempotency key reused with a different canonical request")
	ErrOperationAlreadyCommitted = errors.New("idempotency operation is already committed")
)

// AppendAdminBindingAudit appends one audit fact to the caller's transaction.
// It never commits independently. The database trigger is the final ordinary
// UPDATE/DELETE guard for the immutable audit table.
func AppendAdminBindingAudit(tx *gorm.DB, audit *domain.AdminBindingAudit) error {
	if tx == nil {
		return errors.New("database transaction is required")
	}
	if audit == nil {
		return errors.New("audit is required")
	}
	if audit.ID == uuid.Nil {
		audit.ID = uuid.New()
	}
	if audit.OccurredAt.IsZero() {
		audit.OccurredAt = time.Now().UTC()
	}
	var err error
	if audit.ScopeSnapshot, err = normalizeJSON(audit.ScopeSnapshot); err != nil {
		return fmt.Errorf("scope snapshot: %w", err)
	}
	if audit.Metadata, err = normalizeJSON(audit.Metadata); err != nil {
		return fmt.Errorf("audit metadata: %w", err)
	}
	return tx.Create(audit).Error
}

// ClaimAdminBindingOperation inserts an operation using the database unique
// guard. claimed is true only when this transaction inserted the row. A false
// result returns the committed or in-flight existing row for replay handling.
func ClaimAdminBindingOperation(tx *gorm.DB, operation *domain.AdminBindingOperation) (existing domain.AdminBindingOperation, claimed bool, err error) {
	if tx == nil {
		return existing, false, errors.New("database transaction is required")
	}
	if operation == nil {
		return existing, false, errors.New("operation is required")
	}
	if len(operation.CanonicalRequestHash) != 32 {
		return existing, false, errors.New("canonical request hash must be SHA-256")
	}
	if operation.ID == uuid.Nil {
		operation.ID = uuid.New()
	}
	if operation.OperationID == uuid.Nil {
		operation.OperationID = uuid.New()
	}
	operation.ScopeSnapshot, err = normalizeJSON(operation.ScopeSnapshot)
	if err != nil {
		return existing, false, fmt.Errorf("scope snapshot: %w", err)
	}
	if operation.CreatedAt.IsZero() {
		operation.CreatedAt = time.Now().UTC()
	}

	result := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "actor_id"},
			{Name: "scope_key"},
			{Name: "operation"},
			{Name: "idempotency_key"},
		},
		DoNothing: true,
	}).Create(operation)
	if result.Error != nil {
		return existing, false, result.Error
	}
	if result.RowsAffected == 1 {
		return *operation, true, nil
	}

	query := tx.Where(
		"actor_id = ? AND scope_key = ? AND operation = ? AND idempotency_key = ?",
		operation.ActorID, operation.ScopeKey, operation.Operation, operation.IdempotencyKey,
	).First(&existing)
	if query.Error != nil {
		return existing, false, query.Error
	}
	if !bytes.Equal(existing.CanonicalRequestHash, operation.CanonicalRequestHash) {
		return existing, false, ErrIdempotencyKeyReused
	}
	return existing, false, nil
}

// LoadAdminBindingOperation reloads one scoped operation without changing it.
func LoadAdminBindingOperation(tx *gorm.DB, actorID uint, scopeKey, operationName, idempotencyKey string) (domain.AdminBindingOperation, error) {
	var operation domain.AdminBindingOperation
	if tx == nil {
		return operation, errors.New("database transaction is required")
	}
	err := tx.Where(
		"actor_id = ? AND scope_key = ? AND operation = ? AND idempotency_key = ?",
		actorID, scopeKey, operationName, idempotencyKey,
	).First(&operation).Error
	return operation, err
}

// CommitAdminBindingOperation records the semantic response only after the
// caller has persisted the business mutation and audit in the same transaction.
func CommitAdminBindingOperation(tx *gorm.DB, operationID uuid.UUID, response json.RawMessage, committedAt time.Time) error {
	if tx == nil {
		return errors.New("database transaction is required")
	}
	if operationID == uuid.Nil {
		return errors.New("operation ID is required")
	}
	if !json.Valid(response) {
		return errors.New("committed response must be valid JSON")
	}
	if committedAt.IsZero() {
		committedAt = time.Now().UTC()
	} else {
		committedAt = committedAt.UTC()
	}

	result := tx.Model(&domain.AdminBindingOperation{}).
		Where("operation_id = ? AND committed_at IS NULL", operationID).
		Updates(map[string]interface{}{
			"committed_response": gorm.Expr("?::jsonb", string(response)),
			"committed_at":       committedAt,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var existing domain.AdminBindingOperation
	if err := tx.Where("operation_id = ?", operationID).First(&existing).Error; err != nil {
		return err
	}
	return ErrOperationAlreadyCommitted
}

// HasCommittedTelemetryConflict checks TIME-01 for one current assignment.
// The assignment interval predicates are included so a reading from an older
// assignment of the same Device and Measurement Point cannot be misclassified.
func HasCommittedTelemetryConflict(tx *gorm.DB, assignmentID uuid.UUID, candidate time.Time) (bool, error) {
	if tx == nil {
		return false, errors.New("database transaction is required")
	}
	if assignmentID == uuid.Nil {
		return false, errors.New("assignment ID is required")
	}
	var conflict bool
	err := tx.Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM device_assignments assignment
			JOIN power_readings reading
			  ON reading.device_id = assignment.device_id
			 AND reading.measurement_point_id = assignment.measurement_point_id
			 AND reading.recorded_at >= assignment.valid_from
			 AND (assignment.valid_to IS NULL OR reading.recorded_at < assignment.valid_to)
			 AND reading.recorded_at >= ?
			WHERE assignment.id = ?
			  AND assignment.valid_to IS NULL
		)`, candidate.UTC(), assignmentID).Scan(&conflict).Error
	return conflict, err
}

// TransactionLookup is the concrete PostgreSQL lookup seam used by the
// Admin Binding executor. It is deliberately bound to a caller-owned handle;
// it never begins or commits a transaction.
type TransactionLookup struct {
	tx *gorm.DB
}

func NewTransactionLookup(tx *gorm.DB) *TransactionLookup { return &TransactionLookup{tx: tx} }

func (l *TransactionLookup) FindShop(_ context.Context, id uint) (*domain.Shop, error) {
	var value domain.Shop
	if l == nil || l.tx == nil {
		return nil, errors.New("database transaction is required")
	}
	if err := l.tx.First(&value, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &value, nil
}

func (l *TransactionLookup) FindMeasurementPoint(_ context.Context, id uuid.UUID) (*domain.MeasurementPoint, error) {
	var value domain.MeasurementPoint
	if l == nil || l.tx == nil {
		return nil, errors.New("database transaction is required")
	}
	if err := l.tx.First(&value, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &value, nil
}

func (l *TransactionLookup) FindDeviceByID(ctx context.Context, id uint) (*domain.Device, error) {
	return l.findDevice(ctx, "id = ?", id)
}

func (l *TransactionLookup) FindDeviceBySerial(ctx context.Context, serial string) (*domain.Device, error) {
	return l.findDevice(ctx, "serial_number = ?", serial)
}

func (l *TransactionLookup) FindDeviceByMAC(ctx context.Context, mac string) (*domain.Device, error) {
	return l.findDevice(ctx, "mac_address = ?", mac)
}

func (l *TransactionLookup) findDevice(_ context.Context, query string, args ...interface{}) (*domain.Device, error) {
	var value domain.Device
	if l == nil || l.tx == nil {
		return nil, errors.New("database transaction is required")
	}
	if err := l.tx.Where(query, args...).First(&value).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &value, nil
}

func (l *TransactionLookup) FindAssignment(_ context.Context, id uuid.UUID) (*domain.DeviceAssignment, error) {
	var value domain.DeviceAssignment
	if l == nil || l.tx == nil {
		return nil, errors.New("database transaction is required")
	}
	if err := l.tx.First(&value, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &value, nil
}

func (l *TransactionLookup) FindActiveAssignmentByDevice(_ context.Context, id uint) (*domain.DeviceAssignment, error) {
	return l.findActive("device_id = ?", id)
}

func (l *TransactionLookup) FindActiveAssignmentByMeasurementPoint(_ context.Context, id uuid.UUID) (*domain.DeviceAssignment, error) {
	return l.findActive("measurement_point_id = ?", id)
}

func (l *TransactionLookup) findActive(query string, args ...interface{}) (*domain.DeviceAssignment, error) {
	var value domain.DeviceAssignment
	if l == nil || l.tx == nil {
		return nil, errors.New("database transaction is required")
	}
	if err := l.tx.Where(query+" AND valid_to IS NULL", args...).Order("valid_from DESC").First(&value).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &value, nil
}

// LockDevicesForUpdate acquires Device serialization rows in ascending ID
// order. Missing rows are reported instead of silently shrinking the lock set.
func LockDevicesForUpdate(tx *gorm.DB, ids []uint) error {
	ids = uniqueSortedUint(ids)
	for _, id := range ids {
		var device domain.Device
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&device, id)
		if result.Error != nil {
			return result.Error
		}
	}
	return nil
}

// LockMeasurementPointsForUpdate acquires MP rows in PostgreSQL UUID order.
func LockMeasurementPointsForUpdate(tx *gorm.DB, ids []uuid.UUID) error {
	ids = uniqueSortedUUID(ids)
	for _, id := range ids {
		var point domain.MeasurementPoint
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", id)
		if result.Error != nil {
			return result.Error
		}
	}
	return nil
}

// LockAssignmentsForUpdate acquires explicit current-assignment rows in UUID
// order, after all Device and MP locks have been taken.
func LockAssignmentsForUpdate(tx *gorm.DB, ids []uuid.UUID) error {
	ids = uniqueSortedUUID(ids)
	for _, id := range ids {
		var assignment domain.DeviceAssignment
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&assignment, "id = ?", id)
		if result.Error != nil {
			return result.Error
		}
	}
	return nil
}

// DatabaseTransitionTime samples PostgreSQL wall-clock time. Unlike
// transaction_timestamp/current_timestamp, clock_timestamp reflects time after
// any preceding lock wait in the caller-owned transaction.
func DatabaseTransitionTime(tx *gorm.DB) (time.Time, error) {
	var value time.Time
	if err := tx.Raw("SELECT clock_timestamp()").Scan(&value).Error; err != nil {
		return time.Time{}, err
	}
	return value.UTC(), nil
}

func uniqueSortedUint(values []uint) []uint {
	seen := make(map[uint]struct{}, len(values))
	result := make([]uint, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func uniqueSortedUUID(values []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(values))
	result := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		if value == uuid.Nil {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result
}

func normalizeJSON(value json.RawMessage) (json.RawMessage, error) {
	if len(value) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(value) {
		return nil, errors.New("must be valid JSON")
	}
	return value, nil
}
