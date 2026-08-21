package iot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"gorm.io/gorm"

	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/data/migrations"
)

type messagePublisher interface {
	Publish(topic string, qos byte, retained bool, payload interface{}) mqtt.Token
}

type queuedMessage struct {
	message    mqtt.Message
	receivedAt time.Time
	tracked    bool
}

type MqttService struct {
	client             mqtt.Client
	disconnect         func(uint)
	ackPublisher       messagePublisher
	db                 *gorm.DB // retained for the legacy compatibility path
	ingestor           *TelemetryIngestor
	config             MqttConfig
	work               chan queuedMessage
	workersOnce        sync.Once
	clock              func() time.Time
	stateMu            sync.RWMutex
	connected          bool
	subscriptionsReady bool
	ingestionBlocked   bool
	smokeOnly          bool
	inFlight           sync.WaitGroup
}

// NewMqttService is retained for callers that used the old constructor. New
// deployments should use NewMqttServiceWithConfig and a tls:// broker URL.
func NewMqttService(brokerURL string, db *gorm.DB) *MqttService {
	config := MqttConfig{BrokerURL: brokerURL, ClientID: "power-iot-backend-" + time.Now().UTC().Format("20060102150405.000000000"), TelemetryTopic: TelemetryTopic, CommandPrefix: CommandPrefix, ConnectTimeout: 10 * time.Second, WorkerCount: 4, QueueSize: 64}
	s := &MqttService{db: db, ingestor: NewTelemetryIngestor(db), config: config, work: make(chan queuedMessage, config.QueueSize), clock: func() time.Time { return time.Now().UTC() }, ingestionBlocked: false}
	client, err := newMQTTClient(config, s.onConnect, s.onConnectionLost)
	if err != nil {
		log.Printf("MQTT client setup failed: %v", err)
		return s
	}
	s.client = client
	s.disconnect = client.Disconnect
	return s
}

func (s *MqttService) Connect() error {
	s.setConnected(false)
	if s.client == nil {
		return errors.New("MQTT client is not configured")
	}
	token := s.client.Connect()
	if !token.Wait() {
		return errors.New("MQTT connection timed out")
	}
	if err := token.Error(); err != nil {
		s.setConnected(false)
		return err
	}
	return nil
}

// Ready reports whether MQTT is connected and all required subscriptions are established.
func (s *MqttService) Ready() bool {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	// MQTT dependency readiness is distinct from general ingestion admission.
	// PRE_CUTOVER may be broker-ready while every general writer remains denied.
	return s.connected && s.subscriptionsReady
}

// IngestionBlocked reports the operator-controlled denial state. Broker
// connectivity remains a separate concern: a connected broker is not an
// enabled application writer.
func (s *MqttService) IngestionBlocked() bool {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.ingestionBlocked
}

// StopIngestion denies callbacks, disconnects the client, and waits for
// already accepted messages to finish. A disconnected client cannot silently
// re-enable the old writer because onConnect checks the same denial state.
func (s *MqttService) StopIngestion(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.stateMu.Lock()
	s.ingestionBlocked = true
	s.connected = false
	s.subscriptionsReady = false
	s.stateMu.Unlock()
	if s.disconnect != nil {
		// Disconnect is idempotent for the Paho client and avoids relying on a
		// racy connection-state read during the drain boundary.
		s.disconnect(0)
	}
	wait := make(chan struct{})
	go func() {
		s.inFlight.Wait()
		close(wait)
	}()
	select {
	case <-wait:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("MQTT ingestion drain incomplete: %w", ctx.Err())
	}
}

// StartBoundedSmoke is the explicit, temporary smoke seam. It permits only
// the approved telemetry topic; it never opens general ingestion.
func (s *MqttService) StartBoundedSmoke() error {
	s.stateMu.Lock()
	s.ingestionBlocked = false
	s.smokeOnly = true
	s.stateMu.Unlock()
	return s.Connect()
}

// StartIngestion is the explicit POST_CUTOVER operator action. It is never
// called by the reconnect callback, so restart/re-entry cannot bypass the
// pre-cutover decision.
func (s *MqttService) StartIngestion() error {
	s.stateMu.Lock()
	s.ingestionBlocked = false
	s.smokeOnly = false
	s.stateMu.Unlock()
	return s.Connect()
}

func (s *MqttService) setConnected(connected bool) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.connected = connected
	if !connected {
		s.subscriptionsReady = false
	}
}

func (s *MqttService) setSubscriptionsReady(ready bool) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.subscriptionsReady = ready
}

