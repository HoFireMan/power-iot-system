package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Client 客戶別 (對應 basic.company / basic.branch 的上層)
type Client struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"not null;size:100"`   // e.g., 華山商圈
	Code      string `gorm:"uniqueIndex;size:50"` // e.g., ant10
	Shops     []Shop `gorm:"foreignKey:ClientID"`
	CreatedAt time.Time
}

// CarbonFactorSet is versioned factor metadata. Rates are stored separately
// so future tariff sets can diverge without changing Shop classification.
type CarbonFactorSet struct {
	ID            uint64 `gorm:"primaryKey"`
	Organization  string `gorm:"size:200;not null"`
	DocumentTitle string `gorm:"column:document_title;size:200;not null"`
	SourceURL     string `gorm:"column:source_url;size:500;not null"`
	VersionYear   int    `gorm:"column:version_year;not null"`
	ROCYear       int    `gorm:"column:roc_year;not null"`
	Unit          string `gorm:"size:32;not null"`
	IsActive      bool   `gorm:"column:is_active;not null;default:true"`
	CreatedAt     time.Time
}

type CarbonFactorRate struct {
	ID         uint64 `gorm:"primaryKey"`
	SetID      uint64 `gorm:"column:set_id;not null"`
	TariffCode string `gorm:"column:tariff_code;size:32;not null"`
	// Numeric values remain decimal text at the persistence boundary.
	FactorKgCO2ePerKwh string `gorm:"column:factor_kgco2e_per_kwh;type:numeric(10,6);not null"`
}

const (
	TariffLightingCommercial    = "LIGHTING_COMMERCIAL"
	TariffLowVoltage            = "LOW_VOLTAGE"
	TariffHighVoltage           = "HIGH_VOLTAGE"
	TariffExtraHighVoltage      = "EXTRA_HIGH_VOLTAGE"
	TariffLightingNoncommercial = "LIGHTING_NONCOMMERCIAL"
	TariffPackageLighting       = "PACKAGE_LIGHTING"
)

func ValidElectricityTariff(value string) bool {
	if strings.TrimSpace(value) != value || value == "" {
		return false
	}
	_, ok := map[string]struct{}{
		TariffLightingCommercial: {}, TariffLowVoltage: {}, TariffHighVoltage: {},
		TariffExtraHighVoltage: {}, TariffLightingNoncommercial: {}, TariffPackageLighting: {},
	}[value]
	return ok
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
	// ElectricityTariff is an explicit, nullable tariff classification. It is
	// intentionally not defaulted: carbon is unavailable until an administrator
	// selects one of the supported classifications.
	ElectricityTariff *string `gorm:"column:electricity_tariff;size:32"`

	Devices []Device `gorm:"foreignKey:ShopID"`
}
