package persistence

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestUserQueryCurrentShopAuthorityAndNullableProjection(t *testing.T) {
	db := authDB(t)
	suffix := uuid.NewString()[:12]
	accountPrefix := "b6-me-" + suffix + "-"
	codePrefix := "b6-me-" + suffix + "-"
	clientID := insertQueryClient(t, db, codePrefix+"client")
	activeShop := insertQueryShop(t, db, clientID, codePrefix+"active", true)
	inactiveShop := insertQueryShop(t, db, clientID, codePrefix+"inactive", false)
	unrelatedShop := insertQueryShop(t, db, clientID, codePrefix+"unrelated", true)
	defer func() {
		db.Exec("DELETE FROM user_shop_relations WHERE user_id IN (SELECT id FROM users WHERE account LIKE ?)", accountPrefix+"%")
		db.Exec("DELETE FROM users WHERE account LIKE ?", accountPrefix+"%")
		db.Exec("DELETE FROM shops WHERE code LIKE ?", codePrefix+"%")
		db.Exec("DELETE FROM clients WHERE code = ?", codePrefix+"client")
	}()

	valid := insertQueryUser(t, db, accountPrefix+"valid", &activeShop, true, "valid@example.test", "+1")
	if result := db.Exec("INSERT INTO user_shop_relations (user_id, shop_id) VALUES (?, ?)", valid, activeShop); result.Error != nil {
		t.Fatal(result.Error)
	}
	nullCurrent := insertQueryUser(t, db, accountPrefix+"null", nil, false, "", "")
	stale := uint(999999999)
	staleUser := insertQueryUser(t, db, accountPrefix+"stale", &stale, false, "", "")
	inactive := insertQueryUser(t, db, accountPrefix+"inactive", &inactiveShop, false, "", "")
	if result := db.Exec("INSERT INTO user_shop_relations (user_id, shop_id) VALUES (?, ?)", inactive, inactiveShop); result.Error != nil {
		t.Fatal(result.Error)
	}
	unrelated := insertQueryUser(t, db, accountPrefix+"unrelated", &activeShop, false, "", "")
	if result := db.Exec("INSERT INTO user_shop_relations (user_id, shop_id) VALUES (?, ?)", unrelated, unrelatedShop); result.Error != nil {
		t.Fatal(result.Error)
	}
	admin := insertQueryUser(t, db, accountPrefix+"admin", &unrelatedShop, true, "", "")
	if result := db.Exec("INSERT INTO user_shop_relations (user_id, shop_id) VALUES (?, ?)", admin, activeShop); result.Error != nil {
		t.Fatal(result.Error)
	}
	noShops := insertQueryUser(t, db, accountPrefix+"no-shops", nil, false, "", "")

	repository := NewUserQueryRepository(db)
	tests := []struct {
		name     string
		userID   uint
		shopID   *uint
		nullable bool
	}{
		{name: "active valid", userID: valid, shopID: &activeShop},
		{name: "null", userID: nullCurrent, nullable: true},
		{name: "nonexistent", userID: staleUser, nullable: true},
		{name: "inactive", userID: inactive, nullable: true},
		{name: "unrelated", userID: unrelated, nullable: true},
		{name: "admin unrelated sanitized", userID: admin, nullable: true},
		{name: "no shops", userID: noShops, nullable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile, err := repository.FindUserProfile(context.Background(), test.userID)
			if err != nil {
				t.Fatal(err)
			}
			if test.nullable && profile.CurrentShopID != nil {
				t.Fatalf("current shop=%v, want null", *profile.CurrentShopID)
			}
			if test.shopID != nil && (profile.CurrentShopID == nil || *profile.CurrentShopID != *test.shopID) {
				t.Fatalf("current shop=%v, want %d", profile.CurrentShopID, *test.shopID)
			}
			if test.nullable && (profile.Email != nil || profile.Phone != nil) {
				t.Fatalf("nullable contact fields=%v/%v", profile.Email, profile.Phone)
			}
		})
	}
}

func insertQueryClient(t *testing.T, db *gorm.DB, code string) uint {
	t.Helper()
	var id uint
	if err := db.Raw("INSERT INTO clients (name, code) VALUES (?, ?) RETURNING id", code, code).Scan(&id).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

func insertQueryShop(t *testing.T, db *gorm.DB, clientID uint, code string, active bool) uint {
	t.Helper()
	var id uint
	if err := db.Raw("INSERT INTO shops (client_id, code, name, is_active) VALUES (?, ?, ?, ?) RETURNING id", clientID, code, code, active).Scan(&id).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

func insertQueryUser(t *testing.T, db *gorm.DB, account string, current *uint, admin bool, email, phone string) uint {
	t.Helper()
	var id uint
	if current == nil {
		if err := db.Raw("INSERT INTO users (account, password_hash, name, email, phone, is_admin, current_shop_id) VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULL) RETURNING id", account, "not-returned", account, email, phone, admin).Scan(&id).Error; err != nil {
			t.Fatal(err)
		}
		return id
	}
	if err := db.Raw("INSERT INTO users (account, password_hash, name, email, phone, is_admin, current_shop_id) VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?) RETURNING id", account, "not-returned", account, email, phone, admin, *current).Scan(&id).Error; err != nil {
		t.Fatal(err)
	}
	return id
}
