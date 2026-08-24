package persistence

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestDashboardProjectionUsesAuthorizationAssignmentAndDeviceStatusAuthority(t *testing.T) {
	db := openPersistenceDB(t)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	macPrefix := suffix[:8]
	clientCode := "b7a-" + suffix
	activeCode := clientCode + "-active"
	inactiveCode := clientCode + "-inactive"
	otherCode := clientCode + "-other"
	clientID := insertQueryClient(t, db, clientCode)
	activeShop := insertQueryShop(t, db, clientID, activeCode, true)
	inactiveShop := insertQueryShop(t, db, clientID, inactiveCode, false)
	otherShop := insertQueryShop(t, db, clientID, otherCode, true)
	accountPrefix := "b7a-" + suffix + "-"
	relatedUser := insertQueryUser(t, db, accountPrefix+"related", &activeShop, false, "", "")
	inactiveUser := insertQueryUser(t, db, accountPrefix+"inactive", &inactiveShop, false, "", "")
	unrelatedUser := insertQueryUser(t, db, accountPrefix+"unrelated", &otherShop, false, "", "")
	adminUser := insertQueryUser(t, db, accountPrefix+"admin", &otherShop, true, "", "")
	for user, shop := range map[uint]uint{relatedUser: activeShop, inactiveUser: inactiveShop, unrelatedUser: otherShop, adminUser: otherShop} {
		if err := db.Exec("INSERT INTO user_shop_relations (user_id, shop_id) VALUES (?, ?)", user, shop).Error; err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		db.Exec("DELETE FROM user_shop_relations WHERE user_id IN (SELECT id FROM users WHERE account LIKE ?)", accountPrefix+"%")
		db.Exec("DELETE FROM device_assignments WHERE device_id IN (SELECT id FROM devices WHERE mac_address LIKE ?)", macPrefix+"%")
		db.Exec("DELETE FROM measurement_points WHERE shop_id IN (?, ?, ?)", activeShop, inactiveShop, otherShop)
		db.Exec("DELETE FROM devices WHERE mac_address LIKE ?", macPrefix+"%")
		db.Exec("DELETE FROM users WHERE account LIKE ?", accountPrefix+"%")
		db.Exec("DELETE FROM shops WHERE code LIKE ?", clientCode+"%")
		db.Exec("DELETE FROM clients WHERE code = ?", clientCode)
	}()

	pointID := uuid.New()
	otherPointID := uuid.New()
	if err := db.Exec("INSERT INTO measurement_points (id, shop_id, name) VALUES (?, ?, ?), (?, ?, ?)", pointID, activeShop, "Main point", otherPointID, otherShop, "Other point").Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	validDevice := insertDashboardDevice(t, db, macPrefix+"0001", otherShop, "Authoritative device", true)
	_ = insertDashboardDevice(t, db, macPrefix+"0002", activeShop, "Unassigned", true)
	futureDevice := insertDashboardDevice(t, db, macPrefix+"0003", activeShop, "Future", true)
	expiredDevice := insertDashboardDevice(t, db, macPrefix+"0004", activeShop, "Expired", true)
	negativeDevice := insertDashboardDevice(t, db, macPrefix+"0005", activeShop, "Other shop assignment", true)
	insertDashboardAssignment(t, db, validDevice, pointID, now.Add(-time.Hour), timePtr(now.Add(30*time.Minute)))
	if err := db.Exec("UPDATE devices SET last_seen = ? WHERE id = ?", now.Add(-24*time.Hour), validDevice).Error; err != nil {
		t.Fatal(err)
	}
	insertDashboardAssignment(t, db, futureDevice, pointID, now.Add(time.Hour), nil)
	insertDashboardAssignment(t, db, expiredDevice, pointID, now.Add(-2*time.Hour), timePtr(now.Add(-time.Hour)))
	insertDashboardAssignment(t, db, negativeDevice, otherPointID, now.Add(-time.Hour), timePtr(now.Add(time.Hour)))
	insertDashboardPowerReading(t, db, validDevice, &pointID, now.Add(-time.Minute), 7)
	insertDashboardPowerReading(t, db, negativeDevice, &otherPointID, now.Add(-time.Minute), 99)

	repository := NewDashboardQueryRepository(db)
	projection, err := repository.FindDashboard(context.Background(), relatedUser, activeShop, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if projection.Shop.ID != activeShop || len(projection.Devices) != 1 || projection.Devices[0].ID != validDevice {
		t.Fatalf("projection=%+v, want only current assignment", projection)
	}
	if projection.Devices[0].Name != "Authoritative device" || !projection.Devices[0].IsOnline {
		t.Fatalf("status projection=%+v", projection.Devices[0])
	}
	if projection.CurrentPowerW == nil || *projection.CurrentPowerW != 7 {
		t.Fatalf("current power=%v, want 7 from assignment authority", projection.CurrentPowerW)
	}

	for name, candidate := range map[string]struct {
		user uint
		shop uint
	}{
		"nonexistent": {user: relatedUser, shop: activeShop + 999999},
		"inactive":    {user: inactiveUser, shop: inactiveShop},
		"unrelated":   {user: unrelatedUser, shop: activeShop},
		"admin":       {user: adminUser, shop: activeShop},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := repository.FindDashboard(context.Background(), candidate.user, candidate.shop, func() time.Time { return now }); err != ErrDashboardNotFound {
				t.Fatalf("err=%v, want ErrDashboardNotFound", err)
			}
		})
	}
}

