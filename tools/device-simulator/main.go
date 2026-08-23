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

// defaultQueueCapacity keeps simulator RAM bounded while allowing the default
// offline replay batch to fit without dropping an item.
const defaultQueueCapacity = 100

type config struct {
	Mode            string
	BrokerURL       string
	Username        string
	Password        string
	CAFile          string
	MAC             string
	FirmwareVersion string
	Interval        time.Duration
	CoverageProfile   bool
	ClockSynchronized bool
	BootCounter        int64
	StartSequence   int64
	ReplayCount     int
	QueueCapacity   int
	AckTimeout      time.Duration
	CommandAck      bool
	RecordedAt      *time.Time
	OfflineWait     time.Duration
	ReconnectMAC    string
}

type repeatableMACFlag struct {
	values []string
}

func (f *repeatableMACFlag) String() string {
	return strings.Join(f.values, ",")
}

func (f *repeatableMACFlag) Set(value string) error {
	f.values = append(f.values, value)
	return nil
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

	config               config
	generator            simulator.Generator
	coverageNextStart    *time.Time
	mu                   sync.Mutex
	pending              map[ackKey][]chan simulator.Ack
	readyEvents          chan struct{}
	offlineQueue         []simulator.Telemetry
	queue                *simulator.TelemetryQueue
	queueMu              sync.Mutex
	replayMu             sync.Mutex
	ackEpoch             map[chan simulator.Ack]uint64
	ackQueueItem         map[chan simulator.Ack]bool
	ackAttempt           map[chan simulator.Ack]bool
	ackAttemptAuthorized map[chan simulator.Ack]bool
	ackAuthorized        map[ackKey]bool
	deferredACK          map[ackKey]simulator.Ack

	// Test-only seam for proving waiter registration is atomic with epoch capture.
	ackRegistrationHook func()

	// Test-only seam for injecting an ACK immediately before quarantine release.
	queuedACKReleaseHook func(ackKey)

	// Test-only seam for making an ACK ready after the timer wins the outer
	// select, before timeout arbitration consumes or revokes the waiter.
	ackTimeoutArbitrationHook func(ackKey)

	// Test-only seam called after timeout arbitration finds no ready ACK and
	// before it revokes authorization. The callback must not block or reacquire
	// lifecycleMu/mu; it is used only to start a competing ACK attempt.
	ackTimeoutArbitrationInspectionHook func(ackKey)
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
	change <-chan struct{}
}

type simulatorProcess struct {
	devices []*deviceSimulator
}

type deviceResult struct {
	index     int
	err       error
	cancelled bool
}

func newDeviceSimulator(base config, mac string) (*deviceSimulator, error) {
	deviceConfig := base
	deviceConfig.MAC = mac
	if base.RecordedAt != nil {
		recordedAt := *base.RecordedAt
		deviceConfig.RecordedAt = &recordedAt
	}
	generator, err := simulator.NewGenerator(mac, deviceConfig.FirmwareVersion, deviceConfig.BootCounter, deviceConfig.StartSequence)
	if err != nil {
		return nil, err
	}
	queueCapacity := deviceConfig.QueueCapacity
	if queueCapacity == 0 {
		queueCapacity = defaultQueueCapacity
	}
	queue, err := simulator.NewTelemetryQueue(queueCapacity)
	if err != nil {
		return nil, err
	}
	return &deviceSimulator{
		config:               deviceConfig,
		generator:            generator,
		pending:              make(map[ackKey][]chan simulator.Ack),
		readyEvents:          make(chan struct{}, 4),
		queue:                queue,
		ackEpoch:             make(map[chan simulator.Ack]uint64),
		ackQueueItem:         make(map[chan simulator.Ack]bool),
		ackAttempt:           make(map[chan simulator.Ack]bool),
		ackAttemptAuthorized: make(map[chan simulator.Ack]bool),
		ackAuthorized:        make(map[ackKey]bool),
		deferredACK:          make(map[ackKey]simulator.Ack),
	}, nil
}

func normalizeConfiguredMACs(base config, rawMACs []string) ([]string, error) {
	if len(rawMACs) == 0 {
		rawMACs = []string{base.MAC}
	}
	canonicalMACs := make([]string, len(rawMACs))
	seen := make(map[string]int, len(rawMACs))
	for i, rawMAC := range rawMACs {
		canonical, err := simulator.NormalizeMAC(rawMAC)
		if err != nil {
			return nil, fmt.Errorf("device MAC %q: %w", rawMAC, err)
		}
		if previous, exists := seen[canonical]; exists {
			return nil, fmt.Errorf("duplicate device MAC %q normalizes to %q (already configured at position %d)", rawMAC, canonical, previous)
		}
		seen[canonical] = i
		canonicalMACs[i] = canonical
	}
	return canonicalMACs, nil
}

