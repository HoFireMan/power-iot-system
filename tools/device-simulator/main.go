package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"power-iot-device-simulator/internal/simulator"
)

type config struct {
	Mode            string
	BrokerURL       string
	Username        string
	Password        string
	CAFile          string
	MAC             string
	FirmwareVersion string
	Interval        time.Duration
	BootCounter     int64
	StartSequence   int64
	ReplayCount     int
	AckTimeout      time.Duration
	CommandAck      bool
	RecordedAt      *time.Time
	OfflineWait     time.Duration
}

type deviceSimulator struct {
	client       mqtt.Client
	config       config
	generator    simulator.Generator
	mu           sync.Mutex
	pending      map[ackKey][]chan simulator.Ack
	ready        chan struct{}
	readyOnce    sync.Once
	readyEvents  chan struct{}
	offlineQueue []simulator.Telemetry
}

type ackKey struct {
	boot int64
	seq  int64
}

type ackWaitError struct {
	status  string
	timeout bool
}

func (e *ackWaitError) Error() string {
	if e.timeout {
		return "ACK timeout"
	}
	return fmt.Sprintf("nonterminal ACK status=%s", e.status)
}

func main() {
	cfg, err := parseConfig()
	if err != nil {
		log.Fatal(err)
	}
	generator, err := simulator.NewGenerator(cfg.MAC, cfg.FirmwareVersion, cfg.BootCounter, cfg.StartSequence)
	if err != nil {
		log.Fatal(err)
	}
	device := &deviceSimulator{config: cfg, generator: generator, pending: make(map[ackKey][]chan simulator.Ack), ready: make(chan struct{}), readyEvents: make(chan struct{}, 4)}
	if cfg.Mode == "offline-replay" {
		device.prepareOfflineQueue()
		if cfg.OfflineWait > 0 {
			log.Printf("OFFLINE_BUFFER waiting=%s before MQTTS connect", cfg.OfflineWait)
			time.Sleep(cfg.OfflineWait)
		}
	}
	if err := device.connect(); err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	defer device.shutdown()
	if err := device.run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

func parseConfig() (config, error) {
	cfg := config{}
	flag.StringVar(&cfg.Mode, "mode", envOr("SIMULATOR_MODE", "once"), "once|continuous|duplicate|invalid|offline-replay|reconnect")
	flag.StringVar(&cfg.BrokerURL, "mqtt-broker-url", envOr("MQTT_BROKER_URL", ""), "MQTTS broker URL, for example tls://127.0.0.1:8883")
	flag.StringVar(&cfg.Username, "mqtt-username", envOr("MQTT_USERNAME", ""), "MQTT username")
	flag.StringVar(&cfg.Password, "mqtt-password", envOr("MQTT_PASSWORD", ""), "MQTT password")
	flag.StringVar(&cfg.CAFile, "mqtt-ca-file", envOr("MQTT_CA_FILE", ""), "PEM CA file used to verify the broker")
	flag.StringVar(&cfg.MAC, "device-mac", envOr("DEVICE_MAC", simulator.DefaultMAC), "device MAC")
	flag.StringVar(&cfg.FirmwareVersion, "firmware-version", envOr("FIRMWARE_VERSION", "simulator-1.0.0"), "firmware version reported in telemetry")
	flag.DurationVar(&cfg.Interval, "publish-interval", durationEnv("PUBLISH_INTERVAL", 5*time.Second), "telemetry interval")
	flag.Int64Var(&cfg.BootCounter, "boot-counter", int64Env("BOOT_COUNTER", 1), "simulated boot counter")
	flag.Int64Var(&cfg.StartSequence, "start-seq", int64Env("START_SEQ", 1), "first simulated sequence number")
	flag.IntVar(&cfg.ReplayCount, "replay-count", intEnv("REPLAY_COUNT", 5), "offline-replay queue length")
	flag.DurationVar(&cfg.AckTimeout, "ack-timeout", durationEnv("ACK_TIMEOUT", 10*time.Second), "time to wait for an application ACK")
	flag.BoolVar(&cfg.CommandAck, "command-ack", boolEnv("COMMAND_ACK", true), "publish command/ack after receiving a command")
	recordedAtText := envOr("RECORDED_AT", "")
	offlineWaitText := durationEnv("OFFLINE_WAIT", 0)
	flag.StringVar(&recordedAtText, "recorded-at", recordedAtText, "RFC3339 telemetry recorded_at/ts base timestamp")
	flag.DurationVar(&offlineWaitText, "offline-wait", offlineWaitText, "wait while buffered offline before connecting")
	flag.Parse()
	if recordedAtText != "" {
		recordedAt, err := parseRecordedAt(recordedAtText)
		if err != nil {
			return config{}, err
		}
		cfg.RecordedAt = recordedAt
	}
	cfg.OfflineWait = offlineWaitText

	if !isTLSBroker(cfg.BrokerURL) {
		return config{}, fmt.Errorf("MQTT broker URL must use tls://; insecure transport is not supported")
	}
	if strings.TrimSpace(cfg.Username) == "" || cfg.Password == "" {
		return config{}, fmt.Errorf("MQTT username and password are required")
	}
	if strings.TrimSpace(cfg.CAFile) == "" {
		return config{}, fmt.Errorf("MQTT CA file is required")
	}
	if cfg.Interval <= 0 || cfg.AckTimeout <= 0 {
		return config{}, fmt.Errorf("publish interval and ACK timeout must be positive")
	}
	if cfg.BootCounter < 0 || cfg.StartSequence < 0 || cfg.ReplayCount <= 0 || cfg.OfflineWait < 0 {
		return config{}, fmt.Errorf("boot counter, sequence, replay count, and offline wait are invalid")
	}
	return cfg, nil
}

func (s *deviceSimulator) connect() error {
	roots := x509.NewCertPool()
	ca, err := os.ReadFile(s.config.CAFile)
	if err != nil {
		return fmt.Errorf("read MQTT CA file: %w", err)
	}
	if !roots.AppendCertsFromPEM(ca) {
		return fmt.Errorf("MQTT CA file contains no certificates")
	}
	broker, err := url.Parse(s.config.BrokerURL)
	if err != nil || broker.Hostname() == "" {
		return fmt.Errorf("invalid MQTT broker URL")
	}
	clientID, err := uniqueClientID(s.generator.MAC)
	if err != nil {
		return err
	}
	statusTopic := fmt.Sprintf("device/%s/status", s.generator.MAC)
	opts := mqtt.NewClientOptions()
	opts.AddBroker(s.config.BrokerURL)
	opts.SetClientID(clientID)
	opts.SetUsername(s.config.Username)
	opts.SetPassword(s.config.Password)
	opts.SetAutoReconnect(true)
	opts.SetConnectTimeout(10 * time.Second)
	opts.SetKeepAlive(30 * time.Second)
	opts.SetTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: broker.Hostname()})
	opts.SetWill(statusTopic, string(simulator.StatusPayload(s.generator.MAC, s.bootID(), s.generator.FirmwareVersion, false)), 1, true)
	opts.SetOnConnectHandler(s.onConnect)
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) { log.Printf("MQTT connection lost: %v", err) })
	s.client = mqtt.NewClient(opts)
	token := s.client.Connect()
	if !token.WaitTimeout(15 * time.Second) {
		return fmt.Errorf("MQTT connection timeout")
	}
	if err := token.Error(); err != nil {
		return err
	}
	select {
	case <-s.ready:
		return nil
	case <-time.After(15 * time.Second):
		return fmt.Errorf("MQTT subscriptions were not ready")
	}
}

