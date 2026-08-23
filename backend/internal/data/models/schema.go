package models

import (
	"time"

	"github.com/google/uuid"
)

// ==========================================
// 1. 系統與組織模組 (Identity Context)
// ==========================================

// SystemConfig 系統全域設定
type SystemConfig struct {
	Key         string `gorm:"primaryKey;size:50"`
	Value       string `gorm:"not null;size:100"`
	Description string
}

// Client 客戶別 (對應 basic.company / basic.branch 的上層)
type Client struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"not null;size:100"`   // e.g., 華山商圈
	Code      string `gorm:"uniqueIndex;size:50"` // e.g., ant10
	Shops     []Shop `gorm:"foreignKey:ClientID"`
	CreatedAt time.Time
}

// Shop 店家 (對應 basic.branch)
type Shop struct {
	ID       uint `gorm:"primaryKey"`
	ClientID uint `gorm:"index"`

	// [整合舊欄位]
	Code    string `gorm:"uniqueIndex;size:50"` // 店家編碼 (branchcode)
	Name    string `gorm:"not null;size:100"`   // 店家名稱 (branchname)
	Address string // 地址
	Phone   string // 電話
	IsHead  bool   `gorm:"default:false"` // 是否為總部 (ishead)
	Memo    string // 備註 (memo)

	InviteUUID uuid.UUID `gorm:"type:uuid;default:gen_random_uuid();uniqueIndex"`
	IsActive   bool      `gorm:"default:true"` // 狀態 (status: 1=true, 9=false)
	CreatedAt  time.Time
	UpdatedAt  time.Time // 對應 modifytm

	Devices []Device `gorm:"foreignKey:ShopID"`
}

// User 使用者 (對應 basic.employee 概念)
type User struct {
	ID           uint   `gorm:"primaryKey"`
	Account      string `gorm:"uniqueIndex;size:50;not null"`
	PasswordHash string `gorm:"not null"`
	Name         string `gorm:"size:50;not null"`
	Email        string `gorm:"size:100"`
	Phone        string `gorm:"size:20"`
	IsAdmin      bool   `gorm:"default:false"`
	// AuthEnabled maps directly to users.auth_enabled and defaults closed.
	AuthEnabled   bool  `gorm:"column:auth_enabled;not null;default:false"`
	CurrentShopID *uint `gorm:"index"`
	CreatedAt     time.Time
	UpdatedAt     time.Time

	ShopRelations []UserShopRelation `gorm:"foreignKey:UserID"`
}

// UserShopRelation 使用者與店家關聯
type UserShopRelation struct {
	ID        uint   `gorm:"primaryKey"`
	UserID    uint   `gorm:"uniqueIndex:idx_user_shop"`
	ShopID    uint   `gorm:"uniqueIndex:idx_user_shop"`
	ShopRole  string `gorm:"size:20;default:'staff'"`
	CreatedAt time.Time
}

// ==========================================
// 2. 物聯網資產模組 (IoT Context)
// ==========================================

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

// AlertLog 警報紀錄 (對應 basic.warn)
// 這裡我們雖然正規化了，但為了保留「案發當下」的數據，還是會存 Voltage/Current
type AlertLog struct {
	ID       uint64 `gorm:"primaryKey"`
	DeviceID uint   `gorm:"index;not null"`

	Type    string `gorm:"size:50"` // 警報類型 (e.g., OVER_USAGE, CURFEW)
	Message string // 備註/訊息 (memo)

	// 案發當下的快照 (Snapshot)
	Voltage float64 `gorm:"type:numeric(5,2)"`
	Current float64 `gorm:"type:numeric(5,2)"`
	Power   float64 `gorm:"type:numeric(8,2)"`

	IsRead    bool      `gorm:"default:false"` // 是否已讀
	CreatedAt time.Time `gorm:"index"`         // 發生時間
}

// ==========================================
// 3. 數據模組 (Telemetry Context)
// ==========================================

// PowerReading mirrors domain.PowerReading for legacy consumers. Runtime
// migrations and MQTT processing use internal/core/domain as the canonical model.
// Keep these fields and tags in lockstep until this package is retired.
// PowerReading 電力數據 (Raw Data)
type PowerReading struct {
	ID                    uint64     `gorm:"primaryKey"`
	Time                  time.Time  `gorm:"column:time"`
	RecordedAt            time.Time  `gorm:"column:recorded_at;not null"`
	ReceivedAt            time.Time  `gorm:"column:received_at"`
	MeasurementPointID    *uuid.UUID `gorm:"column:measurement_point_id;index"`
	DeviceID              uint       `gorm:"index;not null"`
	Voltage               float64    `gorm:"type:numeric(5,2)"`
	Current               float64    `gorm:"type:numeric(5,2)"`
	Power                 float64    `gorm:"type:numeric(8,2)"`
	ActivePower           float64    `gorm:"column:active_power;type:numeric(8,2)"`
	KwhTotal              float64    `gorm:"type:numeric(10,3)"`
	EnergyDeltaKwh        *float64   `gorm:"type:numeric(10,6)"`
	PowerFactor           *float64   `gorm:"type:numeric(5,4)"`
	RSSI                  *int       `gorm:"column:rssi"`
	ProtocolVersion       int        `gorm:"default:0"`
	BootID                string     `gorm:"size:80"`
	BootCounter           *int64
	Sequence              *int64 `gorm:"column:sequence"`
	ValidSamples          *int   `gorm:"column:valid_samples"`
	InvalidSamples        *int   `gorm:"column:invalid_samples"`
	FirmwareVersion       string `gorm:"column:firmware_version;size:80"`
	LegacyFirmwareVersion string `gorm:"column:fw;size:80"`
}

// MeasurementPoint is the permanent logical identity for a monitored point.
type MeasurementPoint struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	ShopID    uint      `gorm:"column:shop_id;not null;index"`
	Name      string    `gorm:"size:100;not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// DeviceAssignment records the historical half-open [ValidFrom, ValidTo)
// relationship. Exclusion constraints are owned by SQL migrations.
type DeviceAssignment struct {
	ID                 uuid.UUID `gorm:"type:uuid;primaryKey"`
	DeviceID           uint      `gorm:"index;not null"`
	MeasurementPointID uuid.UUID `gorm:"column:measurement_point_id;index;not null"`
	ValidFrom          time.Time `gorm:"not null"`
	ValidTo            *time.Time
	CreatedAt          time.Time
}

// TelemetryIngestKey is the ordinary PostgreSQL idempotency boundary.
type TelemetryIngestKey struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey"`
	DeviceID    uint      `gorm:"not null"`
	BootCounter int64     `gorm:"not null"`
	Sequence    int64     `gorm:"column:sequence;not null"`
	CreatedAt   time.Time
	ReceivedAt  time.Time
}

// DailyUsage 每日用電統計 (對應 basic.report)
// 這是透過排程計算出來的結果，查詢速度快
type DailyUsage struct {
	ID       uint64 `gorm:"primaryKey"`
	Date     string `gorm:"index;size:10"`                      // YYYY-MM-DD
	DeviceID uint   `gorm:"index;uniqueIndex:idx_daily_device"` // 複合唯一索引: 每天每設備只有一筆

	KwhUsage float64 `gorm:"type:numeric(10,3)"` // 當日用電量 (degree)
	CarbonKg float64 `gorm:"type:numeric(10,3)"` // 當日碳排

	CreatedAt time.Time
	UpdatedAt time.Time
}
