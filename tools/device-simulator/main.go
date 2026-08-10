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
	// When both locks are needed, acquire publishMu before lifecycleMu.
	// Shutdown is the sole exception in sequence only: it releases lifecycleMu
	// before waiting for publishMu, so the locks are never held in reverse order.
	publishMu            sync.Mutex
	lifecycleMu          sync.Mutex
	client               mqtt.Client
	stopping             bool
	ready                bool
	readyChanged         chan struct{}
	sessionEpoch         uint64
	restoringEpoch       uint64
	mqttOperationTimeout time.Duration

	config       config
	generator    simulator.Generator
	mu           sync.Mutex
	pending      map[ackKey][]chan simulator.Ack
	readyEvents  chan struct{}
	offlineQueue []simulator.Telemetry
}

type ackKey = simulator.TelemetryIdentity

type ackWaitError struct {
	status  string
	timeout bool
}

var errMQTTSessionChanged = errors.New("MQTT session changed during publish")

type publishAttempt struct {
	token  mqtt.Token
	client mqtt.Client
	epoch  uint64
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
	device := &deviceSimulator{config: cfg, generator: generator, pending: make(map[ackKey][]chan simulator.Ack), readyEvents: make(chan struct{}, 4)}
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
	opts := s.clientOptions(clientID, roots, broker)
	client := mqtt.NewClient(opts)
	if err := s.setClient(client); err != nil {
		return err
	}
	token := client.Connect()
	if !token.WaitTimeout(15 * time.Second) {
		return fmt.Errorf("MQTT connection timeout")
	}
	if err := token.Error(); err != nil {
		return err
	}
	if err := s.waitForReady(15 * time.Second); err != nil {
		return fmt.Errorf("MQTT subscriptions were not ready: %w", err)
	}
	return nil
}

func (s *deviceSimulator) clientOptions(clientID string, roots *x509.CertPool, broker *url.URL) *mqtt.ClientOptions {
	opts := mqtt.NewClientOptions()
	opts.AddBroker(s.config.BrokerURL)
	opts.SetClientID(clientID)
	opts.SetUsername(s.config.Username)
	opts.SetPassword(s.config.Password)
	// Reconnect mode deliberately replaces the Paho client. Same-client
	// auto-reconnect would make callback ownership ambiguous across losses.
	opts.SetAutoReconnect(false)
	// A command handler performs a finite command ACK publish wait. Run message
	// handlers independently so that wait cannot block Paho's message router.
	// Handler state is concurrency-safe: pending ACKs use mu and MQTT lifecycle
	// state uses lifecycleMu.
	opts.SetOrderMatters(false)
	opts.SetConnectTimeout(10 * time.Second)
	opts.SetKeepAlive(30 * time.Second)
	opts.SetTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: broker.Hostname()})
	opts.SetWill(s.statusTopic(), string(simulator.StatusPayload(s.generator.MAC, s.bootID(), s.generator.FirmwareVersion, false)), 1, true)
	opts.SetOnConnectHandler(s.onConnect)
	opts.SetConnectionLostHandler(s.onConnectionLost)
	return opts
}

func (s *deviceSimulator) onConnect(client mqtt.Client) {
	epoch, ok := s.beginRestoration(client)
	if !ok {
		log.Printf("stale MQTT connect ignored")
		return
	}

	const requiredQoS byte = 1
	for _, topic := range []string{s.ackTopic(), s.commandTopic()} {
		if !s.isCurrentRestoration(client, epoch) {
			return
		}
		token := client.Subscribe(topic, requiredQoS, s.onMessage)
		if err := waitMQTTToken(token, s.operationTimeout()); err != nil {
			log.Printf("subscribe failed for %s: %v", topic, err)
			return
		}
		if !s.isCurrentRestoration(client, epoch) {
			return
		}
		if err := validateSubscribeGrant(token, topic, requiredQoS); err != nil {
			log.Printf("subscribe failed for %s: %v", topic, err)
			return
		}
		log.Printf("SUBSCRIBED %s", topic)
	}

	payload := simulator.StatusPayload(s.generator.MAC, s.bootID(), s.generator.FirmwareVersion, true)
	token, admitted := s.publishForEpoch(client, epoch, s.statusTopic(), 1, true, payload)
	if !admitted {
		return
	}
	if err := waitMQTTToken(token, s.operationTimeout()); err != nil {
		log.Printf("status publish failed: %v", err)
		return
	}
	if !s.commitReady(client, epoch) {
		return
	}
	log.Printf("STATUS online=true topic=%s", s.statusTopic())
}

