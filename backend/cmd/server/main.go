package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	httpadapter "power-iot-backend/internal/adapters/http"
	applicationauth "power-iot-backend/internal/application/auth"
	applicationdashboard "power-iot-backend/internal/application/dashboard"
	applicationme "power-iot-backend/internal/application/me"
	applicationshops "power-iot-backend/internal/application/shops"
	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/core/iot"
	"power-iot-backend/internal/data/migrations"
	"power-iot-backend/internal/deployment"
	"power-iot-backend/internal/security"
)

func main() {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	httpAddr := envOr("HTTP_ADDR", ":8080")
	appEnv := envOr("APP_ENV", "development")

	// Load the host-managed Ed25519 keyring before bootstrap side effects. The
	// keyring is retained as the composition seam for the B3 auth application.
	// No key material is included in the failure log.
	authKeyring, err := security.LoadKeyringFromEnv()
	if err != nil {
		log.Fatal("JWT keyring configuration failed")
	}

	fmt.Printf("connecting to database (environment=%s)\n", appEnv)
	var db *gorm.DB
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

	admission, err := migrations.BootstrapAndAdmit(context.Background(), databaseURL)
	if err != nil {
		log.Fatalf("schema admission refused: disposition=%s state=%s: %v", admission.Disposition, admission.State, err)
	}
	if admission.Disposition != migrations.RuntimeServeV6 {
		log.Fatalf("schema admission refused: disposition=%s state=%s", admission.Disposition, admission.State)
	}
	// The bootstrap is an idempotent, single-key system seed under the shared
	// writer fence. General HTTP writes remain denied until the external D6
	// drain/cutover workflow proves every frozen gate.
	if err := initData(db); err != nil {
		log.Fatal("initial data bootstrap failed: ", err)
	}

	runtimeMode := envOr("D6_RUNTIME_MODE", "PRE_CUTOVER")
	if runtimeMode != "PRE_CUTOVER" && runtimeMode != "POST_CUTOVER" {
		log.Fatalf("D6_RUNTIME_MODE must be PRE_CUTOVER or POST_CUTOVER")
	}
	writeGate := deployment.NewWriteGate(runtimeMode != "POST_CUTOVER")
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

	// Request ID runs outside D6 so rejected mutations remain correlatable; the
	// Gin global safety middleware runs before the B5-A login handler.
	r := gin.New()
	r.Use(gin.Recovery(), httpadapter.RequestIDMiddleware())
	refreshLimiter := security.NewAbuseLimiter()
	loginRunner := applicationauth.NewGormTransactionRunnerWithConfig(db, applicationauth.Config{
		Signer: authKeyring, RefreshLimiter: refreshLimiter,
	})
	httpadapter.RegisterLoginRoute(r, httpadapter.LoginHandlerConfig{
		Runner:  loginRunner,
		Limiter: refreshLimiter,
	})
	httpadapter.RegisterRefreshRoute(r, httpadapter.RefreshHandlerConfig{Runner: loginRunner})
	authenticator := httpadapter.NewB3Authenticator(loginRunner)
	httpadapter.RegisterLogoutRoute(r, authenticator, httpadapter.LogoutHandlerConfig{Runner: loginRunner})
	httpadapter.RegisterMeRoute(r, authenticator, applicationme.NewGormQueryRunner(db))
	httpadapter.RegisterShopsRoute(r, authenticator, applicationshops.NewGormQueryRunner(db))
	httpadapter.RegisterDashboardRoute(r, authenticator, applicationdashboard.NewGormQueryRunner(db))
	r.GET("/", func(c *gin.Context) {
		mqttReady := mqttService.Ready()
		status := "degraded"
		if mqttReady {
			status = "online"
		}
		c.JSON(http.StatusOK, gin.H{"status": status, "db": "connected", "mqtt_ready": mqttReady, "mqtt_ingestion_blocked": mqttService.IngestionBlocked(), "d6_runtime_mode": runtimeMode, "http_writes_blocked": writeGate.Blocked(), "version": "v1.0"})
	})
	// Server order is request ID -> trusted client context -> D6 write gate ->
	// Gin route middleware/authentication/handlers.
	trustedProxyConfig := security.TrustedProxyConfig{}
	handler := httpadapter.ClientIPHTTPMiddleware(trustedProxyConfig, writeGate.Middleware(r))
	server := &http.Server{Addr: httpAddr, Handler: httpadapter.RequestIDHTTPMiddleware(handler)}
	shutdownSignal, stopSignal := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignal()
	go func() {
		log.Printf("HTTP server listening on %s (D6_RUNTIME_MODE=%s general writes blocked=%t)", httpAddr, runtimeMode, writeGate.Blocked())
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	<-shutdownSignal.Done()
	writeGate.Block()
	drainContext, cancelDrain := context.WithTimeout(context.Background(), 10*time.Second)
	if err := mqttService.StopIngestion(drainContext); err != nil {
		log.Printf("MQTT drain incomplete during shutdown: %v", err)
	}
	cancelDrain()
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownContext); err != nil {
		log.Printf("HTTP shutdown failed: %v", err)
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func initData(db *gorm.DB) error {
	err := db.WithContext(context.Background()).Transaction(func(tx *gorm.DB) error {
		if err := migrations.AcquireSharedWriterFenceOnGORM(context.Background(), tx); err != nil {
			return err
		}
		var config domain.SystemConfig
		if err := tx.First(&config, "key = ?", "carbon_factor").Error; err != nil {
			if createErr := tx.Create(&domain.SystemConfig{Key: "carbon_factor", Value: "0.474", Description: "台電電力排碳係數"}).Error; createErr != nil {
				return createErr
			}
		}
		return nil
	})
	return err
}
