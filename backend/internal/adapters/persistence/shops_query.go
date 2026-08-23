package persistence

import (
	"context"
	"database/sql"

	"gorm.io/gorm"
)

// ShopProjection is the safe shop-list projection. It contains only fields
// permitted by GET /api/v1/shops; relation and user metadata are not exposed.
type ShopProjection struct {
	ID      uint
	Code    string
	Name    string
	Address *string
	Phone   *string
	IsHead  bool
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
		ID            uint
		Code          string
		Name          string
		Address       sql.NullString
		Phone         sql.NullString
		IsHead        bool
		CurrentShopID sql.NullInt64
	}
	query := `
SELECT DISTINCT s.id, s.code, s.name, s.address, s.phone, s.is_head,
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

func shopNullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	out := value.String
	return &out
}