// Subscribe subscribes at QoS 1 and starts a bounded worker pool. MQTT's
// callback only enqueues work; it never creates a goroutine per message.
func (s *MqttService) Subscribe() {
	if s.IngestionBlocked() {
		return
	}
	s.startWorkers()
	if !s.Ready() {
		s.subscribeTopics()
	}
}

func (s *MqttService) startWorkers() {
	s.workersOnce.Do(func() {
		count := s.config.WorkerCount
		if count <= 0 {
			count = 4
		}
		for i := 0; i < count; i++ {
			go func() {
				for message := range s.work {
					s.handleQueuedMessage(message)
				}
			}()
		}
	})
}

func (s *MqttService) subscribeTopics() bool {
	if s.client == nil {
		s.setSubscriptionsReady(false)
		return false
	}
	allReady := true
	for _, subscription := range []struct {
		topic   string
		qos     byte
		handler mqtt.MessageHandler
	}{
		{s.telemetryTopic(), 1, s.handleMessage},
		{StatusTopic, 1, s.handleMessage},
	} {
		var subscribed bool
		for attempt := 0; attempt < 3; attempt++ {
			token := s.client.Subscribe(subscription.topic, subscription.qos, subscription.handler)
			if token.WaitTimeout(5*time.Second) && token.Error() == nil {
				subscribed = true
				log.Printf("MQTT subscribed: %s", subscription.topic)
				break
			}
			if token.Error() != nil {
				log.Printf("MQTT subscribe failed for %s (attempt %d): %v", subscription.topic, attempt+1, token.Error())
			} else {
				log.Printf("MQTT subscribe timed out for %s (attempt %d)", subscription.topic, attempt+1)
			}
			if attempt < 2 {
				time.Sleep(time.Duration(1<<attempt) * 100 * time.Millisecond)
			}
		}
		if !subscribed {
			allReady = false
		}
	}
	s.setSubscriptionsReady(allReady)
	return allReady
}

func (s *MqttService) telemetryTopic() string {
	if s.config.TelemetryTopic == "" {
		return TelemetryTopic
	}
	return s.config.TelemetryTopic
}

func (s *MqttService) onConnect(client mqtt.Client) {
	// Broker dependency connectivity is allowed in PRE_CUTOVER. The message
	// callback enforces the separate ingestion admission state, so reconnect
	// cannot bypass the mode boundary.
	log.Printf("MQTT broker connected")
	s.setConnected(true)
	s.startWorkers()
	if !s.subscribeTopics() {
		log.Printf("MQTT readiness unavailable: required subscriptions are not ready")
	}
}

func (s *MqttService) onConnectionLost(_ mqtt.Client, err error) {
	s.setConnected(false)
	log.Printf("MQTT connection lost: %v", err)
}

func (s *MqttService) currentTime() time.Time {
	if s.clock != nil {
		return s.clock().UTC()
	}
	return time.Now().UTC()
}

func (s *MqttService) handleMessage(_ mqtt.Client, msg mqtt.Message) {
	s.stateMu.Lock()
	if s.ingestionBlocked || (s.smokeOnly && msg.Topic() != s.telemetryTopic()) {
		s.stateMu.Unlock()
		return
	}
	s.inFlight.Add(1)
	s.stateMu.Unlock()
	queued := queuedMessage{message: msg, receivedAt: s.currentTime(), tracked: true}
	select {
	case s.work <- queued:
	default:
		s.inFlight.Done()
		log.Printf("MQTT processing queue full; dropping message from %s", msg.Topic())
	}
}

func (s *MqttService) handleQueuedMessage(queued queuedMessage) {
	if queued.tracked {
		defer s.inFlight.Done()
	}
	msg := queued.message
	if msg.Topic() == s.telemetryTopic() {
		s.processTelemetryAt(msg.Payload(), queued.receivedAt)
		return
	}
	if strings.HasSuffix(msg.Topic(), "/status") {
		s.processStatus(msg.Topic(), msg.Payload(), queued.receivedAt)
	}
}

func (s *MqttService) processTelemetry(raw []byte) {
	s.processTelemetryAt(raw, s.currentTime())
}

