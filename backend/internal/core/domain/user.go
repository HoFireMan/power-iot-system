package domain

import (
	"time"
)

// User 使用者 (對應 basic.employee 概念)
type User struct {
	ID            uint      `gorm:"primaryKey"`
	Account       string    `gorm:"uniqueIndex;size:50;not null"`
	PasswordHash  string    `gorm:"not null"`
	Name          string    `gorm:"size:50;not null"`
	Email         string    `gorm:"size:100"`
	Phone         string    `gorm:"size:20"`
	IsAdmin       bool      `gorm:"default:false"`
	CurrentShopID *uint     `gorm:"index"`
	CreatedAt     time.Time
	UpdatedAt     time.Time

	ShopRelations []UserShopRelation `gorm:"foreignKey:UserID"`
}

// UserShopRelation 使用者與店家關聯
type UserShopRelation struct {
	ID        uint      `gorm:"primaryKey"`
	UserID    uint      `gorm:"uniqueIndex:idx_user_shop"`
	ShopID    uint      `gorm:"uniqueIndex:idx_user_shop"`
	ShopRole  string    `gorm:"size:20;default:'staff'"`
	CreatedAt time.Time
}