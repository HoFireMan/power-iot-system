package persistence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"power-iot-backend/internal/core/domain"
)

func TestMeasurementPointAlertSettingsAuthorizationAndWatermarkPersistence(t *testing.T) {
	db := authDB(t)
	suffix := uuid.NewString()[:12]
	client := domain.Client{Code: "alerts-client-" + suffix, Name: "Alerts Client"}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	shop := domain.Shop{ClientID: client.ID, Code: "alerts-shop-" + suffix, Name: "Alerts Shop", IsActive: true}
	otherShop := domain.Shop{ClientID: client.ID, Code: "alerts-other-" + suffix, Name: "Other Shop", IsActive: true}
	if err := db.Create(&shop).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&otherShop).Error; err != nil {
		t.Fatal(err)
	}
	point := domain.MeasurementPoint{ID: uuid.New(), ShopID: shop.ID, Name: "Alerts MP"}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	admin := domain.User{Account: "alerts-admin-" + suffix, PasswordHash: "hash", Name: "Admin", IsAdmin: true, AuthEnabled: true}
	member := domain.User{Account: "alerts-member-" + suffix, PasswordHash: "hash", Name: "Member", AuthEnabled: true}
	crossAdmin := domain.User{Account: "alerts-cross-" + suffix, PasswordHash: "hash", Name: "Cross Admin", IsAdmin: true, AuthEnabled: true}
	for _, user := range []*domain.User{&admin, &member, &crossAdmin} {
		if err := db.Create(user).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, relation := range []domain.UserShopRelation{
		{UserID: admin.ID, ShopID: shop.ID},
		{UserID: member.ID, ShopID: shop.ID},
		{UserID: crossAdmin.ID, ShopID: otherShop.ID},
	} {
		if err := db.Create(&relation).Error; err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		db.Exec("DELETE FROM measurement_point_curfew_states WHERE measurement_point_id = ?", point.ID)
		db.Exec("DELETE FROM measurement_point_alert_settings WHERE measurement_point_id = ?", point.ID)
		db.Exec("DELETE FROM user_shop_relations WHERE user_id IN (?, ?, ?)", admin.ID, member.ID, crossAdmin.ID)
		db.Unscoped().Delete(&domain.User{}, admin.ID)
		db.Unscoped().Delete(&domain.User{}, member.ID)
		db.Unscoped().Delete(&domain.User{}, crossAdmin.ID)
		db.Unscoped().Delete(&domain.MeasurementPoint{}, point.ID)
		db.Unscoped().Delete(&domain.Shop{}, shop.ID)
		db.Unscoped().Delete(&domain.Shop{}, otherShop.ID)
		db.Unscoped().Delete(&domain.Client{}, client.ID)
	}()

	repository := NewMeasurementPointAlertRepository(db)
	if _, err := repository.FindMeasurementPointAlertSettings(context.Background(), member.ID, shop.ID, point.ID); err != nil {
		t.Fatalf("authorized member GET failed: %v", err)
	}
	if _, err := repository.FindMeasurementPointAlertSettings(context.Background(), crossAdmin.ID, shop.ID, point.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-Shop GET err=%v, want not found", err)
	}
	if err := repository.SetMeasurementPointAlertSettings(context.Background(), member.ID, shop.ID, point.ID, "22:00", "02:00", 10, true); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("ordinary member PUT err=%v, want not found", err)
	}
	if err := repository.SetMeasurementPointAlertSettings(context.Background(), crossAdmin.ID, shop.ID, point.ID, "22:00", "02:00", 10, true); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-Shop admin PUT err=%v, want not found", err)
	}
	if err := db.Create(&domain.MeasurementPointAlertSetting{MeasurementPointID: point.ID, QuietHoursStart: "20:00", QuietHoursEnd: "06:00", PowerThresholdW: 10, IsEnabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	watermark := time.Date(2026, 8, 8, 15, 0, 0, 0, time.UTC)
	if err := db.Create(&domain.MeasurementPointCurfewState{MeasurementPointID: point.ID, InCurfew: true, LastEventAt: &watermark}).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.SetMeasurementPointAlertSettings(context.Background(), admin.ID, shop.ID, point.ID, "21:00", "05:00", 12, true); err != nil {
		t.Fatalf("scoped-admin PUT failed: %v", err)
	}
	var state domain.MeasurementPointCurfewState
	if err := db.First(&state, "measurement_point_id = ?", point.ID).Error; err != nil {
		t.Fatal(err)
	}
	if state.InCurfew || state.LastEventAt == nil || !state.LastEventAt.Equal(watermark) {
		t.Fatalf("policy update did not reset edge while preserving watermark: %+v", state)
	}
}

func TestMeasurementPointAlertHistoryAuthorizationFilteringAndSameTimestampCursor(t *testing.T) {
	db := authDB(t)
	suffix := uuid.NewString()[:12]
	client := domain.Client{Code: "history-client-" + suffix, Name: "History Client"}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	shop := domain.Shop{ClientID: client.ID, Code: "history-shop-" + suffix, Name: "History Shop", IsActive: true}
	otherShop := domain.Shop{ClientID: client.ID, Code: "history-other-" + suffix, Name: "Other History Shop", IsActive: true}
	if err := db.Create(&shop).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&otherShop).Error; err != nil {
		t.Fatal(err)
	}
	point := domain.MeasurementPoint{ID: uuid.New(), ShopID: shop.ID, Name: "History MP"}
	otherPoint := domain.MeasurementPoint{ID: uuid.New(), ShopID: otherShop.ID, Name: "Other MP"}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&otherPoint).Error; err != nil {
		t.Fatal(err)
	}
	device := domain.Device{ShopID: shop.ID, MacAddress: "AABBCCDDEEFF", Name: "History Device"}
	otherDevice := domain.Device{ShopID: otherShop.ID, MacAddress: "FFEEDDCCBBAA", Name: "Other Device"}
	if err := db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&otherDevice).Error; err != nil {
		t.Fatal(err)
	}
	member := domain.User{Account: "history-member-" + suffix, PasswordHash: "hash", Name: "Member", AuthEnabled: true}
	outsider := domain.User{Account: "history-outsider-" + suffix, PasswordHash: "hash", Name: "Outsider", AuthEnabled: true}
	if err := db.Create(&member).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&outsider).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.UserShopRelation{UserID: member.ID, ShopID: shop.ID}).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		db.Exec("DELETE FROM alert_logs WHERE device_id IN (?, ?)", device.ID, otherDevice.ID)
		db.Exec("DELETE FROM user_shop_relations WHERE user_id IN (?, ?)", member.ID, outsider.ID)
		db.Unscoped().Delete(&domain.User{}, member.ID)
		db.Unscoped().Delete(&domain.User{}, outsider.ID)
		db.Unscoped().Delete(&domain.Device{}, device.ID)
		db.Unscoped().Delete(&domain.Device{}, otherDevice.ID)
		db.Unscoped().Delete(&domain.MeasurementPoint{}, point.ID)
		db.Unscoped().Delete(&domain.MeasurementPoint{}, otherPoint.ID)
		db.Unscoped().Delete(&domain.Shop{}, shop.ID)
		db.Unscoped().Delete(&domain.Shop{}, otherShop.ID)
		db.Unscoped().Delete(&domain.Client{}, client.ID)
	}()

	created := time.Date(2026, 8, 8, 15, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		id := point.ID
		if err := db.Create(&domain.AlertLog{DeviceID: device.ID, MeasurementPointID: &id, Type: "CURFEW_USAGE", Message: "same timestamp", Power: float64(i + 1), CreatedAt: created, RecordedAt: created}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&domain.AlertLog{DeviceID: device.ID, MeasurementPointID: nil, LegacyUnresolved: true, CreatedAt: created, RecordedAt: created}).Error; err != nil {
		t.Fatal(err)
	}
	otherID := otherPoint.ID
	if err := db.Create(&domain.AlertLog{DeviceID: otherDevice.ID, MeasurementPointID: &otherID, CreatedAt: created, RecordedAt: created}).Error; err != nil {
		t.Fatal(err)
	}

	repository := NewMeasurementPointAlertRepository(db)
	if _, err := repository.FindAlertHistory(context.Background(), outsider.ID, shop.ID, nil, 2, ""); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("unauthorized history err=%v, want not found", err)
	}
	first, err := repository.FindAlertHistory(context.Background(), member.ID, shop.ID, nil, 2, "")
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first history page=%+v err=%v", first, err)
	}
	second, err := repository.FindAlertHistory(context.Background(), member.ID, shop.ID, nil, 2, first.NextCursor)
	if err != nil || len(second.Items) != 2 {
		t.Fatalf("second history page=%+v err=%v", second, err)
	}
	third, err := repository.FindAlertHistory(context.Background(), member.ID, shop.ID, nil, 2, second.NextCursor)
	if err != nil || len(third.Items) != 1 || third.NextCursor != "" {
		t.Fatalf("third history page=%+v err=%v", third, err)
	}
	seen := map[uint64]bool{}
	for _, page := range []AlertHistoryPage{first, second, third} {
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatalf("cursor duplicated alert id=%d", item.ID)
			}
			seen[item.ID] = true
			if item.MeasurementPointID != point.ID {
				t.Fatalf("history leaked point=%s", item.MeasurementPointID)
			}
		}
	}
	if len(seen) != 5 {
		t.Fatalf("cursor skipped or included excluded rows: seen=%d ids=%v", len(seen), seen)
	}
	filtered, err := repository.FindAlertHistory(context.Background(), member.ID, shop.ID, &point.ID, 100, "")
	if err != nil || len(filtered.Items) != 5 {
		t.Fatalf("measurement point filter page=%+v err=%v", filtered, err)
	}
}
