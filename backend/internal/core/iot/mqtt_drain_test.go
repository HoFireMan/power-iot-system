package iot

import (
	"context"
	"testing"
	"time"
)

func TestMQTTDrainBlocksIngressAndReconnectReadiness(t *testing.T) {
	client := &fakeMQTTClient{connectToken: readyTestToken(nil)}
	service := &MqttService{client: client, work: make(chan queuedMessage, 1), config: MqttConfig{TelemetryTopic: TelemetryTopic, CommandPrefix: CommandPrefix}}
	service.setConnected(true)
	service.setSubscriptionsReady(true)
	if err := service.StopIngestion(context.Background()); err != nil {
		t.Fatalf("StopIngestion: %v", err)
	}
	if !service.IngestionBlocked() || service.Ready() {
		t.Fatalf("drained service state: blocked=%t ready=%t", service.IngestionBlocked(), service.Ready())
	}
	service.handleMessage(nil, testMessage{topic: TelemetryTopic, body: []byte(`{}`)})
	select {
	case <-service.work:
		t.Fatal("message entered queue after MQTT drain")
	default:
	}
	service.onConnect(client)
	if !service.IngestionBlocked() {
		t.Fatal("reconnect race cleared the ingestion block")
	}
	if !service.Ready() {
		t.Fatal("broker dependency should remain independently ready after drain")
	}
}

func TestMQTTBoundedSmokeOnlyAdmitsTelemetryTopic(t *testing.T) {
	service := &MqttService{work: make(chan queuedMessage, 2), config: MqttConfig{TelemetryTopic: TelemetryTopic}, smokeOnly: true}
	service.handleMessage(nil, testMessage{topic: StatusTopic, body: []byte(`{}`)})
	select {
	case <-service.work:
		t.Fatal("status message entered bounded smoke queue")
	default:
	}
	service.handleMessage(nil, testMessage{topic: TelemetryTopic, body: []byte(`{}`)})
	select {
	case <-service.work:
	default:
		t.Fatal("approved telemetry message did not enter bounded smoke queue")
	}
}

func TestMQTTDrainWaitsForTrackedMessage(t *testing.T) {
	service := &MqttService{work: make(chan queuedMessage, 1), config: MqttConfig{TelemetryTopic: TelemetryTopic}}
	service.inFlight.Add(1)
	done := make(chan error, 1)
	go func() { done <- service.StopIngestion(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("drain completed before in-flight work: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	service.inFlight.Done()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("drain: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("drain did not wait for in-flight work")
	}
}
