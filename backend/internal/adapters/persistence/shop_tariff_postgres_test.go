package persistence

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestShopTariffMutationRequiresScopedActiveAdmin(t *testing.T) {
	db := openPersistenceDB(t)
	suffix := "tariff-" + uuid.NewString()[:8]
	clientID := insertQueryClient(t, db, suffix)
	activeShop := insertQueryShop(t, db, clientID, suffix+"-active", true)
	otherShop := insertQueryShop(t, db, clientID, suffix+"-other", true)
	inactiveShop := insertQueryShop(t, db, clientID, suffix+"-inactive", false)
	admin := insertQueryUser(t, db, suffix+"-admin", &activeShop, true, "", "")
	normal := insertQueryUser(t, db, suffix+"-normal", &activeShop, false, "", "")
	unrelatedAdmin := insertQueryUser(t, db, suffix+"-unrelated", &otherShop, true, "", "")
	if err := db.Exec("INSERT INTO user_shop_relations (user_id, shop_id) VALUES (?, ?), (?, ?), (?, ?)", admin, activeShop, normal, activeShop, unrelatedAdmin, otherShop).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM user_shop_relations WHERE user_id IN (?, ?, ?)", admin, normal, unrelatedAdmin)
		db.Exec("DELETE FROM users WHERE id IN (?, ?, ?)", admin, normal, unrelatedAdmin)
		db.Exec("DELETE FROM shops WHERE id IN (?, ?, ?)", activeShop, otherShop, inactiveShop)
		db.Exec("DELETE FROM clients WHERE id = ?", clientID)
	})

	mutation := NewShopMutationRepository(db)
	if err := mutation.UpdateShopTariff(context.Background(), admin, activeShop, "LOW_VOLTAGE"); err != nil {
		t.Fatalf("scoped admin update: %v", err)
	}
	var tariff string
	if err := db.Raw("SELECT electricity_tariff FROM shops WHERE id = ?", activeShop).Scan(&tariff).Error; err != nil {
		t.Fatal(err)
	}
	if tariff != "LOW_VOLTAGE" {
		t.Fatalf("tariff=%q", tariff)
	}
	for name, actorShop := range map[string][2]uint{
		"normal user":          {normal, activeShop},
		"unrelated admin":      {unrelatedAdmin, activeShop},
		"inactive shop":        {admin, inactiveShop},
		"other shop isolation": {admin, otherShop},
	} {
		t.Run(name, func(t *testing.T) {
			err := mutation.UpdateShopTariff(context.Background(), actorShop[0], actorShop[1], "HIGH_VOLTAGE")
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				t.Fatalf("err=%v, want authorization failure", err)
			}
		})
	}
}
