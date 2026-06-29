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
	ID         uint      `gorm:"primaryKey"`
	ShopID     uint      `gorm:"index"`
	TypeID     uint      `gorm:"index"`
	
	MacAddress string    `gorm:"uniqueIndex;size:17;not null"`
	Name       string    `gorm:"size:100;not null"` // devicecode/devicename
	
	// [整合舊欄位]
	Location   string    `gorm:"size:100"`          // 安裝位置 (location)
	Memo       string    // 備註
	
	IsOnline   bool      `gorm:"default:false"`
	LastSeen   *time.Time
	CreatedAt  time.Time
	
	AlertSettings DeviceAlertSetting `gorm:"foreignKey:DeviceID;constraint:OnDelete:CASCADE"`
}

// DeviceAlertSetting 警報設定
type DeviceAlertSetting struct {
	ID       uint `gorm:"primaryKey"`
	DeviceID uint `gorm:"uniqueIndex"`
	
	DailyLimitKwh   *float64
	MonthlyLimitKwh *float64
	NonUsageStartTime string
	NonUsageEndTime   string
	
	IsEnabled bool `gorm:"default:true"`
	UpdatedAt time.Time
}