func (s *deviceSimulator) onConnect(client mqtt.Client) {
	ready := true
	for _, topic := range []string{s.ackTopic(), s.commandTopic()} {
		token := client.Subscribe(topic, 1, s.onMessage)
		if !token.Wait() || token.Error() != nil {
			log.Printf("subscribe failed for %s: %v", topic, token.Error())
			ready = false
			continue
		}
		log.Printf("SUBSCRIBED %s", topic)
	}
	if !ready {
		return
	}
	payload := simulator.StatusPayload(s.generator.MAC, s.bootID(), s.generator.FirmwareVersion, true)
	token := client.Publish(s.statusTopic(), 1, true, payload)
	if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
		log.Printf("status publish failed: %v", token.Error())
		return
	}
	log.Printf("STATUS online=true topic=%s", s.statusTopic())
	s.readyOnce.Do(func() { close(s.ready) })
	select {
	case s.readyEvents <- struct{}{}:
	default:
	}
}

func (s *deviceSimulator) onMessage(_ mqtt.Client, message mqtt.Message) {
	switch message.Topic() {
	case s.ackTopic():
		ack, err := simulator.ParseAck(message.Payload())
		if err != nil {
			log.Printf("ACK malformed: %v", err)
			return
		}
		s.printAck(ack)
		s.resolveAck(ack)
	case s.commandTopic():
		command, err := simulator.ParseCommand(message.Payload())
		if err != nil {
			log.Printf("COMMAND malformed: %v", err)
			return
		}
		log.Printf("COMMAND command_id=%s action=%s expires_at=%d", command.CommandID, command.Action, command.ExpiresAt)
		if err := command.Validate(time.Now()); err != nil {
			log.Printf("COMMAND rejected: %v", err)
			return
		}
		if s.config.CommandAck {
			s.publishCommandAck(command)
		}
	}
}

