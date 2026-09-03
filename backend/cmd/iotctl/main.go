// Command iotctl publishes authenticated development maintenance commands.
// It intentionally has no factory-reset or queue-clearing operation.
package main

import (
	"flag"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"power-iot-backend/internal/core/iot"
)

func supportedActionUsage() string {
	return strings.Join(iot.SupportedCommandActions(), "|")
}

func main() {
	mac := flag.String("device-mac", "", "device MAC")
	action := flag.String("action", iot.DiagnosticsAction, supportedActionUsage())
	expiresIn := flag.Duration("expires-in", 5*time.Minute, "command lifetime")
	version := flag.String("version", "", "OTA version")
	url := flag.String("url", "", "OTA HTTPS URL")
	sha256 := flag.String("sha256", "", "OTA SHA-256")
	size := flag.Int64("size", 0, "OTA firmware size in bytes")
	force := flag.Bool("force", false, "OTA force flag")
	flag.Parse()

	config, err := iot.LoadMqttConfigFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	service, err := iot.NewMqttServiceWithConfig(config, nil)
	if err != nil {
		log.Fatal(err)
	}
	if err := service.Connect(); err != nil {
		log.Fatal(err)
	}
	command := iot.CommandEnvelope{CommandID: uuid.NewString(), Action: *action, ExpiresAt: time.Now().Add(*expiresIn).Unix(), Version: *version, URL: *url, SHA256: *sha256, Size: *size, Force: *force}
	if err := service.PublishCommand(*mac, command); err != nil {
		log.Fatal(err)
	}
	log.Printf("published %s command %s", command.Action, command.CommandID)
}
