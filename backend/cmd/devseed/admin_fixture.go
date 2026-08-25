package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/data/migrations"
	"power-iot-backend/internal/security"
)

const (
	devSeedAdminPasswordEnv = "DEVSEED_ADMIN_PASSWORD"
	devSeedAdminAccount     = "devseed-admin"
	devSeedAdminUserName    = "Development Seed Admin"
)

func resolveAdminFixturePassword(enabled bool, value string) (string, error) {
	if !enabled {
		return "", nil
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("development admin fixture password is required via %s", devSeedAdminPasswordEnv)
	}
	return value, nil
}

// seedDevelopmentAdminIdentity creates only the explicit development admin
// fixture. Existing rows are accepted only when their complete identity and
// single-Shop authorization exactly match the canonical fixture.
func seedDevelopmentAdminIdentity(ctx context.Context, db *gorm.DB, shopID uint, password string) error {
	if strings.TrimSpace(password) == "" {
		return fmt.Errorf("development admin fixture password is required via %s", devSeedAdminPasswordEnv)
	}
	if db == nil {
		return errors.New("development admin fixture database is required")
	}
	if shopID == 0 {
		return errors.New("development admin fixture Shop is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := migrations.AcquireSharedWriterFenceOnGORM(ctx, tx); err != nil {
			return err
		}

		var shop domain.Shop
		if err := tx.Where("id = ? AND code = ?", shopID, devSeedShopCode).First(&shop).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("development admin fixture Shop is not the canonical development Shop")
			}
			return err
		}
		if shop.Name != devSeedShopName || !shop.IsActive || shop.ClientID == 0 {
			return errors.New("development admin fixture Shop is not valid")
		}

		var user domain.User
		result := tx.Where("account = ?", devSeedAdminAccount).First(&user)
		created := false
		switch {
		case errors.Is(result.Error, gorm.ErrRecordNotFound):
			passwordHash, err := security.HashPassword([]byte(password))
			if err != nil {
				return errors.New("development admin fixture password could not be hashed")
			}
			currentShopID := shop.ID
			user = domain.User{
				Account: devSeedAdminAccount, PasswordHash: passwordHash, Name: devSeedAdminUserName,
				AuthEnabled: true, IsAdmin: true, CurrentShopID: &currentShopID,
			}
			if err := tx.Create(&user).Error; err != nil {
				return err
			}
			created = true
		case result.Error != nil:
			return result.Error
		default:
			if user.Name != devSeedAdminUserName || !user.AuthEnabled || !user.IsAdmin || user.CurrentShopID == nil || *user.CurrentShopID != shop.ID {
				return errors.New("development admin fixture account is already in use")
			}
			valid, err := security.VerifyPassword([]byte(password), user.PasswordHash)
			if err != nil || !valid {
				return errors.New("development admin fixture password mismatch")
			}
		}

		var relations []domain.UserShopRelation
		if err := tx.Where("user_id = ?", user.ID).Find(&relations).Error; err != nil {
			return err
		}
		if created {
			if len(relations) != 0 {
				return errors.New("development admin fixture account has unexpected authorization")
			}
			return tx.Create(&domain.UserShopRelation{UserID: user.ID, ShopID: shop.ID, ShopRole: "staff"}).Error
		}
		if len(relations) != 1 || relations[0].ShopID != shop.ID || relations[0].ShopRole != "staff" {
			return errors.New("development admin fixture authorization conflicts")
		}
		return nil
	})
}
