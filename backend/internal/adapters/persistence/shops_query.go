package persistence

import (
	"context"
	"database/sql"
	"errors"

	"gorm.io/gorm"
	corebilling "power-iot-backend/internal/core/billing"
)

// ShopProjection is the safe shop-list projection. It contains only fields
// permitted by GET /api/v1/shops; relation and user metadata are not exposed.
type ShopProjection struct {
	ID                uint
	Code              string
	Name              string
	Address           *string
	Phone             *string
	IsHead            bool
	ElectricityTariff *string
}

// AuthorizedShopsQuery is the read-only B6-B persistence capability.
type AuthorizedShopsQuery interface {
	FindAuthorizedShops(context.Context, uint) ([]ShopProjection, *uint, error)
}

// UserQueryRepository (the accepted B6-A read seam) uses the authenticated
// user's relation rows as the sole authority. IsAdmin, ClientID, and an
// un-related CurrentShopID cannot grant access. The id ASC ordering is the
// repository's deterministic list convention; the unique (user_id, shop_id)
// constraint prevents duplicates.
type UserShopQueryRepository = UserQueryRepository

var _ AuthorizedShopsQuery = (*UserQueryRepository)(nil)

func NewUserShopQueryRepository(db *gorm.DB) *UserShopQueryRepository {
	return NewUserQueryRepository(db)
}

// FindAuthorizedShops performs one parameterized, read-only query. The
// current-shop preference is returned as metadata and is retained by the
// application only when it matches one of the active authorized shops.
func (r *UserQueryRepository) FindAuthorizedShops(ctx context.Context, userID uint) ([]ShopProjection, *uint, error) {
	if r == nil || r.db == nil {
		return nil, nil, gorm.ErrInvalidDB
	}
	var rows []struct {
		ID                uint
		Code              string
		Name              string
		Address           sql.NullString
		Phone             sql.NullString
		IsHead            bool
		ElectricityTariff sql.NullString
		CurrentShopID     sql.NullInt64
	}
	query := `
SELECT DISTINCT s.id, s.code, s.name, s.address, s.phone, s.is_head, s.electricity_tariff,
       CASE WHEN s.id = u.current_shop_id THEN u.current_shop_id ELSE NULL END AS current_shop_id
FROM users AS u
JOIN user_shop_relations AS relation
  ON relation.user_id = u.id
JOIN shops AS s
  ON s.id = relation.shop_id
 AND s.is_active = TRUE
WHERE u.id = ?
ORDER BY s.id ASC`
	result := r.db.WithContext(queryContext(ctx)).Raw(query, userID).Scan(&rows)
	if result.Error != nil {
		return nil, nil, result.Error
	}
	shops := make([]ShopProjection, 0, len(rows))
	var current *uint
	for _, row := range rows {
		shops = append(shops, ShopProjection{
			ID: row.ID, Code: row.Code, Name: row.Name,
			Address: shopNullableString(row.Address), Phone: shopNullableString(row.Phone), IsHead: row.IsHead,
			ElectricityTariff: shopNullableString(row.ElectricityTariff),
		})
		if row.CurrentShopID.Valid && row.CurrentShopID.Int64 > 0 {
			id := uint(row.CurrentShopID.Int64)
			if int64(id) == row.CurrentShopID.Int64 {
				current = &id
			}
		}
	}
	return shops, current, nil
}

// ShopMutationRepository is the narrow mutation capability consumed by HTTP.
type ShopMutationRepository struct{ db *gorm.DB }

func NewShopMutationRepository(db *gorm.DB) *ShopMutationRepository {
	return &ShopMutationRepository{db: db}
}

// UpdateShopTariff is one transactional, idempotent mutation. Authorization
// is evaluated from the active Shop, explicit relation, and users.is_admin;
// no current-shop or global-admin shortcut is accepted.
func (r *ShopMutationRepository) UpdateShopTariff(ctx context.Context, actorID, shopID uint, tariff string) error {
	if r == nil || r.db == nil {
		return gorm.ErrInvalidDB
	}
	return r.db.WithContext(queryContext(ctx)).Transaction(func(tx *gorm.DB) error {
		var current sql.NullString
		if err := tx.Raw(`
SELECT s.electricity_tariff
FROM shops AS s
JOIN users AS actor ON actor.id = ? AND actor.is_admin = TRUE
JOIN user_shop_relations AS relation
  ON relation.user_id = actor.id AND relation.shop_id = s.id
WHERE s.id = ? AND s.is_active = TRUE
FOR UPDATE OF s`, actorID, shopID).Row().Scan(&current); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return gorm.ErrRecordNotFound
			}
			return err
		}
		var history int64
		if err := tx.Raw(`SELECT count(*) FROM shop_billing_assignments WHERE shop_id = ?`, shopID).Row().Scan(&history); err != nil {
			return err
		}
		if history > 0 && (!current.Valid || current.String != tariff) {
			return corebilling.ErrBillingHistoryConflict
		}
		result := tx.Exec(`
UPDATE shops AS shop
SET electricity_tariff = ?
FROM users AS actor
JOIN user_shop_relations AS relation
  ON relation.user_id = actor.id
WHERE relation.shop_id = shop.id
  AND shop.id = ? AND shop.is_active = TRUE AND actor.id = ? AND actor.is_admin = TRUE`, tariff, shopID, actorID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

func shopNullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	out := value.String
	return &out
}
