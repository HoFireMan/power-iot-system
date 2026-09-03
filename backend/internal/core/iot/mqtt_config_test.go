package iot_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"power-iot-backend/internal/core/iot"
)

func TestLoadMqttConfigRequiresTLSCAAndCredentials(t *testing.T) {
	base := map[string]string{
		"MQTT_BROKER_URL": "tls://mqtt.example.test:8883",
		"MQTT_CA_FILE":    "/tmp/test-ca.pem",
		"MQTT_USERNAME":   "test-user",
		"MQTT_PASSWORD":   "test-password",
		"D6_RUNTIME_MODE": "POST_CUTOVER",
	}
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{name: "non TLS broker", key: "MQTT_BROKER_URL", value: "mqtt://mqtt.example.test:1883"},
		{name: "missing CA", key: "MQTT_CA_FILE", value: ""},
		{name: "missing username", key: "MQTT_USERNAME", value: ""},
		{name: "missing password", key: "MQTT_PASSWORD", value: ""},
	}
	for key, value := range base {
		t.Setenv(key, value)
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			for key, value := range base {
				t.Setenv(key, value)
			}
			t.Setenv(test.key, test.value)
			if _, err := iot.LoadMqttConfigFromEnv(); err == nil {
				t.Fatal("LoadMqttConfigFromEnv unexpectedly accepted invalid configuration")
			}
		})
	}
}

func TestNewMqttServiceWithConfigPropagatesCASetupFailure(t *testing.T) {
	service, err := iot.NewMqttServiceWithConfig(iot.MqttConfig{
		BrokerURL: "tls://mqtt.example.test:8883",
		CAFile:    filepath.Join(t.TempDir(), "missing-ca.pem"),
		Username:  "test-user",
		Password:  "test-password",
	}, nil)
	if err == nil {
		t.Fatal("NewMqttServiceWithConfig unexpectedly accepted an unreadable CA")
	}
	if service != nil {
		t.Fatal("NewMqttServiceWithConfig returned a partial service after setup failure")
	}
}

func TestNewMqttServiceWithConfigPreservesD6IngestionModes(t *testing.T) {
	caFile := writeTestCA(t)
	for _, test := range []struct {
		name             string
		initialIngestion bool
		boundedSmoke     bool
		blocked          bool
	}{
		{name: "PRE_CUTOVER", blocked: true},
		{name: "POST_CUTOVER", initialIngestion: true, blocked: false},
		{name: "PRE_CUTOVER bounded smoke", boundedSmoke: true, blocked: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, err := iot.NewMqttServiceWithConfig(iot.MqttConfig{
				BrokerURL:               "tls://mqtt.example.test:8883",
				CAFile:                  caFile,
				Username:                "test-user",
				Password:                "test-password",
				InitialIngestionEnabled: test.initialIngestion,
				BoundedSmokeEnabled:     test.boundedSmoke,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := service.IngestionBlocked(); got != test.blocked {
				t.Fatalf("IngestionBlocked()=%v, want %v", got, test.blocked)
			}
		})
	}
}

func writeTestCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "test CA"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
