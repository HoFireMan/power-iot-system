package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/core/iot"
	"power-iot-backend/internal/data/migrations"
)

func main() {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	httpAddr := envOr("HTTP_ADDR", ":8080")
	appEnv := envOr("APP_ENV", "development")

	fmt.Printf("connecting to database (environment=%s)\n", appEnv)
	var db *gorm.DB
	var err error
	for i := 0; i < 5; i++ {
		db, err = gorm.Open(postgres.Open(databaseURL), &gorm.Config{})
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		log.Fatal("database connection failed: ", err)
	}

	if err := migrations.Up(databaseURL); err != nil {
		log.Fatal("versioned schema migration failed: ", err)
	}
	initData(db)

	mqttConfig, err := iot.LoadMqttConfigFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	mqttService, err := iot.NewMqttServiceWithConfig(mqttConfig, db)
	if err != nil {
		log.Fatal("MQTT client setup failed: ", err)
	}
	// Keep HTTP liveness available while the MQTT client retries an unavailable broker.
	// Readiness remains degraded until the connection callback establishes every required subscription.
	go func() {
		if err := mqttService.Connect(); err != nil {
			log.Printf("MQTT connection failed: %v", err)
		}
	}()

	r := gin.Default()
	r.GET("/", func(c *gin.Context) {
		mqttReady := mqttService.Ready()
		status := "degraded"
		if mqttReady {
			status = "online"
		}
		c.JSON(http.StatusOK, gin.H{"status": status, "db": "connected", "mqtt_ready": mqttReady, "version": "v1.0"})
	})
	log.Printf("HTTP server listening on %s", httpAddr)
	if err := r.Run(httpAddr); err != nil {
		log.Fatal(err)
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func initData(db *gorm.DB) {
	var config domain.SystemConfig
	if err := db.First(&config, "key = ?", "carbon_factor").Error; err != nil {
		if createErr := db.Create(&domain.SystemConfig{Key: "carbon_factor", Value: "0.474", Description: "台電電力排碳係數"}).Error; createErr != nil {
			log.Printf("carbon factor seed failed: %v", createErr)
		}
	}
}
