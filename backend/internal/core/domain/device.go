package domain

import (
	"time"
)

// DeviceType 設備類別 (對應 basic.systag 或 basic.devicetype)
type DeviceType struct {
	ID      uint   `gorm:"primaryKey"`
	Name    string `gorm:"not null;size:50"` // 電器類型名稱
	Code    string `gorm:"size:50"`          // 電器類型編碼 (devicetypecode)
	IconKey string `gorm:"size:50"`
}

// Device 設備 (對應 basic.device)
type Device struct {
	ID     uint `gorm:"primaryKey"`
	ShopID uint `gorm:"index"`
	// InventoryOwnerClientID is authoritative inventory tenancy. ShopID is a
	// compatibility placement field and must not authorize or derive tenancy.
	InventoryOwnerClientID *uint `gorm:"column:inventory_owner_client_id;index"`
	TypeID                 uint  `gorm:"index"`

	// MacAddress is stored in canonical form: uppercase, no separators, 12 hex chars.
	// Canonicalization and uniqueness are enforced by versioned SQL migrations.
	MacAddress   string  `gorm:"size:17;not null"`
	SerialNumber *string `gorm:"column:serial_number;size:128"`
	Name         string  `gorm:"size:100;not null"` // devicecode/devicename

	// [整合舊欄位]
	Location string `gorm:"size:100"` // 安裝位置 (location)
	Memo     string // 備註

	IsOnline        bool `gorm:"default:false"`
	LastSeen        *time.Time
	BootID          string `gorm:"size:80"`
	FirmwareVersion string `gorm:"column:firmware_version;size:80"`
	IPAddress       string `gorm:"column:ip_address;size:45"`
	RSSI            *int   `gorm:"column:rssi"`
	QueueCount      *int   `gorm:"column:queue_count"`
	SafeMode        bool   `gorm:"column:safe_mode;default:false"`
	TimeSynced      bool   `gorm:"column:time_synced;default:false"`
	CreatedAt       time.Time

	AlertSettings DeviceAlertSetting `gorm:"foreignKey:DeviceID;constraint:OnDelete:CASCADE"`
}

// DeviceAlertSetting 警報設定
type DeviceAlertSetting struct {
	ID       uint `gorm:"primaryKey"`
	DeviceID uint `gorm:"uniqueIndex"`

	DailyLimitKwh     *float64
	MonthlyLimitKwh   *float64
	NonUsageStartTime string
	NonUsageEndTime   string

	IsEnabled bool `gorm:"default:true"`
	UpdatedAt time.Time
}
