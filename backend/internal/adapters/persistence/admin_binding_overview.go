package persistence

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrAdminBindingOverviewNotFound               = errors.New("admin binding overview not found")
	ErrAdminBindingOverviewForbidden              = errors.New("admin binding overview forbidden")
	ErrAdminBindingOverviewAuthenticationRequired = errors.New("admin binding overview authentication required")
)

type AdminBindingOverview struct {
	MeasurementPoints []AdminMeasurementPointProjection
	Devices           []AdminDeviceProjection
	ActiveAssignments []AdminAssignmentProjection
	AssignmentHistory []AdminAssignmentProjection
}
type AdminMeasurementPointProjection struct {
	ID     uuid.UUID
	ShopID uint
	Name   string
}
type AdminDeviceProjection struct {
	ID                                     uint
	Name, SerialNumber, MACAddress, Status string
	LifecycleStatus                        string
}
type AdminAssignmentProjection struct {
	ID                 uuid.UUID
	DeviceID           uint
	MeasurementPointID uuid.UUID
	ValidFrom          time.Time
	ValidTo            *time.Time
}

// AdminBindingOverviewRepository is the narrow read-only capability used by
// the admin HTTP adapter.
type AdminBindingOverviewRepository struct{ db *gorm.DB }

func NewAdminBindingOverviewRepository(db *gorm.DB) *AdminBindingOverviewRepository {
	return &AdminBindingOverviewRepository{db: db}
}

var _ interface {
	FindAdminBindingOverview(context.Context, uint, uint) (AdminBindingOverview, error)
} = (*AdminBindingOverviewRepository)(nil)
var _ interface {
	FindAdminBindingOverviewForSession(context.Context, uint, uint, uuid.UUID) (AdminBindingOverview, error)
} = (*AdminBindingOverviewRepository)(nil)

// FindAdminBindingOverview is retained for non-HTTP callers. HTTP callers must
// use FindAdminBindingOverviewForSession so the authenticated session is
// revalidated in the overview transaction.
func (r *AdminBindingOverviewRepository) FindAdminBindingOverview(ctx context.Context, userID, shopID uint) (AdminBindingOverview, error) {
	return r.findAdminBindingOverview(ctx, userID, shopID, uuid.Nil)
}

// FindAdminBindingOverviewForSession is the HTTP-safe overview capability. It
// locks and revalidates the authenticated refresh session in the same
// repeatable-read transaction as authorization and projection reads.
func (r *AdminBindingOverviewRepository) FindAdminBindingOverviewForSession(ctx context.Context, userID, shopID uint, sessionID uuid.UUID) (AdminBindingOverview, error) {
	if sessionID == uuid.Nil {
		return AdminBindingOverview{}, ErrAdminBindingOverviewAuthenticationRequired
	}
	return r.findAdminBindingOverview(ctx, userID, shopID, sessionID)
}

// findAdminBindingOverview is a read-only, scoped projection. The relation
// and Shop->Client joins are the authority; compatibility Device.ShopID is not
// used.
func (r *AdminBindingOverviewRepository) findAdminBindingOverview(ctx context.Context, userID, shopID uint, sessionID uuid.UUID) (out AdminBindingOverview, err error) {
	if r == nil || r.db == nil || userID == 0 || shopID == 0 {
		return AdminBindingOverview{}, ErrAdminBindingOverviewNotFound
	}
	// READ COMMITTED would give each SELECT a different snapshot. Repeatable
	// read makes the authorization, inventory, and assignment projection one
	// coherent view while FOR SHARE protects the authorization facts themselves.
	err = r.db.WithContext(queryContext(ctx)).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ").Error; err != nil {
			return err
		}
		if sessionID != uuid.Nil {
			// MATERIALIZED ensures the row lock is acquired before the volatile
			// expiry check. Thus clock_timestamp() is evaluated after any wait
			// for a concurrent session revocation/update to finish.
			var session struct{ UserID uint }
			query := tx.Raw(`WITH locked_session AS MATERIALIZED (
				SELECT user_id, expires_at
				FROM refresh_sessions
				WHERE id = ? AND user_id = ? AND revoked_at IS NULL
				FOR UPDATE
			)
			SELECT user_id FROM locked_session
			WHERE expires_at > clock_timestamp()`, sessionID, userID).Scan(&session)
			if query.Error != nil {
				return query.Error
			}
			if query.RowsAffected != 1 {
				return ErrAdminBindingOverviewAuthenticationRequired
			}
		}
		var auth struct {
			IsAdmin  bool
			ClientID uint
		}
		query := tx.Raw(`SELECT u.is_admin, c.id AS client_id
			FROM users u
			JOIN user_shop_relations rel ON rel.user_id = u.id AND rel.shop_id = ?
			JOIN shops s ON s.id = rel.shop_id AND s.is_active = TRUE
			JOIN clients c ON c.id = s.client_id
			WHERE u.id = ? AND u.auth_enabled = TRUE
			FOR SHARE OF u, rel, s, c`, shopID, userID).Scan(&auth)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected != 1 || auth.ClientID == 0 {
			return ErrAdminBindingOverviewNotFound
		}
		if !auth.IsAdmin {
			return ErrAdminBindingOverviewForbidden
		}
		out = AdminBindingOverview{MeasurementPoints: []AdminMeasurementPointProjection{}, Devices: []AdminDeviceProjection{}, ActiveAssignments: []AdminAssignmentProjection{}}
		var points []AdminMeasurementPointProjection
		if err := tx.Raw(`SELECT id, shop_id, name FROM measurement_points WHERE shop_id = ? ORDER BY id`, shopID).Scan(&points).Error; err != nil {
			return err
		}
		out.MeasurementPoints = points
		var devices []struct {
			ID              uint
			Name            string
			SerialNumber    sql.NullString
			MACAddress      string
			IsOnline        bool
			LifecycleStatus string
		}
		// Device.ShopID is deliberately absent: inventory is scoped by the
		// authoritative Client owner, which is the approved admin model.
		if err := tx.Raw(`SELECT id, name, serial_number, mac_address, is_online, lifecycle_status
			FROM devices WHERE inventory_owner_client_id = ? ORDER BY id`, auth.ClientID).Scan(&devices).Error; err != nil {
			return err
		}
		for _, d := range devices {
			status := "Offline"
			if d.IsOnline {
				status = "Online"
			}
			serial := ""
			if d.SerialNumber.Valid {
				serial = d.SerialNumber.String
			}
			out.Devices = append(out.Devices, AdminDeviceProjection{ID: d.ID, Name: d.Name, SerialNumber: serial, MACAddress: d.MACAddress, Status: status, LifecycleStatus: d.LifecycleStatus})
		}
		var assignments []AdminAssignmentProjection
		if err := tx.Raw(`SELECT a.id, a.device_id, a.measurement_point_id, a.valid_from, a.valid_to
			FROM device_assignments a
			JOIN measurement_points p ON p.id = a.measurement_point_id
			JOIN devices d ON d.id = a.device_id AND d.inventory_owner_client_id = ?
			WHERE p.shop_id = ? ORDER BY a.valid_from DESC, a.id DESC`, auth.ClientID, shopID).Scan(&assignments).Error; err != nil {
			return err
		}
		out.AssignmentHistory = assignments
		for _, a := range assignments {
			if a.ValidTo == nil {
				out.ActiveAssignments = append(out.ActiveAssignments, a)
			}
		}
		return nil
	})
	return out, err
}
