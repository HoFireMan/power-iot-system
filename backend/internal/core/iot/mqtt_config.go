package iot

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// MqttConfig contains all broker connection settings. No credentials are logged.
type MqttConfig struct {
	BrokerURL      string
	ClientID       string
	Username       string
	Password       string
	CAFile         string
	TelemetryTopic string
	CommandPrefix  string
	ConnectTimeout time.Duration
	WorkerCount    int
	QueueSize      int
}

// MQTTConfig is an acronym-friendly compatibility alias.
type MQTTConfig = MqttConfig

// LoadMqttConfigFromEnv loads the backend's documented environment contract.
func LoadMqttConfigFromEnv() (MqttConfig, error) {
	broker := strings.TrimSpace(os.Getenv("MQTT_BROKER_URL"))
	if broker == "" {
		return MqttConfig{}, fmt.Errorf("MQTT_BROKER_URL is required")
	}
	if !strings.HasPrefix(strings.ToLower(broker), "tls://") {
		return MqttConfig{}, fmt.Errorf("MQTT_BROKER_URL must use tls://")
	}
	clientID := strings.TrimSpace(os.Getenv("MQTT_CLIENT_ID"))
	if clientID == "" {
		clientID = "power-iot-backend-" + uuid.NewString()
	}
	caFile := strings.TrimSpace(os.Getenv("MQTT_CA_FILE"))
	if caFile == "" {
		return MqttConfig{}, fmt.Errorf("MQTT_CA_FILE is required for TLS broker")
	}
	if strings.TrimSpace(os.Getenv("MQTT_USERNAME")) == "" || os.Getenv("MQTT_PASSWORD") == "" {
		return MqttConfig{}, fmt.Errorf("MQTT_USERNAME and MQTT_PASSWORD are required")
	}
	return MqttConfig{
		BrokerURL: broker, ClientID: clientID, Username: os.Getenv("MQTT_USERNAME"), Password: os.Getenv("MQTT_PASSWORD"),
		CAFile: caFile, TelemetryTopic: envOr("MQTT_TELEMETRY_TOPIC", TelemetryTopic), CommandPrefix: envOr("MQTT_COMMAND_PREFIX", CommandPrefix),
		ConnectTimeout: 10 * time.Second, WorkerCount: 4, QueueSize: 64,
	}, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func newMQTTClient(config MqttConfig, onConnect mqtt.OnConnectHandler, onLost mqtt.ConnectionLostHandler) (mqtt.Client, error) {
	opts := mqtt.NewClientOptions().AddBroker(config.BrokerURL)
	opts.SetClientID(config.ClientID)
	opts.SetUsername(config.Username)
	opts.SetPassword(config.Password)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetConnectRetryInterval(2 * time.Second)
	opts.SetConnectTimeout(config.ConnectTimeout)
	opts.SetKeepAlive(30 * time.Second)
	opts.SetOnConnectHandler(onConnect)
	opts.SetConnectionLostHandler(onLost)
	if strings.HasPrefix(strings.ToLower(config.BrokerURL), "tls://") {
		roots := x509.NewCertPool()
		ca, err := os.ReadFile(filepath.Clean(config.CAFile))
		if err != nil {
			return nil, fmt.Errorf("read MQTT CA file: %w", err)
		}
		if !roots.AppendCertsFromPEM(ca) {
			return nil, fmt.Errorf("MQTT CA file contains no certificates")
		}
		u, err := url.Parse(config.BrokerURL)
		if err != nil || u.Hostname() == "" {
			return nil, fmt.Errorf("invalid MQTT broker URL")
		}
		opts.SetTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: u.Hostname()})
	}
	return mqtt.NewClient(opts), nil
}

// NewMqttServiceWithConfig creates the bounded, authenticated MQTT service.
func NewMqttServiceWithConfig(config MqttConfig, db *gorm.DB) (*MqttService, error) {
	if config.TelemetryTopic == "" {
		config.TelemetryTopic = TelemetryTopic
	}
	if config.CommandPrefix == "" {
		config.CommandPrefix = CommandPrefix
	}
	if config.ConnectTimeout <= 0 {
		config.ConnectTimeout = 10 * time.Second
	}
	if config.WorkerCount <= 0 {
		config.WorkerCount = 4
	}
	if config.QueueSize <= 0 {
		config.QueueSize = 64
	}
	s := &MqttService{db: db, ingestor: NewTelemetryIngestor(db), config: config, work: make(chan queuedMessage, config.QueueSize), clock: func() time.Time { return time.Now().UTC() }}
	client, err := newMQTTClient(config, s.onConnect, s.onConnectionLost)
	if err != nil {
		return nil, err
	}
	s.client = client
	return s, nil
}