func (s *deviceSimulator) onConnectionLost(client mqtt.Client, err error) {
	s.lifecycleMu.Lock()
	if s.stopping || s.client == nil || client != s.client {
		s.lifecycleMu.Unlock()
		log.Printf("stale MQTT connection loss ignored: %v", err)
		return
	}
	s.invalidateReadinessLocked()
	s.lifecycleMu.Unlock()
	log.Printf("MQTT connection lost: %v", err)
}

func (s *deviceSimulator) setClient(client mqtt.Client) error {
	// Serialize fresh-client ownership with publication admission and reconnect
	// detachment. No old-client Publish can remain inside its call when the new
	// client becomes current.
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopping {
		return errors.New("MQTT simulator is stopping")
	}
	s.client = client
	s.invalidateReadinessLocked()
	return nil
}

func (s *deviceSimulator) isReady() bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return !s.stopping && s.ready
}

func (s *deviceSimulator) beginRestoration(client mqtt.Client) (uint64, bool) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopping || s.client == nil || client != s.client {
		return 0, false
	}
	// Every callback gets a new restoration epoch. Production Paho clients do
	// not auto-reconnect, so explicit reconnect installs a fresh client first.
	s.invalidateReadinessLocked()
	s.restoringEpoch = s.sessionEpoch
	return s.restoringEpoch, true
}

func (s *deviceSimulator) isCurrentRestoration(client mqtt.Client, epoch uint64) bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.currentRestorationLocked(client, epoch)
}

func (s *deviceSimulator) currentRestorationLocked(client mqtt.Client, epoch uint64) bool {
	return !s.stopping && s.client != nil && client == s.client &&
		s.sessionEpoch == epoch && s.restoringEpoch == epoch
}

func (s *deviceSimulator) publishForEpoch(client mqtt.Client, epoch uint64, topic string, qos byte, retained bool, payload interface{}) (mqtt.Token, bool) {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()

	s.lifecycleMu.Lock()
	current := s.currentRestorationLocked(client, epoch)
	s.lifecycleMu.Unlock()
	if !current {
		return nil, false
	}

	// Publish may synchronously block on Paho backpressure. Keep publication
	// admission serialized, but never hold lifecycleMu across a Paho call.
	token := client.Publish(topic, qos, retained, payload)
	s.lifecycleMu.Lock()
	current = s.currentRestorationLocked(client, epoch)
	s.lifecycleMu.Unlock()
	if !current {
		return nil, false
	}
	return token, true
}

func (s *deviceSimulator) commitReady(client mqtt.Client, epoch uint64) bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if !s.currentRestorationLocked(client, epoch) {
		return false
	}
	s.setReadyLocked(true)
	select {
	case s.readyEvents <- struct{}{}:
	default:
	}
	return true
}

func (s *deviceSimulator) disconnectClientForReconnect() mqtt.Client {
	// Wait for every admitted old-client Publish to leave its Paho call before
	// invalidating ownership and disconnecting. setClient uses the same barrier,
	// so a replacement cannot become current during this transition.
	s.publishMu.Lock()
	defer s.publishMu.Unlock()

	s.lifecycleMu.Lock()
	if s.stopping {
		s.lifecycleMu.Unlock()
		return nil
	}
	client := s.client
	s.client = nil
	s.invalidateReadinessLocked()
	s.lifecycleMu.Unlock()

	if client != nil {
		client.Disconnect(250)
	}
	return client
}