func (s *deviceSimulator) printAck(ack simulator.Ack) {
	switch ack.Status {
	case "stored":
		log.Printf("ACK stored")
	case "duplicate":
		log.Printf("ACK duplicate")
	default:
		log.Printf("ACK rejected status=%s", ack.Status)
	}
}

func (s *deviceSimulator) publishCommandAck(command simulator.Command) {
	body, _ := json.Marshal(struct {
		CommandID string `json:"command_id"`
		Action    string `json:"action"`
		Status    string `json:"status"`
	}{command.CommandID, command.Action, "received"})
	token := s.client.Publish(fmt.Sprintf("device/%s/command/ack", s.generator.MAC), 1, false, body)
	if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
		log.Printf("command ACK publish failed: %v", token.Error())
	}
}

func (s *deviceSimulator) run(ctx context.Context) error {
	switch s.config.Mode {
	case "once":
		return s.runOnce(ctx)
	case "continuous":
		return s.runContinuous(ctx)
	case "duplicate":
		return s.runDuplicate(ctx)
	case "invalid":
		return s.runInvalid(ctx)
	case "offline-replay":
		return s.runOfflineReplay(ctx)
	case "reconnect":
		return s.runReconnect(ctx)
	default:
		return fmt.Errorf("unsupported simulator mode %q", s.config.Mode)
	}
}

func (s *deviceSimulator) runOnce(ctx context.Context) error {
	telemetry := s.nextTelemetry(time.Now())
	return s.publishAndWait(ctx, telemetry)
}

func (s *deviceSimulator) runContinuous(ctx context.Context) error {
	ticker := time.NewTicker(s.config.Interval)
	defer ticker.Stop()
	if err := s.publishAndWait(ctx, s.nextTelemetry(time.Now())); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("continuous ACK error: %v", err)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-ticker.C:
			if err := s.publishAndWait(ctx, s.nextTelemetry(now)); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("continuous ACK error: %v", err)
			}
		}
	}
}

func (s *deviceSimulator) runDuplicate(ctx context.Context) error {
	telemetry := s.nextTelemetry(time.Now())
	if err := s.publishAndWait(ctx, telemetry); err != nil {
		return err
	}
	return s.publishAndWait(ctx, telemetry)
}

func (s *deviceSimulator) runInvalid(ctx context.Context) error {
	telemetry := s.nextTelemetry(time.Now())
	telemetry.PowerFactor = 1.5 // outside the backend's safe v1 range
	if err := s.publishAndWait(ctx, telemetry); err != nil {
		var rejected *ackWaitError
		if errors.As(err, &rejected) && !rejected.timeout {
			return nil
		}
		return err
	}
	return nil
}

func (s *deviceSimulator) runOfflineReplay(ctx context.Context) error {
	s.prepareOfflineQueue()
	for _, telemetry := range s.offlineQueue {
		if err := s.publishAndWait(ctx, telemetry); err != nil {
			return err
		}
	}
	return nil
}

func (s *deviceSimulator) runReconnect(ctx context.Context) error {
	if err := s.publishAndWait(ctx, s.nextTelemetry(time.Now())); err != nil {
		return err
	}
	log.Printf("RECONNECT disconnecting MQTT client")
	s.drainReadyEvents()
	s.client.Disconnect(250)
	token := s.client.Connect()
	if !token.WaitTimeout(15 * time.Second) {
		return errors.New("MQTT reconnect timeout")
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("MQTT reconnect failed: %w", err)
	}
	select {
	case <-s.readyEvents:
		log.Printf("RECONNECT connected and subscriptions restored")
	case <-time.After(15 * time.Second):
		return errors.New("MQTT reconnect subscriptions were not ready")
	}
	return s.publishAndWait(ctx, s.nextTelemetry(time.Now()))
}

func (s *deviceSimulator) prepareOfflineQueue() {
	if s.offlineQueue != nil {
		return
	}
	base := time.Now()
	if s.config.RecordedAt != nil {
		base = *s.config.RecordedAt
	}
	queue := make([]simulator.Telemetry, 0, s.config.ReplayCount)
	for i := 0; i < s.config.ReplayCount; i++ {
		queue = append(queue, s.generator.Next(base.Add(time.Duration(i)*s.config.Interval), s.config.Interval))
	}
	s.offlineQueue = queue
	log.Printf("LOCAL_QUEUE created=%d while disconnected", len(queue))
}

func (s *deviceSimulator) nextTelemetry(now time.Time) simulator.Telemetry {
	if s.config.RecordedAt != nil {
		offset := s.generator.Sequence - s.config.StartSequence
		now = s.config.RecordedAt.Add(time.Duration(offset) * s.config.Interval)
	}
	return s.generator.Next(now, s.config.Interval)
}

func (s *deviceSimulator) drainReadyEvents() {
	for {
		select {
		case <-s.readyEvents:
		default:
			return
		}
	}
}

