package domain

import (
	"time"
)

// AlertLog 警報紀錄 (對應 basic.warn)
// 這裡我們雖然正規化了，但為了保留「案發當下」的數據，還是會存 Voltage/Current
type AlertLog struct {
	ID        uint64    `gorm:"primaryKey"`
	DeviceID  uint      `gorm:"index;not null"`
	
	Type      string    `gorm:"size:50"` // 警報類型 (e.g., OVER_USAGE, CURFEW)
	Message   string    // 備註/訊息 (memo)
	
	// 案發當下的快照 (Snapshot)
	Voltage   float64   `gorm:"type:numeric(5,2)"`
	Current   float64   `gorm:"type:numeric(5,2)"`
	Power     float64   `gorm:"type:numeric(8,2)"`
	
	IsRead    bool      `gorm:"default:false"` // 是否已讀
	CreatedAt time.Time `gorm:"index"`         // 發生時間
}

// PowerReading 電力數據 (Raw Data)
type PowerReading struct {
	ID        uint64    `gorm:"primaryKey"`
	Time      time.Time `gorm:"index;not null"`
	DeviceID  uint      `gorm:"index;not null"`
	
	Voltage   float64   `gorm:"type:numeric(5,2)"`
	Current   float64   `gorm:"type:numeric(5,2)"`
	Power     float64   `gorm:"type:numeric(8,2)"`
	KwhTotal  float64   `gorm:"type:numeric(10,3)"` // 設備回傳的累計度數
}

// DailyUsage 每日用電統計 (對應 basic.report)
// 這是透過排程計算出來的結果，查詢速度快
type DailyUsage struct {
	ID        uint64    `gorm:"primaryKey"`
	Date      string    `gorm:"index;size:10"` // YYYY-MM-DD
	DeviceID  uint      `gorm:"index;uniqueIndex:idx_daily_device"` // 複合唯一索引: 每天每設備只有一筆
	
	KwhUsage  float64   `gorm:"type:numeric(10,3)"` // 當日用電量 (degree)
	CarbonKg  float64   `gorm:"type:numeric(10,3)"` // 當日碳排
	
	CreatedAt time.Time
	UpdatedAt time.Time
}