func (s *deviceSimulator) invalidateReadinessLocked() {
	s.sessionEpoch++
	s.restoringEpoch = 0
	if !s.setReadyLocked(false) {
		s.signalReadyChangeLocked()
	}
}

func (s *deviceSimulator) setReadyLocked(ready bool) bool {
	if s.ready == ready {
		return false
	}
	s.ready = ready
	s.signalReadyChangeLocked()
	return true
}

func (s *deviceSimulator) signalReadyChangeLocked() {
	if s.readyChanged == nil {
		s.readyChanged = make(chan struct{})
	}
	close(s.readyChanged)
	s.readyChanged = make(chan struct{})
}

func (s *deviceSimulator) waitForReady(timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.lifecycleMu.Lock()
		if s.readyChanged == nil {
			s.readyChanged = make(chan struct{})
		}
		ready, stopping, changed := s.ready, s.stopping, s.readyChanged
		s.lifecycleMu.Unlock()
		if stopping {
			return errors.New("MQTT simulator is stopping")
		}
		if ready {
			return nil
		}
		select {
		case <-changed:
		case <-timer.C:
			return errors.New("readiness timeout")
		}
	}
}

func (s *deviceSimulator) operationTimeout() time.Duration {
	if s.mqttOperationTimeout > 0 {
		return s.mqttOperationTimeout
	}
	return 5 * time.Second
}

func waitMQTTToken(token mqtt.Token, timeout time.Duration) error {
	if token == nil {
		return errors.New("MQTT operation returned no token")
	}
	if !token.WaitTimeout(timeout) {
		return fmt.Errorf("MQTT operation timeout after %s", timeout)
	}
	if err := token.Error(); err != nil {
		return err
	}
	return nil
}

type subscribeResult interface {
	Result() map[string]byte
}

