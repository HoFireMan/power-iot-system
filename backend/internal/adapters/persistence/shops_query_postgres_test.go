package persistence

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestAuthorizedShopsUsesRelationsActiveRowsAndDeterministicOrder(t *testing.T) {
	db := authDB(t)
	suffix := uuid.NewString()[:12]
	accountPrefix := "b6-shops-" + suffix + "-"
	codePrefix := "b6-shops-" + suffix + "-"
	clientID := insertQueryClient(t, db, codePrefix+"client")
	shopX := insertQueryShop(t, db, clientID, codePrefix+"x", true)
	shopY := insertQueryShop(t, db, clientID, codePrefix+"y", true)
	shopInactive := insertQueryShop(t, db, clientID, codePrefix+"inactive", false)
	if err := db.Exec("UPDATE shops SET address = ?, phone = ?, is_head = true WHERE id = ?", "Y address", "Y phone", shopY).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		db.Exec("DELETE FROM user_shop_relations WHERE user_id IN (SELECT id FROM users WHERE account LIKE ?)", accountPrefix+"%")
		db.Exec("DELETE FROM users WHERE account LIKE ?", accountPrefix+"%")
		db.Exec("DELETE FROM shops WHERE code LIKE ?", codePrefix+"%")
		db.Exec("DELETE FROM clients WHERE code = ?", codePrefix+"client")
	}()

	one := insertQueryUser(t, db, accountPrefix+"one", &shopY, false, "", "")
	for _, shopID := range []uint{shopY, shopX, shopInactive} {
		if err := db.Exec("INSERT INTO user_shop_relations (user_id, shop_id) VALUES (?, ?)", one, shopID).Error; err != nil {
			t.Fatal(err)
		}
	}
	other := insertQueryUser(t, db, accountPrefix+"other", &shopX, false, "", "")
	if err := db.Exec("INSERT INTO user_shop_relations (user_id, shop_id) VALUES (?, ?)", other, shopX).Error; err != nil {
		t.Fatal(err)
	}
	admin := insertQueryUser(t, db, accountPrefix+"admin", &shopX, true, "", "")
	if err := db.Exec("INSERT INTO user_shop_relations (user_id, shop_id) VALUES (?, ?)", admin, shopY).Error; err != nil {
		t.Fatal(err)
	}
	zero := insertQueryUser(t, db, accountPrefix+"zero", nil, true, "", "")

	repository := NewUserShopQueryRepository(db)
	shops, current, err := repository.FindAuthorizedShops(context.Background(), one)
	if err != nil {
		t.Fatal(err)
	}
	if len(shops) != 2 || shops[0].ID != shopX || shops[1].ID != shopY {
		t.Fatalf("shops=%+v, want active IDs in id order", shops)
	}
	if current == nil || *current != shopY {
		t.Fatalf("current=%v, want %d", current, shopY)
	}
	if shops[1].Address == nil || *shops[1].Address != "Y address" || shops[1].Phone == nil || *shops[1].Phone != "Y phone" || !shops[1].IsHead {
		t.Fatalf("safe nullable/flag projection=%+v", shops[1])
	}
	for _, shop := range shops {
		if shop.ID == shopInactive {
			t.Fatalf("inactive shop leaked: %+v", shop)
		}
	}

	adminShops, adminCurrent, err := repository.FindAuthorizedShops(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(adminShops) != 1 || adminShops[0].ID != shopY || adminCurrent != nil {
		t.Fatalf("admin shops/current=%+v/%v, want only related Y and null current", adminShops, adminCurrent)
	}
	otherShops, _, err := repository.FindAuthorizedShops(context.Background(), other)
	if err != nil || len(otherShops) != 1 || otherShops[0].ID != shopX {
		t.Fatalf("cross-user shops=%+v err=%v", otherShops, err)
	}
	zeroShops, zeroCurrent, err := repository.FindAuthorizedShops(context.Background(), zero)
	if err != nil || zeroShops == nil || len(zeroShops) != 0 || zeroCurrent != nil {
		t.Fatalf("zero relation result=%+v/%v err=%v", zeroShops, zeroCurrent, err)
	}
}

// The relation's unique (user_id, shop_id) constraint makes duplicate list
// rows structurally impossible; this test intentionally does not bypass it.
