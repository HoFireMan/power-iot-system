package iot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/data/migrations"
)

type testToken struct {
	err  error
	done chan struct{}
}

func (t *testToken) Wait() bool { <-t.done; return true }
func (t *testToken) WaitTimeout(time.Duration) bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}
func (t *testToken) Done() <-chan struct{} { return t.done }
func (t *testToken) Error() error          { return t.err }

type recordedPublish struct {
	topic   string
	payload interface{}
}
type testPublisher struct {
	mu      sync.Mutex
	records []recordedPublish
}

func (p *testPublisher) Publish(topic string, _ byte, _ bool, payload interface{}) mqtt.Token {
	p.mu.Lock()
	p.records = append(p.records, recordedPublish{topic: topic, payload: payload})
	p.mu.Unlock()
	done := make(chan struct{})
	close(done)
	return &testToken{done: done}
}
func (p *testPublisher) count() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.records) }
func (p *testPublisher) last() recordedPublish {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.records[len(p.records)-1]
}

func TestQueueFullDropsWithoutUnboundedGoroutine(t *testing.T) {
	publisher := &testPublisher{}
	service := &MqttService{ackPublisher: publisher, config: MqttConfig{TelemetryTopic: TelemetryTopic}, work: make(chan queuedMessage, 1), clock: func() time.Time { return time.Unix(1786120000, 0).UTC() }}
	first := testMessage{topic: TelemetryTopic, body: []byte(`{}`)}
	service.handleMessage(nil, first)
	service.handleMessage(nil, first)
	if got := len(service.work); got != 1 {
		t.Fatalf("queue grew beyond bound: %d", got)
	}
}

func TestPublishCommandSupportsDiagnosticsAlias(t *testing.T) {
	for _, action := range []string{"diagnostics", "report_diagnostics"} {
		t.Run(action, func(t *testing.T) {
			publisher := &testPublisher{}
			service := &MqttService{ackPublisher: publisher, config: MqttConfig{CommandPrefix: CommandPrefix}}
			command := CommandEnvelope{CommandID: "cmd-unique", Action: action, ExpiresAt: time.Now().Add(time.Minute).Unix()}
			if err := service.PublishCommand("AABBCCDDEEFF", command); err != nil {
				t.Fatal(err)
			}
			if got := publisher.count(); got != 1 {
				t.Fatalf("published %d commands, want 1", got)
			}
			published := publisher.last()
			if published.topic != "device/AABBCCDDEEFF/command" {
				t.Fatalf("published topic %q", published.topic)
			}
			var got CommandEnvelope
			if err := json.Unmarshal(published.payload.([]byte), &got); err != nil {
				t.Fatal(err)
			}
			if got.Action != action || got.CommandID != command.CommandID || got.ExpiresAt != command.ExpiresAt {
				t.Fatalf("published command = %+v, want %+v", got, command)
			}
		})
	}
}

func TestTransactionFailureDoesNotPublishTerminalAck(t *testing.T) {
	publisher := &testPublisher{}
	service := &MqttService{db: nil, ackPublisher: publisher, config: MqttConfig{CommandPrefix: CommandPrefix}}
	service.processTelemetry([]byte(`{"mac":"AABBCCDDEEFF","v":110,"c":1,"p":100,"kwh":1,"ts":1786021200,"protocol_version":1,"boot_id":"boot","boot_counter":7,"seq":123,"energy_delta_kwh":0}`))
	if publisher.count() != 0 {
		t.Fatal("published terminal ACK after transaction setup failure")
	}
}