func validateSubscribeGrant(token mqtt.Token, topic string, requestedQoS byte) error {
	if requestedQoS > 2 {
		return fmt.Errorf("invalid requested QoS %d", requestedQoS)
	}
	resultToken, ok := token.(subscribeResult)
	if !ok {
		return errors.New("SUBACK token did not expose grant results")
	}
	grant, ok := resultToken.Result()[topic]
	if !ok {
		return errors.New("SUBACK did not include the exact requested topic")
	}
	if grant == 0x80 {
		return errors.New("broker rejected subscription in SUBACK")
	}
	if grant > 2 {
		return fmt.Errorf("SUBACK contained invalid grant 0x%02x", grant)
	}
	// A SUBACK grant is the maximum delivery QoS selected by the broker. It may
	// be lower than requested, but must not be higher. This simulator requires
	// QoS 1 for ACK and command reliability, so a QoS 0 downgrade is valid MQTT
	// but insufficient; for the requested QoS 1, only grant 1 is acceptable.
	if grant < requestedQoS {
		return fmt.Errorf("SUBACK grant QoS %d is below required QoS %d", grant, requestedQoS)
	}
	if grant > requestedQoS {
		return fmt.Errorf("SUBACK grant QoS %d exceeds requested QoS %d", grant, requestedQoS)
	}
	return nil
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
	attempt, err := s.publishWhenReady(fmt.Sprintf("device/%s/command/ack", s.generator.MAC), 1, false, body)
	if err != nil {
		log.Printf("command ACK publish failed: %v", err)
		return
	}
	if err := waitMQTTToken(attempt.token, 5*time.Second); err != nil {
		log.Printf("command ACK publish failed: %v", err)
		return
	}
	if err := s.validateReadyPublish(attempt); err != nil {
		log.Printf("command ACK publish failed: %v", err)
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
	oldClient := s.disconnectClientForReconnect()
	if oldClient == nil {
		return errors.New("MQTT reconnect failed: client is not configured")
	}
	// Paho warns that a client must not be reused immediately after Disconnect;
	// connect() creates a fresh client and restores the required subscriptions.
	if err := s.connect(); err != nil {
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
	identity := telemetry.Identity()
	waiter := s.registerAck(identity)
	attempt, err := s.publishWhenReady("device/upload/data", 0, false, body)
	if err != nil {
		s.removeAck(identity, waiter)
		return err
	}
	if err := waitMQTTToken(attempt.token, 5*time.Second); err != nil {
		s.removeAck(identity, waiter)
		return fmt.Errorf("telemetry publish failed: %w", err)
	}
	if err := s.validateReadyPublish(attempt); err != nil {
		s.removeAck(identity, waiter)
		return err
	}
	log.Printf("PUBLISHED boot=%d seq=%d", telemetry.BootCounter, telemetry.Sequence)
	select {
	case <-ctx.Done():
		s.removeAck(identity, waiter)
		return ctx.Err()
	case ack := <-waiter:
		if !ack.IsTerminal() {
			return &ackWaitError{status: ack.Status}
		}
		if err := s.validateReadyPublish(attempt); err != nil {
			return err
		}
		return nil
	case <-time.After(s.config.AckTimeout):
		s.removeAck(identity, waiter)
		log.Printf("ACK timeout")
		return &ackWaitError{timeout: true}
	}
}

func (s *deviceSimulator) publishWhenReady(topic string, qos byte, retained bool, payload interface{}) (publishAttempt, error) {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()

	s.lifecycleMu.Lock()
	if s.stopping || !s.ready || s.client == nil {
		s.lifecycleMu.Unlock()
		return publishAttempt{}, errors.New("MQTT session is not ready")
	}
	attempt := publishAttempt{client: s.client, epoch: s.sessionEpoch}
	s.lifecycleMu.Unlock()

	// Paho may synchronously block here under backpressure. Lifecycle callbacks
	// remain free to invalidate the captured epoch while publication admission
	// stays serialized through the post-call epoch validation.
	attempt.token = attempt.client.Publish(topic, qos, retained, payload)
	if err := s.validateReadyPublish(attempt); err != nil {
		return publishAttempt{}, err
	}
	return attempt, nil
}

func (s *deviceSimulator) validateReadyPublish(attempt publishAttempt) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopping || !s.ready || s.client == nil || attempt.client != s.client || attempt.epoch != s.sessionEpoch {
		return errMQTTSessionChanged
	}
	return nil
}

func (s *deviceSimulator) registerAck(identity simulator.TelemetryIdentity) chan simulator.Ack {
	waiter := make(chan simulator.Ack, 1)
	s.mu.Lock()
	s.pending[identity] = append(s.pending[identity], waiter)
	s.mu.Unlock()
	return waiter
}

func (s *deviceSimulator) resolveAck(ack simulator.Ack) {
	key := ack.Identity()
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

func (s *deviceSimulator) removeAck(identity simulator.TelemetryIdentity, target chan simulator.Ack) {
	s.mu.Lock()
	defer s.mu.Unlock()
	waiters := s.pending[identity]
	for i, waiter := range waiters {
		if waiter == target {
			s.pending[identity] = append(waiters[:i], waiters[i+1:]...)
			if len(s.pending[identity]) == 0 {
				delete(s.pending, identity)
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
	// Establish terminal ownership before waiting for publication admission.
	// lifecycleMu is released before publishMu is acquired, which avoids the
	// inverse of the normal publishMu -> lifecycleMu lock order.
	s.lifecycleMu.Lock()
	if s.stopping {
		s.lifecycleMu.Unlock()
		return
	}
	s.stopping = true
	client := s.client
	s.client = nil
	s.invalidateReadinessLocked()
	s.lifecycleMu.Unlock()

	// Every normal Publish admitted before the terminal transition finishes its
	// Paho call before offline is enqueued. Later publishes observe stopping and
	// reject, so retained offline is the final status publication.
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	if client == nil {
		return
	}
	if client.IsConnected() {
		payload := simulator.StatusPayload(s.generator.MAC, s.bootID(), s.generator.FirmwareVersion, false)
		token := client.Publish(s.statusTopic(), 1, true, payload)
		if err := waitMQTTToken(token, 2*time.Second); err != nil {
			log.Printf("offline status publish failed: %v", err)
		}
	}
	client.Disconnect(250)
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