func (s *deviceSimulator) publishAndWait(ctx context.Context, telemetry simulator.Telemetry) error {
	body, err := json.Marshal(telemetry)
	if err != nil {
		return err
	}
	waiter := s.registerAck(telemetry.BootCounter, telemetry.Sequence)
	token := s.client.Publish("device/upload/data", 0, false, body)
	if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
		s.removeAck(telemetry.BootCounter, telemetry.Sequence, waiter)
		return fmt.Errorf("telemetry publish failed: %v", token.Error())
	}
	log.Printf("PUBLISHED boot=%d seq=%d", telemetry.BootCounter, telemetry.Sequence)
	select {
	case <-ctx.Done():
		s.removeAck(telemetry.BootCounter, telemetry.Sequence, waiter)
		return ctx.Err()
	case ack := <-waiter:
		if ack.Status != "stored" && ack.Status != "duplicate" {
			return &ackWaitError{status: ack.Status}
		}
		return nil
	case <-time.After(s.config.AckTimeout):
		s.removeAck(telemetry.BootCounter, telemetry.Sequence, waiter)
		log.Printf("ACK timeout")
		return &ackWaitError{timeout: true}
	}
}

func (s *deviceSimulator) registerAck(boot, seq int64) chan simulator.Ack {
	waiter := make(chan simulator.Ack, 1)
	s.mu.Lock()
	s.pending[ackKey{boot: boot, seq: seq}] = append(s.pending[ackKey{boot: boot, seq: seq}], waiter)
	s.mu.Unlock()
	return waiter
}

func (s *deviceSimulator) resolveAck(ack simulator.Ack) {
	key := ackKey{boot: ack.BootCounter, seq: ack.Sequence}
	s.mu.Lock()
	waiters := s.pending[key]
	if len(waiters) == 0 {
		s.mu.Unlock()
		return
	}
	waiter := waiters[0]
	if len(waiters) == 1 {
		delete(s.pending, key)
	} else {
		s.pending[key] = waiters[1:]
	}
	s.mu.Unlock()
	select {
	case waiter <- ack:
	default:
	}
}

func (s *deviceSimulator) removeAck(boot, seq int64, target chan simulator.Ack) {
	key := ackKey{boot: boot, seq: seq}
	s.mu.Lock()
	defer s.mu.Unlock()
	waiters := s.pending[key]
	for i, waiter := range waiters {
		if waiter == target {
			s.pending[key] = append(waiters[:i], waiters[i+1:]...)
			if len(s.pending[key]) == 0 {
				delete(s.pending, key)
			}
			return
		}
	}
}

func parseRecordedAt(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	recordedAt, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("invalid --recorded-at %q: expected RFC3339: %w", value, err)
	}
	recordedAt = recordedAt.UTC()
	return &recordedAt, nil
}

func (s *deviceSimulator) shutdown() {
	if s.client == nil || !s.client.IsConnected() {
		return
	}
	payload := simulator.StatusPayload(s.generator.MAC, s.bootID(), s.generator.FirmwareVersion, false)
	token := s.client.Publish(s.statusTopic(), 1, true, payload)
	if !token.WaitTimeout(2*time.Second) || token.Error() != nil {
		log.Printf("offline status publish failed: %v", token.Error())
	}
	s.client.Disconnect(250)
}

func (s *deviceSimulator) bootID() string {
	return fmt.Sprintf("%s-%d", s.generator.MAC, s.generator.BootCounter)
}
func (s *deviceSimulator) ackTopic() string {
	return fmt.Sprintf("device/%s/telemetry/ack", s.generator.MAC)
}
func (s *deviceSimulator) commandTopic() string {
	return fmt.Sprintf("device/%s/command", s.generator.MAC)
}
func (s *deviceSimulator) statusTopic() string {
	return fmt.Sprintf("device/%s/status", s.generator.MAC)
}

func uniqueClientID(mac string) (string, error) {
	bytes := make([]byte, 6)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate MQTT client ID: %w", err)
	}
	return fmt.Sprintf("device-simulator-%s-%s", mac, hex.EncodeToString(bytes)), nil
}

func isTLSBroker(value string) bool {
	u, err := url.Parse(strings.TrimSpace(value))
	return err == nil && strings.EqualFold(u.Scheme, "tls") && u.Hostname() != ""
}
func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
func durationEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
func int64Env(key string, fallback int64) int64 {
	var value int64
	if _, err := fmt.Sscan(os.Getenv(key), &value); err == nil {
		return value
	}
	return fallback
}
func intEnv(key string, fallback int) int {
	var value int
	if _, err := fmt.Sscan(os.Getenv(key), &value); err == nil {
		return value
	}
	return fallback
}
func boolEnv(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return strings.EqualFold(value, "1") || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes")
}
