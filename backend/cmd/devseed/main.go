// Command devseed is deliberately explicit: it registers only development
// fixtures after an explicit development/test admission check. It is not used
// by the server for unknown-device handling.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/core/iot"
	"power-iot-backend/internal/data/migrations"
	"power-iot-backend/internal/security"
)

const (
	devSeedEnableEnv   = "DEVSEED_ENABLE"
	devSeedPasswordEnv = "DEVSEED_PASSWORD"
	devSeedAccount     = "devseed"
	devSeedUserName    = "Development Seed User"
	devSeedClientCode  = "devseed-client"
	devSeedShopCode    = "devseed-shop"
	devSeedShopName    = "Development Seed Shop"
)

func main() {
	macFlag := flag.String("device-mac", "", "MAC printed by the device serial log")
	name := flag.String("device-name", "test-meter-01", "development device name")
	shopID := flag.Uint("shop-id", 0, "optional existing shop ID; defaults to the development shop")
	measurementPointName := flag.String("measurement-point-name", "", "optional measurement point name for a v1 ingest fixture")
	assignmentFrom := flag.String("assignment-from", "", "optional assignment start in RFC3339 format; defaults to now")
	flag.Parse()

	appEnv := os.Getenv("APP_ENV")
	if err := validateSeedGuard(appEnv, os.Getenv(devSeedEnableEnv)); err != nil {
		log.Fatal(err)
	}
	password, err := readDevelopmentPassword(os.Getenv(devSeedPasswordEnv))
	if err != nil {
		log.Fatal(err)
	}
	mac, err := iot.NormalizeMAC(firstNonEmpty(*macFlag, os.Getenv("DEV_DEVICE_MAC")))
	if err != nil {
		log.Fatal(err)
	}
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}
	db, err := openDevseedDatabase(dsn)
	if err != nil {
		log.Fatal("database connection failed")
	}
	admission, err := migrations.BootstrapAndAdmit(context.Background(), dsn)
	if err != nil {
		log.Fatalf("schema admission refused: disposition=%s state=%s: %v", admission.Disposition, admission.State, err)
	}
	if admission.Disposition != migrations.RuntimeServeV6 {
		log.Fatalf("schema admission refused: disposition=%s state=%s", admission.Disposition, admission.State)
	}
	shopIDForFixture, err := seedDevelopmentIdentity(context.Background(), db, password)
	if err != nil {
		log.Fatal(err)
	}
	if *shopID == 0 {
		*shopID = shopIDForFixture
	}
	registrationMessage, fixtureMessage, err := seedFixtures(context.Background(), db, mac, *name, uint(*shopID), *measurementPointName, *assignmentFrom)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(registrationMessage)
	fmt.Print(fixtureMessage)
}

func openDevseedDatabase(dsn string) (*gorm.DB, error) {
	return gorm.Open(postgres.Open(dsn), devseedGORMConfig())
}

func devseedGORMConfig() *gorm.Config {
	return &gorm.Config{Logger: logger.Discard}
}

func validateSeedGuard(appEnv, enabled string) error {
	if appEnv != "development" && appEnv != "test" {
		return errors.New("development seed refused: APP_ENV must be explicitly development or test")
	}
	if enabled != "true" {
		return errors.New("development seed refused: DEVSEED_ENABLE=true is required")
	}
	return nil
}

// readDevelopmentPassword requires an explicitly supplied runtime secret.
// Interactive input is deliberately unsupported so the command never reads an
// unbounded, echoed terminal password. The password is never included in
// output or errors.
func readDevelopmentPassword(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("development seed password is required via DEVSEED_PASSWORD")
	}
	return value, nil
}