func startConfiguredDevices(ctx context.Context, base config, rawMACs []string, connectFn func(*deviceSimulator) error) (*simulatorProcess, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	canonicalMACs, err := normalizeConfiguredMACs(base, rawMACs)
	if err != nil {
		return nil, err
	}
	process := &simulatorProcess{devices: make([]*deviceSimulator, 0, len(canonicalMACs))}
	for _, mac := range canonicalMACs {
		device, err := newDeviceSimulator(base, mac)
		if err != nil {
			process.shutdown()
			return nil, fmt.Errorf("device %s: %w", mac, err)
		}
		process.devices = append(process.devices, device)
	}
	if base.Mode == "offline-replay" {
		for _, device := range process.devices {
			if err := device.prepareOfflineQueue(); err != nil {
				process.shutdown()
				return nil, fmt.Errorf("prepare offline coverage queue for %s: %w", device.generator.MAC, err)
			}
		}
		if base.OfflineWait > 0 {
			log.Printf("OFFLINE_BUFFER waiting=%s before MQTTS connect", base.OfflineWait)
			timer := time.NewTimer(base.OfflineWait)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				process.shutdown()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	if err := ctx.Err(); err != nil {
		process.shutdown()
		return nil, err
	}
	if err := process.connectAll(ctx, connectFn); err != nil {
		process.shutdown()
		return nil, err
	}
	return process, nil
}

func (p *simulatorProcess) connectAll(ctx context.Context, connectFn func(*deviceSimulator) error) error {
	if p == nil {
		return errors.New("simulator process is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if connectFn == nil {
		connectFn = (*deviceSimulator).connect
	}
	results := make(chan deviceResult, len(p.devices))
	var group sync.WaitGroup
	for index, device := range p.devices {
		group.Add(1)
		go func(index int, device *deviceSimulator) {
			defer group.Done()
			if err := ctx.Err(); err != nil {
				results <- deviceResult{index: index, err: err}
				return
			}
			results <- deviceResult{index: index, err: connectFn(device)}
		}(index, device)
	}
	group.Wait()
	close(results)

	resultByIndex := make([]error, len(p.devices))
	for result := range results {
		resultByIndex[result.index] = result.err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var startupErrors []error
	for index, err := range resultByIndex {
		if err == nil {
			continue
		}
		startupErrors = append(startupErrors, fmt.Errorf("device %s: %w", p.devices[index].generator.MAC, err))
	}
	if len(startupErrors) > 0 {
		return errors.Join(startupErrors...)
	}
	return nil
}

func (p *simulatorProcess) run(ctx context.Context) error {
	if p == nil {
		return errors.New("simulator process is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		p.shutdown()
		return nil
	}

	shutdownOnCancel := make(chan struct{})
	shutdownWatcherDone := make(chan struct{})
	var shutdownOnce sync.Once
	shutdown := func() {
		// sync.Once also waits for an in-flight shutdown, so a caller that
		// observes cancellation cannot return while the watcher is still
		// publishing offline status or disconnecting devices.
		shutdownOnce.Do(p.shutdown)
	}
	go func() {
		defer close(shutdownWatcherDone)
		select {
		case <-ctx.Done():
			shutdown()
		case <-shutdownOnCancel:
			// Cancellation can race with the normal-completion signal. Check
			// again before letting the watcher exit so it cannot miss shutdown.
			if ctx.Err() != nil {
				shutdown()
			}
		}
	}()
	defer func() {
		close(shutdownOnCancel)
		<-shutdownWatcherDone
		// If cancellation raced with group completion, the watcher may have
		// selected shutdownOnCancel. The Once call either performs or waits
		// for the one process-local shutdown before run returns.
		if ctx.Err() != nil {
			shutdown()
		}
	}()

	results := make(chan deviceResult, len(p.devices))
	var group sync.WaitGroup
	for index, device := range p.devices {
		group.Add(1)
		go func(index int, device *deviceSimulator) {
			defer group.Done()
			err := device.run(ctx)
			results <- deviceResult{index: index, err: err, cancelled: ctx.Err() != nil}
		}(index, device)
	}
	group.Wait()
	close(results)
	if ctx.Err() != nil {
		shutdown()
	}

	resultByIndex := make([]error, len(p.devices))
	for result := range results {
		if result.err == nil || result.cancelled || errors.Is(result.err, context.Canceled) || errors.Is(result.err, context.DeadlineExceeded) {
			continue
		}
		resultByIndex[result.index] = result.err
	}
	var runErrors []error
	for index, err := range resultByIndex {
		if err != nil {
			runErrors = append(runErrors, fmt.Errorf("device %s: %w", p.devices[index].generator.MAC, err))
		}
	}
	return errors.Join(runErrors...)
}

func (p *simulatorProcess) shutdown() {
	if p == nil {
		return
	}
	var group sync.WaitGroup
	for _, device := range p.devices {
		if device == nil {
			continue
		}
		group.Add(1)
		go func(device *deviceSimulator) {
			defer group.Done()
			device.shutdown()
		}(device)
	}
	group.Wait()
}

func (e *ackWaitError) Error() string {
	if e.timeout {
		return "ACK timeout"
	}
	return fmt.Sprintf("nonterminal ACK status=%s", e.status)
}

func main() {
	cfg, rawMACs, err := parseConfig()
	if err != nil {
		log.Fatal(err)
	}
	if err := runMain(cfg, rawMACs); err != nil {
		log.Fatal(err)
	}
}

func runMain(cfg config, rawMACs []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	process, err := startConfiguredDevices(ctx, cfg, rawMACs, nil)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return fmt.Errorf("device simulator startup failed: %w", err)
	}
	defer process.shutdown()
	if err := process.run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("device simulator failed: %w", err)
	}
	return nil
}

func parseConfig() (config, []string, error) {
	cfg := config{}
	var rawMACs repeatableMACFlag
	flag.StringVar(&cfg.Mode, "mode", envOr("SIMULATOR_MODE", "once"), "once|continuous|duplicate|invalid|offline-replay|reconnect")
	flag.StringVar(&cfg.BrokerURL, "mqtt-broker-url", envOr("MQTT_BROKER_URL", ""), "MQTTS broker URL, for example tls://127.0.0.1:8883")
	flag.StringVar(&cfg.Username, "mqtt-username", envOr("MQTT_USERNAME", ""), "MQTT username")
	flag.StringVar(&cfg.Password, "mqtt-password", envOr("MQTT_PASSWORD", ""), "MQTT password")
	flag.StringVar(&cfg.CAFile, "mqtt-ca-file", envOr("MQTT_CA_FILE", ""), "PEM CA file used to verify the broker")
	cfg.MAC = envOr("DEVICE_MAC", simulator.DefaultMAC)
	flag.Var(&rawMACs, "device-mac", "device MAC (repeatable)")
	flag.StringVar(&cfg.FirmwareVersion, "firmware-version", envOr("FIRMWARE_VERSION", "simulator-1.0.0"), "firmware version reported in telemetry")
	flag.DurationVar(&cfg.Interval, "publish-interval", durationEnv("PUBLISH_INTERVAL", 5*time.Second), "telemetry interval")
	flag.BoolVar(&cfg.CoverageProfile, "coverage-profile", boolEnv("COVERAGE_PROFILE", false), "emit coverage_version=1 intervals")
	flag.BoolVar(&cfg.ClockSynchronized, "clock-synchronized", boolEnv("CLOCK_SYNCHRONIZED", true), "allow coverage evidence only after clock synchronization")
	flag.Int64Var(&cfg.BootCounter, "boot-counter", int64Env("BOOT_COUNTER", 1), "simulated boot counter")
	flag.Int64Var(&cfg.StartSequence, "start-seq", int64Env("START_SEQ", 0), "first simulated sequence number")
	flag.IntVar(&cfg.ReplayCount, "replay-count", intEnv("REPLAY_COUNT", 5), "offline-replay queue length")
	flag.IntVar(&cfg.QueueCapacity, "queue-capacity", intEnv("QUEUE_CAPACITY", defaultQueueCapacity), "bounded in-memory telemetry queue capacity")
	flag.DurationVar(&cfg.AckTimeout, "ack-timeout", durationEnv("ACK_TIMEOUT", 10*time.Second), "time to wait for an application ACK")
	flag.BoolVar(&cfg.CommandAck, "command-ack", boolEnv("COMMAND_ACK", true), "publish command/ack after receiving a command")
	recordedAtText := envOr("RECORDED_AT", "")
	offlineWaitText := durationEnv("OFFLINE_WAIT", 0)
	reconnectMAC := envOr("RECONNECT_DEVICE_MAC", "")
	flag.StringVar(&recordedAtText, "recorded-at", recordedAtText, "RFC3339 telemetry recorded_at/ts base timestamp")
	flag.DurationVar(&offlineWaitText, "offline-wait", offlineWaitText, "wait while buffered offline before connecting")
	flag.StringVar(&reconnectMAC, "reconnect-device-mac", reconnectMAC, "in reconnect mode, run other configured devices continuously")
	flag.Parse()
	if recordedAtText != "" {
		recordedAt, err := parseRecordedAt(recordedAtText)
		if err != nil {
			return config{}, nil, err
		}
		cfg.RecordedAt = recordedAt
	}
	cfg.OfflineWait = offlineWaitText
	if strings.TrimSpace(reconnectMAC) != "" {
		canonical, err := simulator.NormalizeMAC(reconnectMAC)
		if err != nil {
			return config{}, nil, fmt.Errorf("reconnect device MAC: %w", err)
		}
		cfg.ReconnectMAC = canonical
	}

	if !isTLSBroker(cfg.BrokerURL) {
		return config{}, nil, fmt.Errorf("MQTT broker URL must use tls://; insecure transport is not supported")
	}
	if strings.TrimSpace(cfg.Username) == "" || cfg.Password == "" {
		return config{}, nil, fmt.Errorf("MQTT username and password are required")
	}
	if strings.TrimSpace(cfg.CAFile) == "" {
		return config{}, nil, fmt.Errorf("MQTT CA file is required")
	}
	if cfg.Interval <= 0 || cfg.AckTimeout <= 0 {
		return config{}, nil, fmt.Errorf("publish interval and ACK timeout must be positive")
	}
	if cfg.BootCounter < 0 || cfg.StartSequence < 0 || cfg.ReplayCount <= 0 || cfg.QueueCapacity <= 0 || cfg.OfflineWait < 0 {
		return config{}, nil, fmt.Errorf("boot counter, sequence, replay count, queue capacity, and offline wait are invalid")
	}
	if cfg.CoverageProfile && cfg.StartSequence != 0 {
		return config{}, nil, errors.New("coverage profile requires start sequence 0")
	}
	if cfg.Mode == "offline-replay" && cfg.ReplayCount > cfg.QueueCapacity {
		return config{}, nil, fmt.Errorf("offline replay count %d exceeds queue capacity %d", cfg.ReplayCount, cfg.QueueCapacity)
	}
	if len(rawMACs.values) == 0 {
		rawMACs.values = []string{cfg.MAC}
	}
	return cfg, rawMACs.values, nil
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
	// A waiter from the invalidated session may still receive a delayed
	// terminal ACK after its publish call returns. Quarantine that identity
	// until a fresh replay explicitly registers it.
	s.pruneStaleQueueWaiters()
	s.lifecycleMu.Unlock()
	log.Printf("MQTT connection lost: %v", err)
}

func (s *deviceSimulator) pruneStaleQueueWaiters() {
	// The caller holds lifecycleMu. Registration captures the epoch and inserts
	// under the same lock, so an invalidated waiter cannot remain ahead of a
	// fresh replay waiter.
	s.mu.Lock()
	defer s.mu.Unlock()
	for identity, waiters := range s.pending {
		kept := waiters[:0]
		for _, waiter := range waiters {
			if s.ackAttempt[waiter] {
				delete(s.ackEpoch, waiter)
				delete(s.ackQueueItem, waiter)
				delete(s.ackAttempt, waiter)
				delete(s.ackAttemptAuthorized, waiter)
				continue
			}
			kept = append(kept, waiter)
		}
		if len(kept) == 0 {
			delete(s.pending, identity)
		} else {
			s.pending[identity] = kept
		}
	}

	// A prior successful attempt is no longer authorized after loss. Any
	// terminal ACK already observed remains deferred until a fresh attempt
	// passes publish-token and epoch validation.
	s.queueMu.Lock()
	queue := s.queue
	s.queueMu.Unlock()
	if queue != nil {
		for _, item := range queue.Pending() {
			delete(s.ackAuthorized, item.Identity())
		}
	}
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
	s.pruneStaleQueueWaiters()
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

func (s *deviceSimulator) onMessage(client mqtt.Client, message mqtt.Message) {
	epoch, ok := s.currentMessageSession(client)
	if !ok {
		log.Printf("stale MQTT message ignored")
		return
	}
	switch message.Topic() {
	case s.ackTopic():
		ack, err := simulator.ParseAck(message.Payload())
		if err != nil {
			log.Printf("ACK malformed: %v", err)
			return
		}
		s.printAck(ack)
		s.resolveAckForSession(client, epoch, ack)
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
	identity := fmt.Sprintf("mac=%s boot=%d seq=%d", s.generator.MAC, ack.BootCounter, ack.Sequence)
	switch ack.Status {
	case "stored":
		log.Printf("ACK stored %s", identity)
	case "duplicate":
		log.Printf("ACK duplicate %s", identity)
	default:
		log.Printf("ACK rejected %s status=%s", identity, ack.Status)
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
	var err error
	switch s.config.Mode {
	case "once":
		err = s.runOnce(ctx)
	case "continuous":
		err = s.runContinuous(ctx)
	case "duplicate":
		err = s.runDuplicate(ctx)
	case "invalid":
		err = s.runInvalid(ctx)
	case "offline-replay":
		err = s.runOfflineReplay(ctx)
	case "reconnect":
		if s.config.ReconnectMAC != "" && s.generator.MAC != s.config.ReconnectMAC {
			err = s.runContinuous(ctx)
		} else {
			err = s.runReconnect(ctx)
		}
	default:
		err = fmt.Errorf("unsupported simulator mode %q", s.config.Mode)
	}
	queue, queueErr := s.telemetryQueue()
	if queueErr == nil {
		log.Printf("QUEUE pending=%d mac=%s", queue.Len(), s.generator.MAC)
	}
	return err
}

func (s *deviceSimulator) runOnce(ctx context.Context) error {
	telemetry, err := s.nextTelemetryChecked(time.Now())
	if err != nil {
		return err
	}
	return s.publishAndWait(ctx, telemetry)
}

func (s *deviceSimulator) runContinuous(ctx context.Context) error {
	ticker := time.NewTicker(s.config.Interval)
	defer ticker.Stop()
	telemetry, err := s.nextTelemetryChecked(time.Now())
	if err != nil {
		return err
	}
	if err := s.publishAndWait(ctx, telemetry); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("continuous ACK error: %v", err)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-ticker.C:
			telemetry, err := s.nextTelemetryChecked(now)
			if err != nil {
				return err
			}
			if err := s.publishAndWait(ctx, telemetry); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("continuous ACK error: %v", err)
			}
		}
	}
}

func (s *deviceSimulator) runDuplicate(ctx context.Context) error {
	telemetry, err := s.nextTelemetryChecked(time.Now())
	if err != nil {
		return err
	}
	if err := s.publishAndWait(ctx, telemetry); err != nil {
		return err
	}
	// The protocol intentionally publishes the same identity twice. The
	// second wire attempt is not a new queue item: its duplicate ACK cannot
	// corrupt the bounded queue or be confused with a later fresh identity.
	body, err := json.Marshal(telemetry)
	if err != nil {
		return err
	}
	s.replayMu.Lock()
	defer s.replayMu.Unlock()
	return s.publishItemAndWait(ctx, simulator.QueuedTelemetry{Telemetry: telemetry, Payload: body}, false)
}

func (s *deviceSimulator) runInvalid(ctx context.Context) error {
	telemetry, err := s.nextTelemetryChecked(time.Now())
	if err != nil {
		return err
	}
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

func (s *deviceSimulator) telemetryQueue() (*simulator.TelemetryQueue, error) {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	if s.queue != nil {
		return s.queue, nil
	}
	capacity := s.config.QueueCapacity
	if capacity == 0 {
		capacity = defaultQueueCapacity
	}
	queue, err := simulator.NewTelemetryQueue(capacity)
	if err != nil {
		return nil, err
	}
	s.queue = queue
	return queue, nil
}

func (s *deviceSimulator) replayPending(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	queue, err := s.telemetryQueue()
	if err != nil {
		return err
	}
	s.replayMu.Lock()
	defer s.replayMu.Unlock()
	return s.replayPendingLocked(ctx, queue)
}

func (s *deviceSimulator) replayPendingLocked(ctx context.Context, queue *simulator.TelemetryQueue) error {
	for _, item := range queue.Pending() {
		if err := ctx.Err(); err != nil {
			return err
		}
		// The snapshot is intentionally rechecked immediately before sending;
		// a terminal ACK may have removed this value since Pending was read.
		if !queue.IsPending(item.Identity()) {
			continue
		}
		if err := s.publishQueuedAndWait(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (s *deviceSimulator) runOfflineReplay(ctx context.Context) error {
	if err := s.prepareOfflineQueue(); err != nil {
		return err
	}
	return s.replayPending(ctx)
}

func (s *deviceSimulator) runReconnect(ctx context.Context) error {
	telemetry, err := s.nextTelemetryChecked(time.Now())
	if err != nil {
		return err
	}
	if err := s.publishAndDisconnectBeforeACK(ctx, telemetry); err != nil {
		// The deliberate session loss occurs after the broker accepted the
		// publish but before ACK authorization. The queue item therefore remains
		// pending and reconnect mode can prove exact replay behavior.
		if errors.Is(err, context.Canceled) {
			return err
		}
		log.Printf("RECONNECT first telemetry retained: %v", err)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	log.Printf("RECONNECT reconnecting MQTT client")
	s.drainReadyEvents()
	// If publication failed before the deliberate disconnect point, detach the
	// old client before installing a fresh one. Otherwise setClient would leave
	// an untracked Paho session that shutdown could not close.
	s.lifecycleMu.Lock()
	clientStillConfigured := s.client != nil
	s.lifecycleMu.Unlock()
	if clientStillConfigured && s.disconnectClientForReconnect() == nil {
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
	if err := s.replayPending(ctx); err != nil {
		return err
	}
	telemetry, err = s.nextTelemetryChecked(time.Now())
	if err != nil {
		return err
	}
	return s.publishAndWait(ctx, telemetry)
}

func (s *deviceSimulator) prepareOfflineQueue() error {
	if s.offlineQueue != nil {
		return nil
	}
	if s.config.ReplayCount <= 0 {
		return nil
	}
	queue, err := s.telemetryQueue()
	if err != nil {
		log.Printf("LOCAL_QUEUE unavailable: %v", err)
		return err
	}
	base := time.Now()
	if s.config.RecordedAt != nil {
		base = *s.config.RecordedAt
	}
	telemetryQueue := make([]simulator.Telemetry, 0, s.config.ReplayCount)
	logicalStart := base
	for i := 0; i < s.config.ReplayCount; i++ {
		start := logicalStart
		var telemetry simulator.Telemetry
		if s.config.CoverageProfile {
			end, endErr := simulator.CoverageIntervalEnd(start, s.config.Interval)
			if endErr != nil {
				return endErr
			}
			telemetry, telemetryErr := s.generator.NextCoverageCheckedWithClock(start, end, s.config.ClockSynchronized)
			if telemetryErr != nil {
				return telemetryErr
			}
			logicalStart = end
			telemetryQueue = append(telemetryQueue, telemetry)
			body, marshalErr := json.Marshal(telemetry)
			if marshalErr != nil {
				return marshalErr
			}
			if enqueueErr := queue.Enqueue(context.Background(), simulator.QueuedTelemetry{Telemetry: telemetry, Payload: body}); enqueueErr != nil {
				return enqueueErr
			}
			continue
		} else {
			telemetry = s.generator.Next(start, s.config.Interval)
		}
		body, marshalErr := json.Marshal(telemetry)
		if marshalErr != nil {
			return marshalErr
		}
		if enqueueErr := queue.Enqueue(context.Background(), simulator.QueuedTelemetry{Telemetry: telemetry, Payload: body}); enqueueErr != nil {
			return enqueueErr
		}
		telemetryQueue = append(telemetryQueue, telemetry)
		logicalStart = base.Add(time.Duration(i+1) * s.config.Interval)
	}
	s.offlineQueue = telemetryQueue
	log.Printf("LOCAL_QUEUE created=%d while disconnected", len(telemetryQueue))
	return nil
}

func (s *deviceSimulator) nextTelemetry(now time.Time) simulator.Telemetry {
	telemetry, _ := s.nextTelemetryChecked(now)
	return telemetry
}

func (s *deviceSimulator) nextTelemetryChecked(now time.Time) (simulator.Telemetry, error) {
	if !s.config.CoverageProfile {
		if s.config.RecordedAt != nil {
			offset := s.generator.Sequence - s.config.StartSequence
			now = s.config.RecordedAt.Add(time.Duration(offset) * s.config.Interval)
		}
		return s.generator.Next(now, s.config.Interval), nil
	}
	start := now
	if s.coverageNextStart != nil {
		start = *s.coverageNextStart
	} else if s.config.RecordedAt != nil {
		start = *s.config.RecordedAt
	}
	end, err := simulator.CoverageIntervalEnd(start, s.config.Interval)
	if err != nil {
		return simulator.Telemetry{}, err
	}
	telemetry, err := s.generator.NextCoverageCheckedWithClock(start, end, s.config.ClockSynchronized)
	if err != nil {
		return simulator.Telemetry{}, err
	}
	s.coverageNextStart = &end
	return telemetry, nil
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
	if ctx == nil {
		ctx = context.Background()
	}
	s.replayMu.Lock()
	defer s.replayMu.Unlock()

	body, err := json.Marshal(telemetry)
	if err != nil {
		return err
	}
	queue, err := s.telemetryQueue()
	if err != nil {
		return err
	}
	item := simulator.QueuedTelemetry{Telemetry: telemetry, Payload: body}
	if existing, ok := queue.Item(item.Identity()); ok {
		// A prior not-ready/failed attempt owns this identity. Retry its exact
		// bytes rather than replacing it with regenerated data.
		item = existing
		return s.publishQueuedAndWait(ctx, item)
	}
	// Enqueue precedes publication admission. Every failure after this point
	// therefore leaves the exact value available for a later replay. Preserve
	// Slice 2's behavior of admitting an already-cancelled, non-blocked send;
	// cancellation is observed after publication while a full queue still uses
	// the context for bounded backpressure.
	enqueueContext := ctx
	if ctx != nil && ctx.Err() != nil && queue.Len() < queue.Capacity() {
		enqueueContext = context.Background()
	}
	if err := queue.Enqueue(enqueueContext, item); err != nil {
		return err
	}
	// Do not let a newly generated item bypass older retained values. The
	// queue snapshot remains FIFO, and each identity is rechecked before send.
	return s.replayPendingLocked(ctx, queue)
}

func (s *deviceSimulator) publishAndDisconnectBeforeACK(ctx context.Context, telemetry simulator.Telemetry) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.replayMu.Lock()
	defer s.replayMu.Unlock()

	body, err := json.Marshal(telemetry)
	if err != nil {
		return err
	}
	queue, err := s.telemetryQueue()
	if err != nil {
		return err
	}
	item := simulator.QueuedTelemetry{Telemetry: telemetry, Payload: body}
	if existing, ok := queue.Item(item.Identity()); ok {
		item = existing
	}
	if !queue.IsPending(item.Identity()) {
		enqueueContext := ctx
		if ctx.Err() != nil && queue.Len() < queue.Capacity() {
			enqueueContext = context.Background()
		}
		if err := queue.Enqueue(enqueueContext, item); err != nil {
			return err
		}
	}

	identity := item.Identity()
	waiter := s.registerAckMode(identity, true, true)
	defer func() {
		s.revokeAckAttempt(identity, waiter, true)
		s.removeAck(identity, waiter)
	}()
	if !queue.IsPending(identity) {
		return nil
	}
	attempt, err := s.publishWhenReady("device/upload/data", 0, false, item.Payload)
	if err != nil {
		return err
	}
	if err := waitMQTTToken(attempt.token, 5*time.Second); err != nil {
		return fmt.Errorf("telemetry publish failed: %w", err)
	}
	log.Printf("RECONNECT pending boot=%d seq=%d", item.Telemetry.BootCounter, item.Telemetry.Sequence)
	if s.disconnectClientForReconnect() == nil {
		return errors.New("MQTT reconnect failed: client is not configured")
	}
	return errMQTTSessionChanged
}

func (s *deviceSimulator) publishQueuedAndWait(ctx context.Context, item simulator.QueuedTelemetry) error {
	return s.publishItemAndWait(ctx, item, true)
}

func (s *deviceSimulator) publishItemAndWait(ctx context.Context, item simulator.QueuedTelemetry, queueItem bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	queue, err := s.telemetryQueue()
	if err != nil {
		return err
	}
	identity := item.Identity()
	if queueItem && !queue.IsPending(identity) {
		return errors.New("queued telemetry is no longer pending")
	}

	// Register immediately before external Publish. resolveAck performs the
	// queue completion and waiter notification under the same lifecycle/ACK
	// ordering, so a terminal ACK is consumed exactly once.
	waiter := s.registerAckMode(identity, queueItem, true)
	defer func() {
		s.revokeAckAttempt(identity, waiter, queueItem)
		s.removeAck(identity, waiter)
	}()
	if queueItem && !queue.IsPending(identity) {
		// A terminal ACK won the small admission race after the snapshot
		// check. Do not publish an already-completed item.
		return nil
	}
	attempt, err := s.publishWhenReady("device/upload/data", 0, false, item.Payload)
	if err != nil {
		return err
	}
	if err := waitMQTTToken(attempt.token, 5*time.Second); err != nil {
		return fmt.Errorf("telemetry publish failed: %w", err)
	}
	if err := s.validateReadyPublish(attempt); err != nil {
		return err
	}
	s.releaseAckAttempt(identity, attempt, queueItem)
	log.Printf("PUBLISHED boot=%d seq=%d mac=%s", item.Telemetry.BootCounter, item.Telemetry.Sequence, item.Telemetry.MAC)
	ackTimeout := s.config.AckTimeout
	if ackTimeout <= 0 {
		ackTimeout = 10 * time.Second
	}
	timer := time.NewTimer(ackTimeout)
	defer timer.Stop()
	handleACK := func(ack simulator.Ack) error {
		if !ack.IsTerminal() {
			s.revokeAckAttempt(identity, waiter, queueItem)
			return &ackWaitError{status: ack.Status}
		}
		if err := s.validateReadyPublish(attempt); err != nil {
			return err
		}
		return nil
	}
	select {
	case <-ctx.Done():
		s.revokeAckAttempt(identity, waiter, queueItem)
		return ctx.Err()
	case <-attempt.change:
		s.revokeAckAttempt(identity, waiter, queueItem)
		return errMQTTSessionChanged
	case ack := <-waiter:
		return handleACK(ack)
	case <-timer.C:
		if s.ackTimeoutArbitrationHook != nil {
			s.ackTimeoutArbitrationHook(ackKey(identity))
		}
		// Timer and ACK readiness can coincide. Inspect the waiter and revoke
		// authorization atomically with resolveAckForSession's lifecycle/ACK
		// critical section. Validation stays outside the locks because it
		// reacquires lifecycleMu.
		ack, ready := s.arbitrateAckTimeout(identity, waiter, queueItem)
		if ready {
			return handleACK(ack)
		}
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
	change := s.readyChanged
	if change == nil {
		change = make(chan struct{})
		s.readyChanged = change
	}
	attempt := publishAttempt{client: s.client, epoch: s.sessionEpoch, change: change}
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
	// Preserve the original waiter seam for tests and command paths, while
	// treating an already-pending telemetry identity as queue-owned.
	s.queueMu.Lock()
	queue := s.queue
	s.queueMu.Unlock()
	return s.registerAckMode(identity, queue != nil && queue.IsPending(identity), false)
}

func (s *deviceSimulator) registerQueuedAck(identity simulator.TelemetryIdentity) chan simulator.Ack {
	return s.registerAckMode(identity, true, true)
}

func (s *deviceSimulator) registerAckMode(identity simulator.TelemetryIdentity, queueItem bool, attempt bool) chan simulator.Ack {
	waiter := make(chan simulator.Ack, 1)
	// Capture the current session and insert the waiter under one lifecycle
	// critical section. A loss cannot invalidate the epoch between these steps.
	s.lifecycleMu.Lock()
	epoch := s.sessionEpoch
	if s.ackRegistrationHook != nil {
		s.ackRegistrationHook()
	}
	s.mu.Lock()
	if s.pending == nil {
		s.pending = make(map[ackKey][]chan simulator.Ack)
	}
	if s.ackEpoch == nil {
		s.ackEpoch = make(map[chan simulator.Ack]uint64)
	}
	if s.ackQueueItem == nil {
		s.ackQueueItem = make(map[chan simulator.Ack]bool)
	}
	if s.ackAttempt == nil {
		s.ackAttempt = make(map[chan simulator.Ack]bool)
	}
	if s.ackAttemptAuthorized == nil {
		s.ackAttemptAuthorized = make(map[chan simulator.Ack]bool)
	}
	if queueItem {
		if s.ackAuthorized == nil {
			s.ackAuthorized = make(map[ackKey]bool)
		}
		delete(s.ackAuthorized, identity)
	}
	s.pending[identity] = append(s.pending[identity], waiter)
	s.ackEpoch[waiter] = epoch
	s.ackQueueItem[waiter] = queueItem
	s.ackAttempt[waiter] = attempt
	s.ackAttemptAuthorized[waiter] = false
	s.mu.Unlock()
	s.lifecycleMu.Unlock()
	return waiter
}

func (s *deviceSimulator) revokeAckAttemptLocked(identity simulator.TelemetryIdentity, waiter chan simulator.Ack, queueItem bool) {
	delete(s.ackAttemptAuthorized, waiter)
	if queueItem {
		delete(s.ackAuthorized, identity)
	}
}

func (s *deviceSimulator) revokeAckAttempt(identity simulator.TelemetryIdentity, waiter chan simulator.Ack, queueItem bool) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revokeAckAttemptLocked(identity, waiter, queueItem)
}

func (s *deviceSimulator) arbitrateAckTimeout(identity simulator.TelemetryIdentity, waiter chan simulator.Ack, queueItem bool) (simulator.Ack, bool) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	select {
	case ack := <-waiter:
		return ack, true
	default:
		if s.ackTimeoutArbitrationInspectionHook != nil {
			s.ackTimeoutArbitrationInspectionHook(ackKey(identity))
		}
		s.revokeAckAttemptLocked(identity, waiter, queueItem)
		return simulator.Ack{}, false
	}
}

func (s *deviceSimulator) releaseAckAttempt(identity simulator.TelemetryIdentity, attempt publishAttempt, queueItem bool) {
	if queueItem && s.queuedACKReleaseHook != nil {
		s.queuedACKReleaseHook(ackKey(identity))
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopping || !s.ready || s.client != attempt.client || s.sessionEpoch != attempt.epoch {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	waiters := s.pending[identity]
	if len(waiters) == 0 {
		return
	}
	waiter := waiters[0]
	if !queueItem {
		// Duplicate mode has no queue item to complete. Do not accept an ACK
		// until this specific second publish has passed token/epoch validation.
		s.ackAttemptAuthorized[waiter] = true
		return
	}
	if s.ackAuthorized == nil {
		s.ackAuthorized = make(map[ackKey]bool)
	}
	s.ackAuthorized[identity] = true
	s.ackAttemptAuthorized[waiter] = true
	ack, deferred := s.deferredACK[identity]
	if !deferred {
		return
	}
	delete(s.deferredACK, identity)
	s.queueMu.Lock()
	queue := s.queue
	s.queueMu.Unlock()
	if queue == nil || !queue.HandleAck(ack) {
		return
	}
	if len(waiters) == 1 {
		delete(s.pending, identity)
	} else {
		s.pending[identity] = waiters[1:]
	}
	delete(s.ackEpoch, waiter)
	delete(s.ackQueueItem, waiter)
	delete(s.ackAttempt, waiter)
	delete(s.ackAttemptAuthorized, waiter)
	select {
	case waiter <- ack:
	default:
	}
}

func (s *deviceSimulator) currentMessageSession(client mqtt.Client) (uint64, bool) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopping || !s.ready || s.client == nil || client == nil || client != s.client {
		return 0, false
	}
	return s.sessionEpoch, true
}

func (s *deviceSimulator) resolveAck(ack simulator.Ack) {
	s.resolveAckForSession(nil, 0, ack)
}

func (s *deviceSimulator) resolveAckForSession(client mqtt.Client, epoch uint64, ack simulator.Ack) {
	key := ack.Identity()
	// Keep lifecycle ahead of ACK state. This gives connection loss a clear
	// linearization point: ACKs observed after the epoch changes are stale.
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if client != nil && (s.stopping || !s.ready || s.client != client || s.sessionEpoch != epoch) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	waiters := s.pending[key]
	s.queueMu.Lock()
	queue := s.queue
	s.queueMu.Unlock()
	queuePending := queue != nil && queue.IsPending(key)

	// Queue-owned terminal ACKs are never allowed to delete before a fresh
	// attempt has completed Publish-token and epoch validation. This covers
	// ACKs arriving before registration, during Publish, and after timeout.
	if ack.IsTerminal() && queuePending {
		if !s.ackAuthorized[key] {
			if s.deferredACK == nil {
				s.deferredACK = make(map[ackKey]simulator.Ack)
			}
			s.deferredACK[key] = ack
			return
		}
		if !queue.HandleAck(ack) {
			return
		}
		delete(s.ackAuthorized, key)
		delete(s.deferredACK, key)
		if len(waiters) == 0 {
			return
		}
	}
	if len(waiters) == 0 {
		return
	}
	waiter := waiters[0]
	if waiterEpoch, ok := s.ackEpoch[waiter]; ok && waiterEpoch != s.sessionEpoch {
		return
	}
	if s.ackQueueItem[waiter] && (queue == nil || !queuePending) {
		return
	}
	if ack.IsTerminal() && s.ackAttempt[waiter] && !s.ackAttemptAuthorized[waiter] {
		// Duplicate-mode terminal ACKs are accepted only after this exact
		// publish attempt has passed token and epoch validation.
		return
	}
	if len(waiters) == 1 {
		delete(s.pending, key)
	} else {
		s.pending[key] = waiters[1:]
	}
	delete(s.ackEpoch, waiter)
	delete(s.ackQueueItem, waiter)
	delete(s.ackAttempt, waiter)
	delete(s.ackAttemptAuthorized, waiter)
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
			delete(s.ackEpoch, target)
			delete(s.ackQueueItem, target)
			delete(s.ackAttempt, target)
			delete(s.ackAttemptAuthorized, target)
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
	s.pruneStaleQueueWaiters()
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