func TestInvalidV1EnergyDeltaPublishesOnlyDiagnosticRejection(t *testing.T) {
	for _, raw := range []string{
		`{"mac":"AABBCCDDEEFF","v":110,"c":1,"p":100,"kwh":1,"ts":1786021200,"protocol_version":1,"boot_counter":7,"seq":123}`,
		`{"mac":"AABBCCDDEEFF","v":110,"c":1,"p":100,"kwh":1,"ts":1786021200,"protocol_version":1,"boot_counter":7,"seq":123,"energy_delta_kwh":null}`,
	} {
		t.Run(raw, func(t *testing.T) {
			publisher := &testPublisher{}
			service := &MqttService{db: nil, ackPublisher: publisher, config: MqttConfig{CommandPrefix: CommandPrefix}}
			service.processTelemetry([]byte(raw))
			if publisher.count() != 1 {
				t.Fatalf("want one diagnostic rejection, got %d", publisher.count())
			}
			var ack TelemetryAck
			if err := json.Unmarshal(publisher.last().payload.([]byte), &ack); err != nil {
				t.Fatal(err)
			}
			if ack.Status != string(IngestInvalid) {
				t.Fatalf("invalid telemetry received unexpected ACK: %+v", ack)
			}
		})
	}
}

func TestPostgresLegacyTelemetryWriteCompatibility(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; database-backed protocol test not run")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureAlertsSchema(dsn); err != nil {
		t.Fatal(err)
	}
	mac := "A1B2C3D4E5F6"
	var shopID uint
	shopCode := "protocol-test-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := db.Raw(`INSERT INTO shops (code, name) VALUES (?, ?) RETURNING id`, shopCode, "Protocol Test Shop").Scan(&shopID).Error; err != nil {
		t.Fatal(err)
	}
	device := domain.Device{ShopID: shopID, MacAddress: mac, Name: "protocol-test"}
	if err := db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	point := domain.MeasurementPoint{ID: uuid.New(), ShopID: shopID, Name: "protocol-test-point"}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	assignment := domain.DeviceAssignment{ID: uuid.New(), DeviceID: device.ID, MeasurementPointID: point.ID, ValidFrom: time.Unix(0, 0).UTC()}
	if err := db.Create(&assignment).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		db.Exec("DELETE FROM power_readings WHERE device_id = ?", device.ID)
		db.Exec("DELETE FROM alert_logs WHERE device_id = ?", device.ID)
		db.Exec("DELETE FROM telemetry_ingest_keys WHERE device_id = ?", device.ID)
		db.Exec("DELETE FROM device_assignments WHERE device_id = ?", device.ID)
		db.Delete(&domain.MeasurementPoint{}, point.ID)
		db.Unscoped().Where("id = ?", device.ID).Delete(&domain.Device{})
		db.Exec("DELETE FROM shops WHERE id = ?", shopID)
	}()
	publisher := &testPublisher{}
	service := &MqttService{db: db, ackPublisher: publisher, config: MqttConfig{CommandPrefix: CommandPrefix}}
	payload := []byte(`{"mac":"a1:b2:c3:d4:e5:f6","v":110,"c":1,"p":100,"kwh":1,"ts":1786021200,"protocol_version":1,"boot_id":"boot","boot_counter":7,"seq":123,"energy_delta_kwh":0}`)
	service.processTelemetry(payload)
	service.processTelemetry(payload)
	if publisher.count() != 2 {
		t.Fatalf("want stored and duplicate ACKs, got %d", publisher.count())
	}
	var count int64
	if err := db.Model(&domain.PowerReading{}).Where("device_id = ?", device.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("want one idempotent row, got %d", count)
	}
}

func TestUnknownDeviceDoesNotReturnTerminalStatus(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; database-backed protocol test not run")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureAlertsSchema(dsn); err != nil {
		t.Fatal(err)
	}
	publisher := &testPublisher{}
	service := &MqttService{db: db, ackPublisher: publisher, config: MqttConfig{CommandPrefix: CommandPrefix}}
	service.processTelemetry([]byte(`{"mac":"FFEEDDCCBBAA","v":110,"c":1,"p":100,"kwh":1,"ts":1786021200,"protocol_version":1,"boot_id":"boot","boot_counter":7,"seq":123,"energy_delta_kwh":0}`))
	if publisher.count() != 1 {
		t.Fatal("unknown device should produce one diagnostic rejection")
	}
	var ack TelemetryAck
	if err := json.Unmarshal(publisher.last().payload.([]byte), &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Status == string(IngestStored) || ack.Status == string(IngestDuplicate) {
		t.Fatalf("unknown device received terminal ACK: %+v", ack)
	}
}

