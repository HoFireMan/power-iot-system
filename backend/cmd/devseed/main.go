// Command devseed is deliberately explicit: it registers only the MAC supplied
// by the developer and is not used by the server for unknown-device handling.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/core/iot"
	"power-iot-backend/internal/data/migrations"
)

func main() {
	macFlag := flag.String("device-mac", "", "MAC printed by the device serial log")
	name := flag.String("device-name", "test-meter-01", "development device name")
	shopID := flag.Uint("shop-id", 0, "optional existing shop ID")
	measurementPointName := flag.String("measurement-point-name", "", "optional measurement point name for a v1 ingest fixture")
	assignmentFrom := flag.String("assignment-from", "", "optional assignment start in RFC3339 format; defaults to now")
	flag.Parse()
	mac, err := iot.NormalizeMAC(firstNonEmpty(*macFlag, os.Getenv("DEV_DEVICE_MAC")))
	if err != nil {
		log.Fatal(err)
	}
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatal(err)
	}
	if err := migrations.Bootstrap(dsn); err != nil {
		log.Fatal("schema admission/bootstrap failed: ", err)
	}
	registrationMessage, fixtureMessage, err := seedFixtures(context.Background(), db, mac, *name, uint(*shopID), *measurementPointName, *assignmentFrom)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(registrationMessage)
	fmt.Print(fixtureMessage)
}

func seedFixtures(ctx context.Context, db *gorm.DB, mac, name string, shopID uint, measurementPointName, assignmentFrom string) (registrationMessage, fixtureMessage string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var device domain.Device
	var point domain.MeasurementPoint
	var validFrom time.Time
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := migrations.AcquireSharedWriterFenceOnGORM(ctx, tx); err != nil {
			return err
		}
		result := tx.Where("mac_address = ?", mac).First(&device)
		if result.Error == gorm.ErrRecordNotFound {
			device = domain.Device{MacAddress: mac, Name: name, ShopID: shopID}
			if err := tx.Create(&device).Error; err != nil {
				return err
			}
			registrationMessage = fmt.Sprintf("registered development device %s (%s)\n", mac, name)
		} else if result.Error != nil {
			return result.Error
		} else {
			registrationMessage = fmt.Sprintf("device already registered: %s (%s)\n", mac, device.Name)
		}
		if strings.TrimSpace(measurementPointName) == "" {
			return nil
		}
		if shopID == 0 {
			return fmt.Errorf("-shop-id is required when -measurement-point-name is provided")
		}
		validFrom = time.Now().UTC()
		if strings.TrimSpace(assignmentFrom) != "" {
			validFrom, err = time.Parse(time.RFC3339, strings.TrimSpace(assignmentFrom))
			if err != nil {
				return fmt.Errorf("invalid -assignment-from: %w", err)
			}
		}
		result = tx.Where("shop_id = ? AND name = ?", shopID, measurementPointName).First(&point)
		if result.Error == gorm.ErrRecordNotFound {
			point = domain.MeasurementPoint{ID: uuid.New(), ShopID: shopID, Name: measurementPointName}
			if err := tx.Create(&point).Error; err != nil {
				return err
			}
		} else if result.Error != nil {
			return result.Error
		}
		var assignment domain.DeviceAssignment
		result = tx.Where("device_id = ? AND measurement_point_id = ? AND valid_to IS NULL", device.ID, point.ID).First(&assignment)
		if result.Error == gorm.ErrRecordNotFound {
			assignment = domain.DeviceAssignment{ID: uuid.New(), DeviceID: device.ID, MeasurementPointID: point.ID, ValidFrom: validFrom}
			if err := tx.Create(&assignment).Error; err != nil {
				return err
			}
		} else if result.Error != nil {
			return result.Error
		}
		fixtureMessage = fmt.Sprintf("development telemetry fixture: device=%d measurement_point=%s assignment_from=%s\n", device.ID, point.ID, validFrom.UTC().Format(time.RFC3339))
		return nil
	})
	return registrationMessage, fixtureMessage, err
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