func insertDashboardDevice(t *testing.T, db *gorm.DB, mac string, shopID uint, name string, online bool) uint {
	t.Helper()
	var id uint
	if err := db.Raw("INSERT INTO devices (shop_id, mac_address, name, is_online) VALUES (?, ?, ?, ?) RETURNING id", shopID, strings.ToUpper(mac), name, online).Scan(&id).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

func insertDashboardAssignment(t *testing.T, db *gorm.DB, deviceID uint, pointID uuid.UUID, from time.Time, to *time.Time) {
	t.Helper()
	if err := db.Exec("INSERT INTO device_assignments (device_id, measurement_point_id, valid_from, valid_to) VALUES (?, ?, ?, ?)", deviceID, pointID, from, to).Error; err != nil {
		t.Fatal(err)
	}
}

func timePtr(value time.Time) *time.Time { return &value }

type dashboardPowerFixture struct {
	clientID  uint
	shopID    uint
	userID    uint
	pointIDs  []uuid.UUID
	deviceIDs []uint
	now       time.Time
}

func newDashboardPowerFixture(t *testing.T, db *gorm.DB, deviceCount int) dashboardPowerFixture {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	clientCode := "b7b-" + suffix
	clientID := insertQueryClient(t, db, clientCode)
	shopID := insertQueryShop(t, db, clientID, clientCode, true)
	account := "b7b-" + suffix + "-user"
	userID := insertQueryUser(t, db, account, &shopID, false, "", "")
	if err := db.Exec("INSERT INTO user_shop_relations (user_id, shop_id) VALUES (?, ?)", userID, shopID).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	fixture := dashboardPowerFixture{clientID: clientID, shopID: shopID, userID: userID, now: now}
	for i := 0; i < deviceCount; i++ {
		pointID := uuid.New()
		if err := db.Exec("INSERT INTO measurement_points (id, shop_id, name) VALUES (?, ?, ?)", pointID, shopID, "point").Error; err != nil {
			t.Fatal(err)
		}
		deviceID := insertDashboardDevice(t, db, suffix[:8]+fmt.Sprintf("%04d", i), shopID, "device", true)
		insertDashboardAssignment(t, db, deviceID, pointID, now.Add(-time.Hour), nil)
		fixture.pointIDs = append(fixture.pointIDs, pointID)
		fixture.deviceIDs = append(fixture.deviceIDs, deviceID)
	}
	t.Cleanup(func() {
		if len(fixture.deviceIDs) > 0 {
			db.Exec("DELETE FROM power_readings WHERE device_id IN ?", fixture.deviceIDs)
			db.Exec("DELETE FROM telemetry_ingest_keys WHERE device_id IN ?", fixture.deviceIDs)
			db.Exec("DELETE FROM device_assignments WHERE device_id IN ?", fixture.deviceIDs)
		}
		db.Exec("DELETE FROM user_shop_relations WHERE user_id = ?", userID)
		db.Exec("DELETE FROM measurement_points WHERE shop_id = ?", shopID)
		if len(fixture.deviceIDs) > 0 {
			db.Exec("DELETE FROM devices WHERE id IN ?", fixture.deviceIDs)
		}
		db.Exec("DELETE FROM users WHERE id = ?", userID)
		db.Exec("DELETE FROM shops WHERE id = ?", shopID)
		db.Exec("DELETE FROM clients WHERE id = ?", clientID)
	})
	return fixture
}

func insertDashboardPowerReading(t *testing.T, db *gorm.DB, deviceID uint, pointID *uuid.UUID, receivedAt time.Time, activePower any) {
	t.Helper()
	recordedAt := receivedAt
	if err := db.Exec(`INSERT INTO power_readings
		(time, recorded_at, received_at, measurement_point_id, device_id, voltage, current, active_power, kwh_total, boot_counter, sequence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, recordedAt, recordedAt, receivedAt, pointID, deviceID, 110, 1, activePower, 0, 1, 1).Error; err != nil {
		t.Fatal(err)
	}
}

func TestDashboardCurrentPowerAcceptanceMatrix(t *testing.T) {
	db := openPersistenceDB(t)
	tests := []struct {
		name        string
		deviceCount int
		write       func(*dashboardPowerFixture)
		wantPower   *float64
		wantDevices int
	}{
		{name: "one matching fresh", deviceCount: 1, wantDevices: 1, write: func(f *dashboardPowerFixture) {
			insertDashboardPowerReading(t, db, f.deviceIDs[0], &f.pointIDs[0], f.now.Add(-time.Minute), 12.34)
		}, wantPower: floatPtr(12.34)},
		{name: "multiple decimal fresh", deviceCount: 2, wantDevices: 2, write: func(f *dashboardPowerFixture) {
			insertDashboardPowerReading(t, db, f.deviceIDs[0], &f.pointIDs[0], f.now.Add(-time.Minute), 12.34)
			insertDashboardPowerReading(t, db, f.deviceIDs[1], &f.pointIDs[1], f.now.Add(-2*time.Minute), 0.56)
		}, wantPower: floatPtr(12.90)},
		{name: "all zero", deviceCount: 2, wantDevices: 2, write: func(f *dashboardPowerFixture) {
			insertDashboardPowerReading(t, db, f.deviceIDs[0], &f.pointIDs[0], f.now.Add(-time.Minute), 0)
			insertDashboardPowerReading(t, db, f.deviceIDs[1], &f.pointIDs[1], f.now.Add(-2*time.Minute), 0)
		}, wantPower: floatPtr(0)},
		{name: "signed values sum to zero", deviceCount: 2, wantDevices: 2, write: func(f *dashboardPowerFixture) {
			insertDashboardPowerReading(t, db, f.deviceIDs[0], &f.pointIDs[0], f.now.Add(-time.Minute), -1.25)
			insertDashboardPowerReading(t, db, f.deviceIDs[1], &f.pointIDs[1], f.now.Add(-2*time.Minute), 1.25)
		}, wantPower: floatPtr(0)},
		{name: "missing one", deviceCount: 2, wantDevices: 2, write: func(f *dashboardPowerFixture) {
			insertDashboardPowerReading(t, db, f.deviceIDs[0], &f.pointIDs[0], f.now.Add(-time.Minute), 2)
		}, wantPower: nil},
		{name: "stale", deviceCount: 1, wantDevices: 1, write: func(f *dashboardPowerFixture) {
			insertDashboardPowerReading(t, db, f.deviceIDs[0], &f.pointIDs[0], f.now.Add(-121*time.Second), 2)
		}, wantPower: nil},
		{name: "exactly 120 seconds", deviceCount: 1, wantDevices: 1, write: func(f *dashboardPowerFixture) {
			insertDashboardPowerReading(t, db, f.deviceIDs[0], &f.pointIDs[0], f.now.Add(-120*time.Second), 2)
		}, wantPower: floatPtr(2)},
		{name: "future", deviceCount: 1, wantDevices: 1, write: func(f *dashboardPowerFixture) {
			insertDashboardPowerReading(t, db, f.deviceIDs[0], &f.pointIDs[0], f.now.Add(time.Second), 2)
		}, wantPower: nil},
		{name: "wrong device", deviceCount: 1, wantDevices: 1, write: func(f *dashboardPowerFixture) {
			wrongDevice := insertDashboardDevice(t, db, strings.ReplaceAll(uuid.NewString(), "-", "")[:12], f.shopID, "wrong device", true)
			t.Cleanup(func() {
				db.Exec("DELETE FROM power_readings WHERE device_id = ?", wrongDevice)
				db.Exec("DELETE FROM devices WHERE id = ?", wrongDevice)
			})
			insertDashboardPowerReading(t, db, wrongDevice, &f.pointIDs[0], f.now.Add(-time.Minute), 2)
		}, wantPower: nil},
		{name: "wrong measurement point", deviceCount: 1, wantDevices: 1, write: func(f *dashboardPowerFixture) {
			wrong := uuid.New()
			if err := db.Exec("INSERT INTO measurement_points (id, shop_id, name) VALUES (?, ?, ?)", wrong, f.shopID, "wrong point").Error; err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Exec("DELETE FROM measurement_points WHERE id = ?", wrong) })
			insertDashboardPowerReading(t, db, f.deviceIDs[0], &wrong, f.now.Add(-time.Minute), 2)
		}, wantPower: nil},
		{name: "legacy null measurement point", deviceCount: 1, wantDevices: 1, write: func(f *dashboardPowerFixture) {
			insertDashboardPowerReading(t, db, f.deviceIDs[0], nil, f.now.Add(-time.Minute), 2)
		}, wantPower: nil},
		{name: "null active power", deviceCount: 1, wantDevices: 1, write: func(f *dashboardPowerFixture) {
			insertDashboardPowerReading(t, db, f.deviceIDs[0], &f.pointIDs[0], f.now.Add(-time.Minute), nil)
		}, wantPower: nil},
		{name: "newer invalid does not displace older accepted", deviceCount: 1, wantDevices: 1, write: func(f *dashboardPowerFixture) {
			insertDashboardPowerReading(t, db, f.deviceIDs[0], &f.pointIDs[0], f.now.Add(-time.Minute), 2)
			wrong := uuid.New()
			if err := db.Exec("INSERT INTO measurement_points (id, shop_id, name) VALUES (?, ?, ?)", wrong, f.shopID, "wrong point").Error; err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Exec("DELETE FROM measurement_points WHERE id = ?", wrong) })
			insertDashboardPowerReading(t, db, f.deviceIDs[0], &wrong, f.now.Add(-30*time.Second), 99)
		}, wantPower: floatPtr(2)},
		{name: "newer null active power does not displace older accepted", deviceCount: 1, wantDevices: 1, write: func(f *dashboardPowerFixture) {
			// Both rows have the same device, measurement point, and currently
			// applicable assignment. The newer raw row is independently invalid
			// because active_power is NULL; the older fresh row must remain the
			// latest accepted value.
			insertDashboardPowerReading(t, db, f.deviceIDs[0], &f.pointIDs[0], f.now.Add(-time.Minute), 2)
			insertDashboardPowerReading(t, db, f.deviceIDs[0], &f.pointIDs[0], f.now.Add(-30*time.Second), nil)
		}, wantPower: floatPtr(2)},
		{name: "before valid from", deviceCount: 1, wantDevices: 1, write: func(f *dashboardPowerFixture) {
			from := f.now.Add(-30 * time.Second)
			if err := db.Exec("UPDATE device_assignments SET valid_from = ? WHERE device_id = ?", from, f.deviceIDs[0]).Error; err != nil {
				t.Fatal(err)
			}
			insertDashboardPowerReading(t, db, f.deviceIDs[0], &f.pointIDs[0], f.now.Add(-90*time.Second), 2)
		}, wantPower: nil},
		{name: "after valid to", deviceCount: 1, wantDevices: 1, write: func(f *dashboardPowerFixture) {
			to := f.now.Add(30 * time.Second)
			if err := db.Exec("UPDATE device_assignments SET valid_to = ? WHERE device_id = ?", to, f.deviceIDs[0]).Error; err != nil {
				t.Fatal(err)
			}
			insertDashboardPowerReading(t, db, f.deviceIDs[0], &f.pointIDs[0], f.now.Add(60*time.Second), 2)
		}, wantPower: nil},
		{name: "no current assignments", deviceCount: 1, wantDevices: 0, write: func(f *dashboardPowerFixture) {
			if err := db.Exec("DELETE FROM device_assignments WHERE device_id = ?", f.deviceIDs[0]).Error; err != nil {
				t.Fatal(err)
			}
			insertDashboardPowerReading(t, db, f.deviceIDs[0], &f.pointIDs[0], f.now.Add(-time.Minute), 2)
		}, wantPower: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newDashboardPowerFixture(t, db, tc.deviceCount)
			tc.write(&fixture)
			projection, err := NewDashboardQueryRepository(db).FindDashboard(context.Background(), fixture.userID, fixture.shopID, func() time.Time { return fixture.now })
			if err != nil {
				t.Fatal(err)
			}
			if len(projection.Devices) != tc.wantDevices {
				t.Fatalf("devices=%d, want %d", len(projection.Devices), tc.wantDevices)
			}
			if (projection.CurrentPowerW == nil) != (tc.wantPower == nil) {
				t.Fatalf("current power=%v, want %v", projection.CurrentPowerW, tc.wantPower)
			}
			if tc.wantPower != nil && *projection.CurrentPowerW != *tc.wantPower {
				t.Fatalf("current power=%v, want %v", *projection.CurrentPowerW, *tc.wantPower)
			}
		})
	}
}

func TestDashboardCurrentPowerIsolatedAcrossAssignmentHistory(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newDashboardPowerFixture(t, db, 1)
	deviceID := fixture.deviceIDs[0]
	currentPoint := fixture.pointIDs[0]
	currentFrom := fixture.now.Add(-time.Minute)
	if err := db.Exec("UPDATE device_assignments SET valid_from = ? WHERE device_id = ? AND measurement_point_id = ?", currentFrom, deviceID, currentPoint).Error; err != nil {
		t.Fatal(err)
	}
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	otherClient := insertQueryClient(t, db, "b7b-history-"+suffix)
	otherShop := insertQueryShop(t, db, otherClient, "b7b-history-"+suffix, true)
	otherPoint := uuid.New()
	if err := db.Exec("INSERT INTO measurement_points (id, shop_id, name) VALUES (?, ?, ?)", otherPoint, otherShop, "old point").Error; err != nil {
		t.Fatal(err)
	}
	insertDashboardAssignment(t, db, deviceID, otherPoint, fixture.now.Add(-2*time.Minute), timePtr(currentFrom))
	insertDashboardPowerReading(t, db, deviceID, &otherPoint, fixture.now.Add(-90*time.Second), 99)
	insertDashboardPowerReading(t, db, deviceID, &currentPoint, fixture.now.Add(-30*time.Second), 3)
	t.Cleanup(func() {
		db.Exec("DELETE FROM power_readings WHERE device_id = ?", deviceID)
		db.Exec("DELETE FROM device_assignments WHERE device_id = ?", deviceID)
		db.Exec("DELETE FROM measurement_points WHERE id = ?", otherPoint)
		db.Exec("DELETE FROM shops WHERE id = ?", otherShop)
		db.Exec("DELETE FROM clients WHERE id = ?", otherClient)
	})
	projection, err := NewDashboardQueryRepository(db).FindDashboard(context.Background(), fixture.userID, fixture.shopID, func() time.Time { return fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Devices) != 1 || projection.CurrentPowerW == nil || *projection.CurrentPowerW != 3 {
		t.Fatalf("history leaked into projection: devices=%v power=%v", projection.Devices, projection.CurrentPowerW)
	}
}

func floatPtr(value float64) *float64 { return &value }