func (s *MqttService) processTelemetryAt(raw []byte, receivedAt time.Time) {
	payload, err := DecodeTelemetry(raw)
	if err != nil {
		log.Printf("MQTT telemetry rejected: %v", err)
		publishDiagnosticRejection(s, raw, "invalid")
		return
	}
	mac, err := NormalizeMAC(payload.MacAddress)
	if err != nil {
		return
	}
	payload.MacAddress = mac
	if payload.ProtocolVersion == 1 {
		result, err := s.telemetryIngest(payload, receivedAt)
		if err != nil {
			log.Printf("telemetry transaction failed for %s: %v", mac, err)
			return
		}
		s.publishIngestResult(mac, payload, result)
		return
	}
	if err := s.storeLegacyTelemetry(payload, receivedAt); err != nil {
		log.Printf("legacy telemetry transaction failed for %s: %v", mac, err)
	}
}

// processData remains as a compatibility seam for old package-local callers.
func (s *MqttService) processData(data MqttPayload) { s.processTelemetryMust(data) }
func (s *MqttService) processTelemetryMust(data MqttPayload) {
	data = canonicalPayload(data)
	mac, err := NormalizeMAC(data.MacAddress)
	if err != nil {
		log.Printf("telemetry rejected: %v", err)
		return
	}
	data.MacAddress = mac
	if data.ProtocolVersion == 1 {
		result, err := s.telemetryIngest(data, s.currentTime())
		if err == nil {
			s.publishIngestResult(mac, data, result)
		}
		return
	}
	if err := s.storeLegacyTelemetry(data, s.currentTime()); err != nil {
		log.Printf("legacy telemetry transaction failed: %v", err)
	}
}

func canonicalPayload(data MqttPayload) MqttPayload {
	if data.Sequence == 0 && data.Seq != 0 {
		data.Sequence = data.Seq
	}
	if data.PowerFactor == 0 && data.PF != 0 {
		data.PowerFactor = data.PF
	}
	if data.FirmwareVersion == "" {
		data.FirmwareVersion = data.FW
	}
	return data
}

func (s *MqttService) telemetryIngest(data MqttPayload, receivedAt time.Time) (IngestResult, error) {
	if s.ingestor == nil {
		if s.db == nil {
			return IngestResult{Status: IngestFailed}, errors.New("database is not configured")
		}
		s.ingestor = NewTelemetryIngestor(s.db)
	}
	return s.ingestor.Ingest(data, receivedAt)
}

func (s *MqttService) publishIngestResult(mac string, data MqttPayload, result IngestResult) {
	switch result.Status {
	case IngestStored, IngestDuplicate, IngestUnknownDevice, IngestUnknownAssignment:
		s.publishAck(mac, TelemetryAck{BootCounter: data.BootCounter, Sequence: data.Sequence, Status: string(result.Status)})
	}
}

func (s *MqttService) storeLegacyTelemetry(data MqttPayload, receivedAt time.Time) error {
	data = canonicalPayload(data)
	if s.db == nil {
		return errors.New("database is not configured")
	}
	recordTime := telemetryTimeAt(data.Timestamp, receivedAt)
	return s.db.WithContext(context.Background()).Transaction(func(tx *gorm.DB) error {
		if err := migrations.AcquireSharedWriterFenceOnGORM(context.Background(), tx); err != nil {
			return err
		}
		var device domain.Device
		if err := findDevice(tx, data.MacAddress, &device); err != nil {
			return err
		}
		if err := tx.Model(&domain.Device{}).
			Where("id = ?", device.ID).
			Updates(map[string]interface{}{"is_online": true}).Error; err != nil {
			return err
		}
		if err := tx.Model(&domain.Device{}).
			Where("id = ? AND (last_seen IS NULL OR last_seen < ?)", device.ID, receivedAt).
			Update("last_seen", receivedAt).Error; err != nil {
			return err
		}
		reading := domain.PowerReading{Time: recordTime, RecordedAt: recordTime, ReceivedAt: receivedAt, DeviceID: device.ID, Voltage: data.Voltage, Current: data.Current, Power: data.Power, ActivePower: data.Power, KwhTotal: data.KwhTotal}
		if err := tx.Create(&reading).Error; err != nil {
			return err
		}
		if err := checkTelemetryAlerts(tx, device, data, recordTime); err != nil {
			return err
		}
		return nil
	})
}

func findDevice(tx *gorm.DB, mac string, device *domain.Device) error {
	return tx.Preload("AlertSettings").Where("upper(replace(replace(mac_address, ':', ''), '-', '')) = ?", mac).First(device).Error
}

func telemetryTime(timestamp int64) time.Time {
	return telemetryTimeAt(timestamp, time.Now().UTC())
}

func telemetryTimeAt(timestamp int64, fallback time.Time) time.Time {
	if timestamp < 1577836800 {
		return fallback.UTC()
	}
	return time.Unix(timestamp, 0).UTC()
}

