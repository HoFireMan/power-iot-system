package persistence

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"power-iot-backend/internal/core/domain"
)

func TestMeasurementPointDetailQueryAuthorizesHistoryReplacementAndB7Power(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	if err := db.Exec(`INSERT INTO user_shop_relations (user_id, shop_id) VALUES (?, ?)`, fixture.actorID, fixture.shopID).Error; err != nil { t.Fatal(err) }
	at := time.Date(2026, 8, 24, 4, 0, 0, 0, time.UTC)
	boundary := at.Add(-time.Hour)
	insertAssignment(t, db, fixture.deviceID, fixture.pointID, boundary.Add(-time.Hour), &boundary)
	insertAssignment(t, db, fixture.otherDevice, fixture.pointID, boundary, nil)
	if err := db.Model(&domain.Device{}).Where("id = ?", fixture.otherDevice).Updates(map[string]any{"is_online": true, "last_seen": at.Add(-time.Minute)}).Error; err != nil { t.Fatal(err) }
	if err := db.Exec(`INSERT INTO power_readings (time, recorded_at, received_at, measurement_point_id, device_id, active_power) VALUES (?, ?, ?, ?, ?, ?)`, at.Add(-30*time.Second), at.Add(-30*time.Second), at.Add(-20*time.Second), fixture.pointID, fixture.otherDevice, 263.86).Error; err != nil { t.Fatal(err) }
	repo := NewMeasurementPointDetailQueryRepository(db)
	projection, err := repo.FindMeasurementPointDetail(t.Context(), fixture.actorID, fixture.shopID, fixture.pointID, func() time.Time { return at })
	if err != nil { t.Fatal(err) }
	if projection.CurrentDevice == nil || projection.CurrentDevice.DeviceID != fixture.otherDevice || projection.CurrentDevice.Name != "device-b" { t.Fatalf("current=%+v", projection.CurrentDevice) }
	if projection.CurrentPowerW == nil || *projection.CurrentPowerW != 263.86 || projection.CurrentPowerSeenAt == nil { t.Fatalf("power=%+v seen=%v", projection.CurrentPowerW, projection.CurrentPowerSeenAt) }
	if len(projection.AssignmentHistory) != 2 || projection.AssignmentHistory[0].DeviceID != fixture.otherDevice { t.Fatalf("history=%+v", projection.AssignmentHistory) }

	var inactiveShop uint
	if err := db.Raw(`INSERT INTO shops (code, name, is_active) VALUES (?, ?, false) RETURNING id`, "inactive-"+uuid.NewString()[:8], "Inactive").Scan(&inactiveShop).Error; err != nil { t.Fatal(err) }
	if _, err := repo.FindMeasurementPointDetail(t.Context(), fixture.actorID, inactiveShop, fixture.pointID, func() time.Time { return at }); !errors.Is(err, ErrMeasurementPointNotFound) { t.Fatalf("inactive shop err=%v", err) }
}

func TestMeasurementPointDetailQueryScopedAdminRequiresRelation(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	if err := db.Exec(`UPDATE users SET is_admin = true WHERE id = ?`, fixture.actorID).Error; err != nil { t.Fatal(err) }
	repo := NewMeasurementPointDetailQueryRepository(db)
	projection, err := repo.FindMeasurementPointDetail(t.Context(), fixture.actorID, fixture.shopID, fixture.pointID, time.Now)
	if !errors.Is(err, ErrMeasurementPointNotFound) || projection.ScopedAdmin { t.Fatalf("unrelated admin result=%+v err=%v", projection, err) }
	if err := db.Exec(`INSERT INTO user_shop_relations (user_id, shop_id) VALUES (?, ?)`, fixture.actorID, fixture.shopID).Error; err != nil { t.Fatal(err) }
	projection, err = repo.FindMeasurementPointDetail(t.Context(), fixture.actorID, fixture.shopID, fixture.pointID, time.Now)
	if err != nil || !projection.ScopedAdmin { t.Fatalf("scoped admin result=%+v err=%v", projection, err) }
}
