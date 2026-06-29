package domain

// SystemConfig 系統全域設定
type SystemConfig struct {
	Key         string `gorm:"primaryKey;size:50"`
	Value       string `gorm:"not null;size:100"`
	Description string
}