func seedDevelopmentIdentity(ctx context.Context, db *gorm.DB, password string) (uint, error) {
	if strings.TrimSpace(password) == "" {
		return 0, errors.New("development seed password is required")
	}
	passwordHash, err := security.HashPassword([]byte(password))
	if err != nil {
		return 0, errors.New("development seed password could not be hashed")
	}
	if db == nil {
		return 0, errors.New("development seed database is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var shop domain.Shop
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := migrations.AcquireSharedWriterFenceOnGORM(ctx, tx); err != nil {
			return err
		}
		var client domain.Client
		result := tx.Where("code = ?", devSeedClientCode).First(&client)
		if result.Error == gorm.ErrRecordNotFound {
			client = domain.Client{Name: "Development Seed Client", Code: devSeedClientCode}
			if err := tx.Create(&client).Error; err != nil {
				return err
			}
		} else if result.Error != nil {
			return result.Error
		} else if client.Name != "Development Seed Client" {
			return errors.New("development seed client code is already in use")
		}

		result = tx.Where("code = ?", devSeedShopCode).First(&shop)
		if result.Error == gorm.ErrRecordNotFound {
			shop = domain.Shop{ClientID: client.ID, Code: devSeedShopCode, Name: devSeedShopName, IsActive: true}
			if err := tx.Create(&shop).Error; err != nil {
				return err
			}
		} else if result.Error != nil {
			return result.Error
		} else if shop.Name != devSeedShopName || shop.ClientID != client.ID || !shop.IsActive {
			return errors.New("development seed shop code is already in use")
		}

		var user domain.User
		result = tx.Where("account = ?", devSeedAccount).First(&user)
		if result.Error == gorm.ErrRecordNotFound {
			currentShopID := shop.ID
			user = domain.User{
				Account: devSeedAccount, PasswordHash: passwordHash, Name: devSeedUserName,
				AuthEnabled: true, CurrentShopID: &currentShopID,
			}
			if err := tx.Create(&user).Error; err != nil {
				return err
			}
		} else if result.Error != nil {
			return result.Error
		} else {
			if user.Name != devSeedUserName || !user.AuthEnabled || user.IsAdmin || user.CurrentShopID == nil || *user.CurrentShopID != shop.ID {
				return errors.New("development seed account is already in use")
			}
		}

		var relation domain.UserShopRelation
		result = tx.Where("user_id = ? AND shop_id = ?", user.ID, shop.ID).First(&relation)
		if result.Error == gorm.ErrRecordNotFound {
			if err := tx.Create(&domain.UserShopRelation{UserID: user.ID, ShopID: shop.ID, ShopRole: "staff"}).Error; err != nil {
				return err
			}
		} else if result.Error != nil {
			return result.Error
		} else if relation.ShopRole != "staff" {
			return errors.New("development seed account relation role conflicts with staff")
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return shop.ID, nil
}

func seedFixtures(ctx context.Context, db *gorm.DB, mac, name string, shopID uint, measurementPointName, assignmentFrom string) (registrationMessage, fixtureMessage string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var device domain.Device
	var point domain.MeasurementPoint
	var targetShop domain.Shop
	var validFrom time.Time
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := migrations.AcquireSharedWriterFenceOnGORM(ctx, tx); err != nil {
			return err
		}
		if shopID > 0 {
			result := tx.First(&targetShop, shopID)
			if result.Error != nil {
				if result.Error == gorm.ErrRecordNotFound {
					return fmt.Errorf("shop %d is not available for development fixture", shopID)
				}
				return result.Error
			}
			if !targetShop.IsActive || targetShop.ClientID == 0 {
				return fmt.Errorf("shop %d is not a valid development fixture target", shopID)
			}
			var client domain.Client
			if err := tx.First(&client, targetShop.ClientID).Error; err != nil {
				return fmt.Errorf("shop %d has no valid client for development fixture", shopID)
			}
		}
		result := tx.Where("mac_address = ?", mac).First(&device)
		if result.Error == gorm.ErrRecordNotFound {
			device = domain.Device{MacAddress: mac, Name: name, ShopID: shopID}
			if shopID > 0 {
				clientID := targetShop.ClientID
				device.InventoryOwnerClientID = &clientID
			}
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
		if point.ShopID != targetShop.ID {
			return errors.New("measurement point is not owned by the development fixture shop")
		}
		if err := validateExistingDeviceFixture(tx, device, targetShop, point.ID); err != nil {
			return err
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

func validateExistingDeviceFixture(tx *gorm.DB, device domain.Device, targetShop domain.Shop, targetPointID uuid.UUID) error {
	if device.InventoryOwnerClientID != nil {
		if *device.InventoryOwnerClientID == 0 {
			return errors.New("development device has an invalid inventory owner")
		}
		var owner domain.Client
		if err := tx.First(&owner, *device.InventoryOwnerClientID).Error; err != nil {
			return errors.New("development device has an invalid inventory owner")
		}
		if owner.ID != targetShop.ClientID {
			return errors.New("development device inventory owner conflicts with fixture shop")
		}
	}
	if device.ShopID != 0 {
		var legacyShop domain.Shop
		if err := tx.First(&legacyShop, device.ShopID).Error; err != nil {
			return errors.New("development device legacy shop conflicts with fixture shop")
		}
		if legacyShop.ID != targetShop.ID || legacyShop.ClientID != targetShop.ClientID {
			return errors.New("development device legacy shop conflicts with fixture shop")
		}
	}

	var activeAssignments []domain.DeviceAssignment
	if err := tx.Where("device_id = ? AND valid_to IS NULL", device.ID).Find(&activeAssignments).Error; err != nil {
		return err
	}
	for _, assignment := range activeAssignments {
		if assignment.MeasurementPointID == targetPointID {
			continue
		}
		var assignedPoint domain.MeasurementPoint
		if err := tx.First(&assignedPoint, "id = ?", assignment.MeasurementPointID).Error; err != nil {
			return errors.New("development device has an invalid active assignment")
		}
		var assignedShop domain.Shop
		if err := tx.First(&assignedShop, assignedPoint.ShopID).Error; err != nil || assignedShop.ClientID == 0 || !assignedShop.IsActive {
			return errors.New("development device has an invalid active assignment authority")
		}
		var assignedClient domain.Client
		if err := tx.First(&assignedClient, assignedShop.ClientID).Error; err != nil {
			return errors.New("development device has an invalid active assignment authority")
		}
		return errors.New("development device is already assigned to another measurement point")
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
