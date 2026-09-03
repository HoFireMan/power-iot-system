package persistence

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrAdminBindingAuditHistoryNotFound               = errors.New("admin binding audit history not found")
	ErrAdminBindingAuditHistoryForbidden              = errors.New("admin binding audit history forbidden")
	ErrAdminBindingAuditHistoryAuthenticationRequired = errors.New("admin binding audit history authentication required")
	ErrInvalidAdminBindingAuditCursor                 = errors.New("invalid admin binding audit cursor")
)

type AdminBindingAuditHistoryQuery struct {
	UserID             uint
	ShopID             uint
	SessionID          uuid.UUID
	Action             string
	MeasurementPointID *uuid.UUID
	DeviceID           *uint
	Limit              int
	Cursor             string
}

type AdminBindingAuditHistoryPage struct {
	Items      []AdminBindingAuditHistoryProjection
	NextCursor string
}

// AdminBindingAuditHistoryProjection contains immutable identifiers/snapshots
// from the audit row and separately resolved current names. Names are display
// enrichment only and never participate in authorization or filtering.
type AdminBindingAuditHistoryProjection struct {
	ID                      uuid.UUID
	OperationID             uuid.UUID
	RequestIdentity         string
	Action                  string
	OccurredAt              time.Time
	EffectiveAt             *time.Time
	Reason                  *string
	ActorID                 uint
	ActorName               string
	ShopID                  *uint
	ShopName                string
	ClientID                uint
	ClientName              string
	MeasurementPointID      *uuid.UUID
	MeasurementPointName    string
	DeviceID                *uint
	DeviceName              string
	DeviceSerialNumber      *string
	DeviceMAC               *string
	OldMeasurementPointID   *uuid.UUID
	OldMeasurementPointName string
	OldShopID               *uint
	OldShopName             string
	NewMeasurementPointID   *uuid.UUID
	NewMeasurementPointName string
	NewShopID               *uint
	NewShopName             string
	OldAssignmentID         *uuid.UUID
	NewAssignmentID         *uuid.UUID
}

type adminBindingAuditCursor struct {
	OccurredAt time.Time `json:"occurredAt"`
	ID         uuid.UUID `json:"id"`
}