type testMessage struct {
	topic string
	body  []byte
}

func (m testMessage) Duplicate() bool   { return false }
func (m testMessage) Qos() byte         { return 1 }
func (m testMessage) Retained() bool    { return false }
func (m testMessage) Topic() string     { return m.topic }
func (m testMessage) MessageID() uint16 { return 1 }
func (m testMessage) Payload() []byte   { return m.body }
func (m testMessage) Ack()              {}

func TestQueueCapturesIngressTimeBeforeWorkerDelay(t *testing.T) {
	ingressAt := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	service := &MqttService{
		config: MqttConfig{TelemetryTopic: TelemetryTopic},
		work:   make(chan queuedMessage, 1),
		clock:  func() time.Time { return ingressAt },
	}
	service.handleMessage(nil, testMessage{topic: TelemetryTopic, body: []byte(`{}`)})
	queued := <-service.work
	if !queued.receivedAt.Equal(ingressAt) {
		t.Fatalf("queue captured %v, want %v", queued.receivedAt, ingressAt)
	}
}

func TestStoredAckIsPublishedOnlyAfterCommittedIngest(t *testing.T) {
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	recordedAt := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	receivedAt := recordedAt.Add(10 * time.Minute)
	addAssignment(t, db, fixture.first.ID, fixture.point.ID, recordedAt.Add(-time.Hour), nil)

	publisher := &testPublisher{}
	service := &MqttService{
		db: db, ingestor: NewTelemetryIngestor(db), ackPublisher: publisher,
		config: MqttConfig{CommandPrefix: CommandPrefix, TelemetryTopic: TelemetryTopic},
		work:   make(chan queuedMessage, 1), clock: func() time.Time { return receivedAt },
	}
	payload := testPayload(fixture.first.MacAddress, recordedAt.Unix(), 8, 1)
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	service.handleMessage(nil, testMessage{topic: TelemetryTopic, body: body})
	queued := <-service.work
	if !queued.receivedAt.Equal(receivedAt) {
		t.Fatalf("queue changed ingress time: %v", queued.receivedAt)
	}
	time.Sleep(2 * time.Millisecond)
	service.handleQueuedMessage(queued)
	if publisher.count() != 1 {
		t.Fatalf("want one stored ACK, got %d", publisher.count())
	}
	var ack TelemetryAck
	if err := json.Unmarshal(publisher.last().payload.([]byte), &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Status != string(IngestStored) {
		t.Fatalf("want stored ACK, got %+v", ack)
	}
	var reading domain.PowerReading
	if err := db.Where("device_id = ?", fixture.first.ID).First(&reading).Error; err != nil {
		t.Fatal(err)
	}
	if !reading.ReceivedAt.Equal(receivedAt) {
		t.Fatalf("want ingress received_at %v, got %v", receivedAt, reading.ReceivedAt)
	}
}

func TestRollbackPublishesNoTerminalAck(t *testing.T) {
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	recordedAt := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	addAssignment(t, db, fixture.first.ID, fixture.point.ID, recordedAt.Add(-time.Hour), nil)
	ingestor := NewTelemetryIngestor(db)
	ingestor.afterKeyClaim = func() error { return errors.New("forced rollback") }
	publisher := &testPublisher{}
	service := &MqttService{db: db, ingestor: ingestor, ackPublisher: publisher, config: MqttConfig{CommandPrefix: CommandPrefix}}

	body, err := json.Marshal(testPayload(fixture.first.MacAddress, recordedAt.Unix(), 9, 1))
	if err != nil {
		t.Fatal(err)
	}
	service.processTelemetryAt(body, recordedAt.Add(time.Minute))
	if publisher.count() != 0 {
		t.Fatal("rollback published a terminal ACK")
	}
}

func TestUnknownAssignmentPublishesOnlyDiagnosticStatus(t *testing.T) {
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	publisher := &testPublisher{}
	service := &MqttService{db: db, ingestor: NewTelemetryIngestor(db), ackPublisher: publisher, config: MqttConfig{CommandPrefix: CommandPrefix}}

	body, err := json.Marshal(testPayload(fixture.first.MacAddress, time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC).Unix(), 10, 1))
	if err != nil {
		t.Fatal(err)
	}
	service.processTelemetryAt(body, time.Date(2026, 8, 8, 10, 1, 0, 0, time.UTC))
	if publisher.count() != 1 {
		t.Fatalf("want one diagnostic response, got %d", publisher.count())
	}
	var ack TelemetryAck
	if err := json.Unmarshal(publisher.last().payload.([]byte), &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Status == string(IngestStored) || ack.Status == string(IngestDuplicate) {
		t.Fatalf("unknown assignment received terminal ACK: %+v", ack)
	}
}

type fakeMQTTClient struct {
	mqtt.Client
	connectToken    mqtt.Token
	subscribeErrors []error
	subscribeCalls  int
}

func (f *fakeMQTTClient) Connect() mqtt.Token { return f.connectToken }

func (f *fakeMQTTClient) Subscribe(string, byte, mqtt.MessageHandler) mqtt.Token {
	f.subscribeCalls++
	var err error
	if len(f.subscribeErrors) > 0 {
		err = f.subscribeErrors[0]
		f.subscribeErrors = f.subscribeErrors[1:]
	}
	done := make(chan struct{})
	close(done)
	return &testToken{err: err, done: done}
}

func readyTestToken(err error) mqtt.Token {
	done := make(chan struct{})
	close(done)
	return &testToken{err: err, done: done}
}

func TestStatusMACContractAndIngressTime(t *testing.T) {
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	receivedAt := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	service := &MqttService{db: db, config: MqttConfig{CommandPrefix: CommandPrefix}}
	body, err := json.Marshal(DeviceStatusPayload{
		Online: true, MAC: colonMAC(fixture.first.MacAddress), BootID: "boot-status", Firmware: "fw-status",
		IP: "192.0.2.10", RSSI: -61, QueueCount: 2, SafeMode: false, TimeSynced: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	service.processStatus("device/"+fixture.first.MacAddress+"/status", body, receivedAt)

	var device domain.Device
	if err := db.First(&device, fixture.first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !device.IsOnline || device.BootID != "boot-status" || device.FirmwareVersion != "fw-status" || device.QueueCount == nil || *device.QueueCount != 2 || device.LastSeen == nil || !device.LastSeen.Equal(receivedAt) {
		t.Fatalf("status was not persisted from formal MAC payload: %+v", device)
	}
}

func TestStatusUpdateParticipatesInSharedWriterFence(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; telemetry integration test not run")
	}
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	service := &MqttService{db: db, config: MqttConfig{CommandPrefix: CommandPrefix}}
	body, err := json.Marshal(DeviceStatusPayload{MAC: fixture.first.MacAddress, Online: true, BootID: "fenced-status"})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := migrations.OpenExclusiveWriterFence(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		service.processStatus("device/"+fixture.first.MacAddress+"/status", body, time.Now().UTC())
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("status update crossed exclusive writer fence")
	case <-time.After(150 * time.Millisecond):
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("status update did not proceed after exclusive release")
	}
	var device domain.Device
	if err := db.First(&device, fixture.first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if device.BootID != "fenced-status" || !device.IsOnline {
		t.Fatalf("status update did not commit after admission: %+v", device)
	}
}

func TestStatusLegacyDeviceIDMACCompatibility(t *testing.T) {
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	service := &MqttService{db: db, config: MqttConfig{CommandPrefix: CommandPrefix}}
	body, err := json.Marshal(DeviceStatusPayload{DeviceID: colonMAC(fixture.first.MacAddress), Online: true})
	if err != nil {
		t.Fatal(err)
	}
	service.processStatus("device/"+fixture.first.MacAddress+"/status", body, time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC))
	var device domain.Device
	if err := db.First(&device, fixture.first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !device.IsOnline {
		t.Fatal("legacy MAC-valued device_id was not accepted")
	}
}

func TestStatusIdentityConflictDoesNotUpdateDevice(t *testing.T) {
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	service := &MqttService{db: db, config: MqttConfig{CommandPrefix: CommandPrefix}}
	body := []byte(`{"mac":"FFEEDDCCBBAA","online":true,"boot_id":"must-not-write"}`)
	service.processStatus("device/"+fixture.first.MacAddress+"/status", body, time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC))
	var device domain.Device
	if err := db.First(&device, fixture.first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if device.BootID == "must-not-write" || device.IsOnline {
		t.Fatalf("conflicting status identity mutated device: %+v", device)
	}
}

func TestStatusLastSeenIsMonotonicAndSnapshotDoesNotRegress(t *testing.T) {
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	service := &MqttService{db: db, config: MqttConfig{CommandPrefix: CommandPrefix}}
	newer := time.Date(2026, 8, 8, 12, 2, 0, 0, time.UTC)
	older := newer.Add(-time.Minute)
	newBody, err := json.Marshal(DeviceStatusPayload{MAC: fixture.first.MacAddress, Online: true, BootID: "newer", QueueCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	oldBody, err := json.Marshal(DeviceStatusPayload{MAC: fixture.first.MacAddress, Online: false, BootID: "older", QueueCount: 9})
	if err != nil {
		t.Fatal(err)
	}
	service.processStatus("device/"+fixture.first.MacAddress+"/status", newBody, newer)
	service.processStatus("device/"+fixture.first.MacAddress+"/status", oldBody, older)
	var device domain.Device
	if err := db.First(&device, fixture.first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if device.LastSeen == nil || !device.LastSeen.Equal(newer) || device.BootID != "newer" || !device.IsOnline || device.QueueCount == nil || *device.QueueCount != 2 {
		t.Fatalf("older status regressed state: %+v", device)
	}
}

func colonMAC(mac string) string {
	return mac[0:2] + ":" + mac[2:4] + ":" + mac[4:6] + ":" + mac[6:8] + ":" + mac[8:10] + ":" + mac[10:12]
}

func TestMQTTReadinessRequiresConnectionAndSubscriptions(t *testing.T) {
	service := &MqttService{}
	if service.Ready() {
		t.Fatal("new MQTT service must not be ready")
	}
	service.setConnected(true)
	if service.Ready() {
		t.Fatal("connected service without subscriptions must not be ready")
	}
	service.setSubscriptionsReady(true)
	if !service.Ready() {
		t.Fatal("connected service with subscriptions should be ready")
	}
	service.onConnectionLost(nil, errors.New("test disconnect"))
	if service.Ready() {
		t.Fatal("disconnected service must not remain ready")
	}
}

func TestInitialMQTTFailureCanRecoverWithoutFalseReadiness(t *testing.T) {
	client := &fakeMQTTClient{connectToken: readyTestToken(errors.New("broker unavailable"))}
	service := &MqttService{client: client, config: MqttConfig{TelemetryTopic: TelemetryTopic, CommandPrefix: CommandPrefix}, work: make(chan queuedMessage, 1)}
	if err := service.Connect(); err == nil {
		t.Fatal("initial broker failure unexpectedly succeeded")
	}
	if service.Ready() {
		t.Fatal("initial MQTT failure reported ready")
	}
	client.connectToken = readyTestToken(nil)
	client.subscribeErrors = []error{nil, nil}
	if err := service.Connect(); err != nil {
		t.Fatal(err)
	}
	service.onConnect(client)
	if !service.Ready() {
		t.Fatal("service did not become ready after connection and subscriptions recovered")
	}
}

func TestSubscriptionFailureDoesNotReportReadyAndCanRecover(t *testing.T) {
	client := &fakeMQTTClient{connectToken: readyTestToken(nil), subscribeErrors: []error{errors.New("telemetry subscribe failed"), errors.New("telemetry subscribe failed"), errors.New("telemetry subscribe failed"), nil, nil}}
	service := &MqttService{client: client, config: MqttConfig{TelemetryTopic: TelemetryTopic, CommandPrefix: CommandPrefix}, work: make(chan queuedMessage, 1)}
	service.onConnect(client)
	if service.Ready() {
		t.Fatal("subscription failure reported ready")
	}
	service.onConnect(client)
	if !service.Ready() {
		t.Fatal("subscription recovery did not restore readiness")
	}
}
