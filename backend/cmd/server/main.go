// #C:\Code\PowerWork\power-iot-system\backend\cmd\server\main.go
package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	// ✅ 正確引用：指向新的 domain package
	"power-iot-backend/internal/core/domain"
	// ✅ 正確引用：指向 IoT 服務 package
	"power-iot-backend/internal/core/iot"
)

func main() {
	// 1. 設定資料庫連線字串
	// 注意：如果 Go 跑在 Docker 內，host 應為 "db"；如果在 Docker 外 (本機開發)，host 為 "localhost"
	dsn := "host=localhost user=admin password=password dbname=power_iot port=5432 sslmode=disable TimeZone=Asia/Taipei"

	fmt.Println("🔄 正在連線資料庫...")
	var db *gorm.DB
	var err error

	// 簡單的重試機制，等待 DB 啟動
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
	// 2. 自動遷移 (Auto Migrate)
	// ==========================================
	fmt.Println("📦 正在建立資料庫表格結構 (Schema)...")
	// 使用 domain package 中的 Struct
	err = db.AutoMigrate(
		&domain.SystemConfig{},
		&domain.Client{},
		&domain.Shop{},
		&domain.User{},
		&domain.UserShopRelation{},
		&domain.DeviceType{},
		&domain.Device{},
		&domain.DeviceAlertSetting{},
		&domain.PowerReading{},
		&domain.AlertLog{},
		&domain.DailyUsage{},
	)
	if err != nil {
		log.Fatal("❌ 表格建立失敗: ", err)
	}
	fmt.Println("✅ 所有表格已成功建立！")

	// 3. 初始化基礎資料 (Seeding)
	initData(db)

	// ==========================================
	// 4. 啟動 MQTT 服務 📡
	// ==========================================
	// 連線到本地的 Mosquitto
	mqttService := iot.NewMqttService("tcp://localhost:1883", db)
	if err := mqttService.Connect(); err != nil {
		// 只是警告，不阻斷程式 (可能 MQTT 還沒開)
		log.Printf("⚠️ MQTT 連線失敗: %v", err)
	} else {
		// 訂閱主題
		mqttService.Subscribe()
	}

	// 5. 啟動 HTTP Server
	r := gin.Default()
	
	// 基礎健康檢查 API
	r.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "online", 
			"db": "connected",
			"version": "v1.0",
		})
	})

	fmt.Println("🚀 伺服器運行中: http://localhost:8080")
	r.Run(":8080")
}

// 初始化一些預設資料
func initData(db *gorm.DB) {
	// 1. 碳排係數
	var config domain.SystemConfig
	if err := db.First(&config, "key = ?", "carbon_factor").Error; err != nil {
		db.Create(&domain.SystemConfig{Key: "carbon_factor", Value: "0.474", Description: "台電電力排碳係數"})
	}

	// 2. 預設 Admin 用戶 (測試用)
	var user domain.User
	if err := db.First(&user, "account = ?", "admin").Error; err != nil {
		fmt.Println("👤 建立預設管理員帳號 (admin / 123456)...")
		db.Create(&domain.User{
			Account:      "admin",
			PasswordHash: "123456", // 實際專案請用 bcrypt 加密
			Name:         "系統管理員",
			IsAdmin:      true,
			CreatedAt:    time.Now(),
		})
	}
}

// ==========================================
// 5. 啟動指令
// ==========================================
//go run cmd/server/main.go