package domain

import (
	"time"

	"github.com/google/uuid"
)

// Client 客戶別 (對應 basic.company / basic.branch 的上層)
type Client struct {
	ID        uint      `gorm:"primaryKey"`
	Name      string    `gorm:"not null;size:100"` // e.g., 華山商圈
	Code      string    `gorm:"uniqueIndex;size:50"` // e.g., ant10
	Shops     []Shop    `gorm:"foreignKey:ClientID"`
	CreatedAt time.Time
}

// Shop 店家 (對應 basic.branch)
type Shop struct {
	ID         uint      `gorm:"primaryKey"`
	ClientID   uint      `gorm:"index"`
	
	// [整合舊欄位]
	Code       string    `gorm:"uniqueIndex;size:50"` // 店家編碼 (branchcode)
	Name       string    `gorm:"not null;size:100"`   // 店家名稱 (branchname)
	Address    string    // 地址
	Phone      string    // 電話
	IsHead     bool      `gorm:"default:false"`       // 是否為總部 (ishead)
	Memo       string    // 備註 (memo)
	
	InviteUUID uuid.UUID `gorm:"type:uuid;default:gen_random_uuid();uniqueIndex"`
	IsActive   bool      `gorm:"default:true"`        // 狀態 (status: 1=true, 9=false)
	CreatedAt  time.Time
	UpdatedAt  time.Time // 對應 modifytm
	
	Devices []Device `gorm:"foreignKey:ShopID"`
}