func encodeAdminBindingAuditCursor(at time.Time, id uuid.UUID) string {
	body, _ := json.Marshal(adminBindingAuditCursor{OccurredAt: at.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(body)
}

func decodeAdminBindingAuditCursor(raw string) (adminBindingAuditCursor, error) {
	var cursor adminBindingAuditCursor
	body, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(body, &cursor) != nil || cursor.ID == uuid.Nil || cursor.OccurredAt.IsZero() {
		return cursor, ErrInvalidAdminBindingAuditCursor
	}
	return cursor, nil
}

type AdminBindingAuditHistoryRepository struct{ db *gorm.DB }

func NewAdminBindingAuditHistoryRepository(db *gorm.DB) *AdminBindingAuditHistoryRepository {
	return &AdminBindingAuditHistoryRepository{db: db}
}

func (r *AdminBindingOverviewRepository) FindAdminBindingAuditHistory(ctx context.Context, q AdminBindingAuditHistoryQuery) (AdminBindingAuditHistoryPage, error) {
	return (&AdminBindingAuditHistoryRepository{db: r.db}).FindAdminBindingAuditHistory(ctx, q)
}

func (r *AdminBindingAuditHistoryRepository) FindAdminBindingAuditHistory(ctx context.Context, q AdminBindingAuditHistoryQuery) (AdminBindingAuditHistoryPage, error) {
	if r == nil || r.db == nil || q.UserID == 0 || q.ShopID == 0 {
		return AdminBindingAuditHistoryPage{}, ErrAdminBindingAuditHistoryNotFound
	}
	if q.SessionID == uuid.Nil {
		return AdminBindingAuditHistoryPage{}, ErrAdminBindingAuditHistoryAuthenticationRequired
	}
	limit := q.Limit
	if limit < 1 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	var page AdminBindingAuditHistoryPage
	err := r.db.WithContext(queryContext(ctx)).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ").Error; err != nil {
			return err
		}
		var session struct{ UserID uint }
		sessionQuery := tx.Raw(`WITH locked_session AS MATERIALIZED (
			SELECT user_id, expires_at FROM refresh_sessions
			WHERE id = ? AND user_id = ? AND revoked_at IS NULL FOR UPDATE
		) SELECT user_id FROM locked_session WHERE expires_at > clock_timestamp()`, q.SessionID, q.UserID).Scan(&session)
		if sessionQuery.Error != nil {
			return sessionQuery.Error
		}
		if sessionQuery.RowsAffected != 1 {
			return ErrAdminBindingAuditHistoryAuthenticationRequired
		}

		var auth struct {
			IsAdmin  bool
			ClientID uint
		}
		authQuery := tx.Raw(`SELECT u.is_admin, c.id AS client_id
			FROM users u
			JOIN user_shop_relations rel ON rel.user_id=u.id AND rel.shop_id=?
			JOIN shops s ON s.id=rel.shop_id AND s.is_active=TRUE
			JOIN clients c ON c.id=s.client_id
			WHERE u.id=? AND u.auth_enabled=TRUE
			FOR SHARE OF u, rel, s, c`, q.ShopID, q.UserID).Scan(&auth)
		if authQuery.Error != nil {
			return authQuery.Error
		}
		if authQuery.RowsAffected != 1 || auth.ClientID == 0 {
			return ErrAdminBindingAuditHistoryNotFound
		}
		if !auth.IsAdmin {
			return ErrAdminBindingAuditHistoryForbidden
		}

		var cursor adminBindingAuditCursor
		if q.Cursor != "" {
			var err error
			cursor, err = decodeAdminBindingAuditCursor(q.Cursor)
			if err != nil {
				return err
			}
		}

		// Authorization is intentionally expressed using live Shop relations for
		// both ends of a relocation. The audit's stored ClientID is the tenant
		// snapshot; current Shop/Client joins only enrich and validate provenance.
		query := tx.Table("admin_binding_audits a").Select(`
			a.id, a.operation_id, a.request_identity, a.action, a.occurred_at, a.effective_at, a.reason,
			a.actor_id, COALESCE(actor.name, '') AS actor_name,
			a.shop_id, COALESCE(event_shop.name, '') AS shop_name,
			a.client_id, COALESCE(client.name, '') AS client_name,
			a.measurement_point_id, COALESCE(point.name, '') AS measurement_point_name,
			a.device_id, COALESCE(device.name, '') AS device_name,
			a.device_serial_number, a.device_mac,
			a.old_measurement_point_id, COALESCE(old_point.name, '') AS old_measurement_point_name,
			old_point.shop_id AS old_shop_id, COALESCE(old_shop.name, '') AS old_shop_name,
			a.new_measurement_point_id, COALESCE(new_point.name, '') AS new_measurement_point_name,
			new_point.shop_id AS new_shop_id, COALESCE(new_shop.name, '') AS new_shop_name,
			a.old_assignment_id, a.new_assignment_id`).
			Joins("JOIN clients client ON client.id=a.client_id").
			Joins("LEFT JOIN users actor ON actor.id=a.actor_id").
			Joins("JOIN shops event_shop ON event_shop.id=a.shop_id AND event_shop.client_id=a.client_id AND event_shop.is_active=TRUE").
			Joins("LEFT JOIN devices device ON device.id=a.device_id AND device.inventory_owner_client_id=a.client_id").
			Joins("LEFT JOIN measurement_points point ON point.id=a.measurement_point_id").
			Joins("LEFT JOIN measurement_points old_point ON old_point.id=a.old_measurement_point_id").
			Joins("LEFT JOIN shops old_shop ON old_shop.id=old_point.shop_id AND old_shop.client_id=a.client_id").
			Joins("LEFT JOIN measurement_points new_point ON new_point.id=a.new_measurement_point_id").
			Joins("LEFT JOIN shops new_shop ON new_shop.id=new_point.shop_id AND new_shop.client_id=a.client_id").
			Joins(`JOIN shops requested_shop ON requested_shop.id=? AND requested_shop.client_id=a.client_id AND requested_shop.is_active=TRUE`, q.ShopID).
			Where(`a.client_id=? AND a.shop_id=? AND a.action IN ('create_measurement_point', 'bind', 'replace', 'relocate', 'unbind') AND
				(a.measurement_point_id IS NULL OR EXISTS (SELECT 1 FROM measurement_points p JOIN shops ps ON ps.id=p.shop_id WHERE p.id=a.measurement_point_id AND ps.client_id=a.client_id)) AND
				(a.old_measurement_point_id IS NULL OR EXISTS (SELECT 1 FROM measurement_points p JOIN shops ps ON ps.id=p.shop_id WHERE p.id=a.old_measurement_point_id AND ps.client_id=a.client_id)) AND
				(a.new_measurement_point_id IS NULL OR EXISTS (SELECT 1 FROM measurement_points p JOIN shops ps ON ps.id=p.shop_id WHERE p.id=a.new_measurement_point_id AND ps.client_id=a.client_id)) AND
				(a.action <> 'relocate' OR (
					EXISTS (SELECT 1 FROM user_shop_relations rr JOIN shops rs ON rs.id=rr.shop_id AND rs.is_active=TRUE JOIN measurement_points rp ON rp.shop_id=rs.id WHERE rr.user_id=? AND rp.id=a.old_measurement_point_id AND rs.client_id=a.client_id)
					AND EXISTS (SELECT 1 FROM user_shop_relations rr JOIN shops rs ON rs.id=rr.shop_id AND rs.is_active=TRUE JOIN measurement_points rp ON rp.shop_id=rs.id WHERE rr.user_id=? AND rp.id=a.new_measurement_point_id AND rs.client_id=a.client_id)
				)
			)`, auth.ClientID, q.ShopID, q.UserID, q.UserID)
		if q.Action != "" {
			query = query.Where("a.action = ?", q.Action)
		}
		if q.MeasurementPointID != nil {
			query = query.Where("(? IN (a.measurement_point_id, a.old_measurement_point_id, a.new_measurement_point_id))", *q.MeasurementPointID)
		}
		if q.DeviceID != nil {
			query = query.Where("a.device_id=?", *q.DeviceID)
		}
		if q.Cursor != "" {
			query = query.Where("(a.occurred_at, a.id) < (?, ?)", cursor.OccurredAt.UTC(), cursor.ID)
		}
		var rows []AdminBindingAuditHistoryProjection
		if err := query.Order("a.occurred_at DESC, a.id DESC").Limit(limit + 1).Scan(&rows).Error; err != nil {
			return err
		}
		page.Items = rows
		if len(rows) > limit {
			page.Items = rows[:limit]
			last := page.Items[len(page.Items)-1]
			page.NextCursor = encodeAdminBindingAuditCursor(last.OccurredAt, last.ID)
		}
		if page.Items == nil {
			page.Items = []AdminBindingAuditHistoryProjection{}
		}
		return nil
	})
	return page, err
}
