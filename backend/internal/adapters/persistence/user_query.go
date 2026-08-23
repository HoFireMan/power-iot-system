package persistence

import (
	"context"
	"database/sql"
	"errors"

	"gorm.io/gorm"
)

// ErrUserQueryRecordNotFound indicates that the authenticated user no longer
// exists. It is intentionally separate from the authentication persistence
// errors so the read-only B6 capability cannot alter B1-B5 semantics.
var ErrUserQueryRecordNotFound = errors.New("user query record not found")

// UserProfileProjection is the safe read projection used by GET /api/v1/me.
// It contains neither credentials nor authentication/session state. A current
// shop is populated only when the preference has both a relation row and an
// active shop row.
type UserProfileProjection struct {
	ID            uint
	Account       string
	Name          string
	Email         *string
	Phone         *string
	IsAdmin       bool
	CurrentShopID *uint
}

// UserQueryPersistence is the read-only B6 persistence capability.
type UserQueryPersistence interface {
	FindUserProfile(context.Context, uint) (UserProfileProjection, error)
}

// UserQueryRepository is deliberately not an AuthRepository. It performs no
// writer-fence admission and exposes only the safe profile projection.
type UserQueryRepository struct {
	db *gorm.DB
}

var _ UserQueryPersistence = (*UserQueryRepository)(nil)

func NewUserQueryRepository(db *gorm.DB) *UserQueryRepository {
	return &UserQueryRepository{db: db}
}

// FindUserProfile uses one parameterized read. Relation existence and shop
// activity are the sole authority for exposing current_shop_id; no fallback
// shop is selected and no data is changed.
func (r *UserQueryRepository) FindUserProfile(ctx context.Context, userID uint) (UserProfileProjection, error) {
	if r == nil || r.db == nil || userID == 0 {
		return UserProfileProjection{}, ErrUserQueryRecordNotFound
	}
	var row struct {
		ID            uint
		Account       string
		Name          string
		Email         sql.NullString
		Phone         sql.NullString
		IsAdmin       bool
		CurrentShopID sql.NullInt64
	}
	query := `
SELECT u.id, u.account, u.name, u.email, u.phone, u.is_admin,
       active_shop.id AS current_shop_id
FROM users AS u
LEFT JOIN user_shop_relations AS relation
  ON relation.user_id = u.id
 AND relation.shop_id = u.current_shop_id
LEFT JOIN shops AS active_shop
  ON active_shop.id = relation.shop_id
 AND active_shop.is_active = TRUE
WHERE u.id = ?`
	result := r.db.WithContext(queryContext(ctx)).Raw(query, userID).Scan(&row)
	if result.Error != nil {
		return UserProfileProjection{}, result.Error
	}
	if result.RowsAffected == 0 {
		return UserProfileProjection{}, ErrUserQueryRecordNotFound
	}
	profile := UserProfileProjection{
		ID: row.ID, Account: row.Account, Name: row.Name, IsAdmin: row.IsAdmin,
		Email: nullableString(row.Email), Phone: nullableString(row.Phone),
	}
	if row.CurrentShopID.Valid && row.CurrentShopID.Int64 > 0 {
		shopID := uint(row.CurrentShopID.Int64)
		if int64(shopID) == row.CurrentShopID.Int64 {
			profile.CurrentShopID = &shopID
		}
	}
	return profile, nil
}

func nullableString(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	out := value.String
	return &out
}

func queryContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