func (s *MqttService) publishAck(mac string, ack TelemetryAck) {
	body, err := json.Marshal(ack)
	if err != nil {
		log.Printf("MQTT ACK encode failed: %v", err)
		return
	}
	publisher := s.ackPublisher
	if publisher == nil {
		publisher = s.client
	}
	if publisher == nil {
		log.Printf("MQTT ACK not published: client is not configured")
		return
	}
	prefix := s.config.CommandPrefix
	if prefix == "" {
		prefix = CommandPrefix
	}
	token := publisher.Publish(fmt.Sprintf("%s/%s/telemetry/ack", prefix, mac), 1, false, body)
	if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
		log.Printf("MQTT ACK publish failed for %s: %v", mac, token.Error())
	}
}

func publishDiagnosticRejection(s *MqttService, raw []byte, status string) {
	var candidate struct {
		Mac             string `json:"mac"`
		ProtocolVersion int    `json:"protocol_version"`
		BootCounter     int64  `json:"boot_counter"`
		Sequence        int64  `json:"seq"`
	}
	if json.Unmarshal(raw, &candidate) != nil || candidate.ProtocolVersion != 1 {
		return
	}
	mac, err := NormalizeMAC(candidate.Mac)
	if err != nil {
		return
	}
	s.publishAck(mac, TelemetryAck{BootCounter: candidate.BootCounter, Sequence: candidate.Sequence, Status: status})
}

func (s *MqttService) processStatus(topic string, raw []byte, receivedAt time.Time) {
	parts := strings.Split(topic, "/")
	if len(parts) != 3 {
		return
	}
	topicMAC, err := NormalizeMAC(parts[1])
	if err != nil {
		return
	}
	var status DeviceStatusPayload
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&status); err != nil {
		log.Printf("device status rejected for %s: %v", topicMAC, err)
		return
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		log.Printf("device status rejected for %s: trailing JSON data", topicMAC)
		return
	}
	statusMAC := strings.TrimSpace(status.MAC)
	if statusMAC == "" {
		statusMAC = strings.TrimSpace(status.DeviceID)
	}
	if statusMAC == "" {
		log.Printf("device status rejected for %s: missing MAC identity", topicMAC)
		return
	}
	statusMAC, err = NormalizeMAC(statusMAC)
	if err != nil || statusMAC != topicMAC {
		log.Printf("device status rejected for %s: payload identity does not match topic", topicMAC)
		return
	}
	if s.db == nil {
		return
	}
	if receivedAt.IsZero() {
		receivedAt = s.currentTime()
	} else {
		receivedAt = receivedAt.UTC()
	}
	updates := map[string]interface{}{"is_online": status.Online, "last_seen": receivedAt, "boot_id": status.BootID, "firmware_version": status.Firmware, "ip_address": status.IP, "rssi": status.RSSI, "queue_count": status.QueueCount, "safe_mode": status.SafeMode, "time_synced": status.TimeSynced}
	err = s.db.WithContext(context.Background()).Transaction(func(tx *gorm.DB) error {
		if err := migrations.AcquireSharedWriterFenceOnGORM(context.Background(), tx); err != nil {
			return err
		}
		return tx.Model(&domain.Device{}).
			Where("upper(replace(replace(mac_address, ':', ''), '-', '')) = ? AND (last_seen IS NULL OR last_seen < ?)", topicMAC, receivedAt).
			Updates(updates).Error
	})
	if err != nil {
		log.Printf("device status update failed for %s: %v", topicMAC, err)
	}
}

// PublishCommand sends only the explicitly supported non-destructive commands.
func (s *MqttService) PublishCommand(mac string, command CommandEnvelope) error {
	canonical, err := NormalizeMAC(mac)
	if err != nil {
		return err
	}
	if err := command.Validate(time.Now()); err != nil {
		return err
	}
	var publisher messagePublisher = s.client
	if s.ackPublisher != nil {
		publisher = s.ackPublisher
	}
	if publisher == nil {
		return errors.New("MQTT client is not configured")
	}
	body, err := json.Marshal(command)
	if err != nil {
		return err
	}
	prefix := s.config.CommandPrefix
	if prefix == "" {
		prefix = CommandPrefix
	}
	token := publisher.Publish(fmt.Sprintf("%s/%s/command", prefix, canonical), 1, false, body)
	if !token.WaitTimeout(5 * time.Second) {
		return errors.New("MQTT command publish timed out")
	}
	return token.Error()
}
