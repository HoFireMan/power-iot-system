package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	// 引入我們定義的模型
	// ⚠️ 注意：這裡的路徑要根據您的 go.mod 模組名稱
	"power-iot-backend/internal/data/models"
)

func main() {
	// 1. 設定資料庫連線
	dsn := "host=localhost user=admin password=password dbname=power_iot port=5432 sslmode=disable TimeZone=Asia/Taipei"

	fmt.Println("🔄 正在連線資料庫...")
	var db *gorm.DB
	var err error

	for i := 0; i < 5; i++ {
		db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
		if err == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}

	if err != nil {
		log.Fatal("❌ 資料庫連線失敗: ", err)
	}
	fmt.Println("✅ 資料庫連線成功！")

	// ==========================================
	// 2. 自動遷移 (Auto Migrate) - 這是魔法發生的地方 ✨
	// ==========================================
	fmt.Println("📦 正在建立資料庫表格結構 (Schema)...")
	err = db.AutoMigrate(
		&models.SystemConfig{},
		&models.Client{},
		&models.Shop{},
		&models.User{},
		&models.UserShopRelation{},
		&models.DeviceType{},
		&models.Device{},
		&models.DeviceAlertSetting{},
		&models.PowerReading{},
	)
	if err != nil {
		log.Fatal("❌ 表格建立失敗: ", err)
	}
	fmt.Println("✅ 所有表格已成功建立！(Clients, Shops, Users, Devices...)")

	// 3. 初始化基礎資料 (Seeding)
	initData(db)

	// 4. 啟動 Server
	r := gin.Default()
	r.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "online", "db": "connected"})
	})

	fmt.Println("🚀 伺服器運行中: http://localhost:8080")
	r.Run(":8080")
}

// 初始化一些預設資料，讓前端有東西可以看
func initData(db *gorm.DB) {
	// 1. 碳排係數
	var config models.SystemConfig
	if err := db.First(&config, "key = ?", "carbon_factor").Error; err != nil {
		db.Create(&models.SystemConfig{Key: "carbon_factor", Value: "0.474", Description: "台電電力排碳係數"})
	}

	// 2. 預設 Admin 用戶 (測試用)
	var user models.User
	if err := db.First(&user, "account = ?", "admin").Error; err != nil {
		fmt.Println("👤 建立預設管理員帳號 (admin / 123456)...")
		// 注意：實際專案密碼必須 Hash，這裡先明碼方便測試
		db.Create(&models.User{
			Account:      "admin",
			PasswordHash: "123456",
			Name:         "系統管理員",
			IsAdmin:      true,
			CreatedAt:    time.Now(),
		})
	}
}
