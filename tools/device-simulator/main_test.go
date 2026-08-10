package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"power-iot-device-simulator/internal/simulator"
)

func TestParseRecordedAt(t *testing.T) {
	recordedAt, err := parseRecordedAt("2026-08-08T04:00:01+08:00")
	if err != nil {
		t.Fatal(err)
	}
	if recordedAt == nil || !recordedAt.Equal(time.Date(2026, 8, 7, 20, 0, 1, 0, time.UTC)) {
		t.Fatalf("unexpected recorded_at: %v", recordedAt)
	}
	if _, err := parseRecordedAt("not-a-timestamp"); err == nil {
		t.Fatal("accepted invalid RFC3339 timestamp")
	}
}

func TestNextTelemetryUsesConfiguredRecordedAt(t *testing.T) {
	recordedAt := time.Unix(1786021200, 0).UTC()
	generator, err := simulator.NewGenerator(simulator.DefaultMAC, "test-fw", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	device := &deviceSimulator{
		config:    config{Interval: 5 * time.Second, StartSequence: 10, RecordedAt: &recordedAt},
		generator: generator,
	}
	first := device.nextTelemetry(time.Now())
	second := device.nextTelemetry(time.Now().Add(time.Hour))
	if first.Timestamp != recordedAt.Unix() || second.Timestamp != recordedAt.Add(5*time.Second).Unix() {
		t.Fatalf("unexpected deterministic timestamps: %d, %d", first.Timestamp, second.Timestamp)
	}
}

func TestSimulatorStartsNotReady(t *testing.T) {
	device := newLifecycleDevice(t)
	if device.isReady() {
		t.Fatal("new simulator reported ready before MQTT subscriptions")
	}
}

func TestRequiredSubscriptionFailureLeavesSimulatorNotReady(t *testing.T) {
	device := newLifecycleDevice(t)
	client := newLifecycleFakeClient()
	client.setSubscribeToken(device.commandTopic(), immediateToken{err: errors.New("command subscription failed")})
	installClient(t, device, client)

	device.onConnect(client)

	if device.isReady() {
		t.Fatal("simulator became ready after a required subscription failed")
	}
	if got := client.countStatus(true); got != 0 {
		t.Fatalf("online status published after subscription failure: %d", got)
	}
}

func TestRequiredSubscriptionGrantFailureLeavesSimulatorNotReady(t *testing.T) {
	tests := []struct {
		name   string
		topic  func(*deviceSimulator) string
		result func(string) map[string]byte
	}{
		{
			name:  "ack subscription rejected",
			topic: (*deviceSimulator).ackTopic,
			result: func(topic string) map[string]byte {
				return map[string]byte{topic: 0x80}
			},
		},
		{
			name:  "command subscription rejected",
			topic: (*deviceSimulator).commandTopic,
			result: func(topic string) map[string]byte {
				return map[string]byte{topic: 0x80}
			},
		},
		{
			name:   "ack subscription result missing",
			topic:  (*deviceSimulator).ackTopic,
			result: func(string) map[string]byte { return map[string]byte{} },
		},
		{
			name:   "command subscription result missing",
			topic:  (*deviceSimulator).commandTopic,
			result: func(string) map[string]byte { return map[string]byte{} },
		},
		{
			name:  "subscription downgraded to qos zero",
			topic: (*deviceSimulator).ackTopic,
			result: func(topic string) map[string]byte {
				return map[string]byte{topic: 0}
			},
		},
		{
			name:  "subscription grant exceeds requested qos",
			topic: (*deviceSimulator).commandTopic,
			result: func(topic string) map[string]byte {
				return map[string]byte{topic: 2}
			},
		},
		{
			name:  "subscription has invalid grant",
			topic: (*deviceSimulator).commandTopic,
			result: func(topic string) map[string]byte {
				return map[string]byte{topic: 0x7f}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			device := newLifecycleDevice(t)
			client := newLifecycleFakeClient()
			topic := test.topic(device)
			client.setSubscribeToken(topic, fakeSubscribeToken{result: test.result(topic)})
			installClient(t, device, client)

			device.onConnect(client)

			if device.isReady() {
				t.Fatal("simulator became ready after an unacceptable SUBACK grant")
			}
			if got := client.countStatus(true); got != 0 {
				t.Fatalf("online status published after unacceptable SUBACK grant: %d", got)
			}
		})
	}
}

func TestOnlineStatusFailureLeavesSimulatorNotReady(t *testing.T) {
	device := newLifecycleDevice(t)
	client := newLifecycleFakeClient()
	client.setPublishToken(device.statusTopic(), immediateToken{err: errors.New("status publish failed")})
	installClient(t, device, client)

	device.onConnect(client)

	if device.isReady() {
		t.Fatal("simulator became ready after online status failed")
	}
	if got := client.subscriptionCount(); got != 2 {
		t.Fatalf("subscription count=%d, want both required subscriptions", got)
	}
}

func TestSuccessfulRestorationEstablishesReadiness(t *testing.T) {
	device := newLifecycleDevice(t)
	client := newLifecycleFakeClient()
	establishReady(t, device, client)

	if got := client.subscriptionCount(); got != 2 {
		t.Fatalf("subscription count=%d, want both required subscriptions", got)
	}
	if got := client.countStatus(true); got != 1 {
		t.Fatalf("online status count=%d, want 1", got)
	}
	if !client.statusWasRetained(true) {
		t.Fatal("successful online status was not retained")
	}
}

func TestClientOptionsDisableSameClientAutoReconnect(t *testing.T) {
	device := newLifecycleDevice(t)
	broker, err := url.Parse("tls://broker.example:8883")
	if err != nil {
		t.Fatal(err)
	}

	opts := device.clientOptions("test-client", x509.NewCertPool(), broker)

	if opts.AutoReconnect {
		t.Fatal("Paho AutoReconnect is enabled; reconnect must use a fresh client")
	}
	if opts.OnReconnecting != nil {
		t.Fatal("production options installed a same-client reconnect handler")
	}
	if opts.OnConnect == nil || opts.OnConnectionLost == nil {
		t.Fatal("required lifecycle handlers were not installed")
	}
}

func TestClientOptionsDisableOrderedMessageHandlers(t *testing.T) {
	device := newLifecycleDevice(t)
	broker, err := url.Parse("tls://broker.example:8883")
	if err != nil {
		t.Fatal(err)
	}

	opts := device.clientOptions("test-client", x509.NewCertPool(), broker)

	if opts.Order {
		t.Fatal("Paho ordered message handlers are enabled; command ACK waits could block message routing")
	}
}

func TestConnectionLossDuringEachRestorationOperationCannotCommitReady(t *testing.T) {
	for _, stage := range []string{"ack subscription", "command subscription", "online status"} {
		t.Run(stage, func(t *testing.T) {
			device := newLifecycleDevice(t)
			device.mqttOperationTimeout = time.Second
			client := newLifecycleFakeClient()
			gate := newGatedToken()
			switch stage {
			case "ack subscription":
				client.setSubscribeToken(device.ackTopic(), gate)
			case "command subscription":
				client.setSubscribeToken(device.commandTopic(), gate)
			case "online status":
				client.setPublishToken(device.statusTopic(), gate)
			}
			installClient(t, device, client)

			restoreDone := make(chan struct{})
			go func() {
				device.onConnect(client)
				close(restoreDone)
			}()
			waitClosed(t, gate.started, "restoration operation did not start")
			assertReturns(t, func() {
				device.onConnectionLost(client, errors.New("loss during restoration"))
			}, "connection loss invalidation blocked behind restoration token wait")
			close(gate.release)
			waitClosed(t, restoreDone, "restoration callback did not return")

			if device.isReady() {
				t.Fatal("stale restoration committed READY after connection loss")
			}
		})
	}
}

func TestNeverCompletingSubscribeTimesOutWithoutHoldingLifecycleLocks(t *testing.T) {
	device := newLifecycleDevice(t)
	device.mqttOperationTimeout = 500 * time.Millisecond
	client := newLifecycleFakeClient()
	never := newNeverCompletingToken()
	client.setSubscribeToken(device.ackTopic(), never)
	installClient(t, device, client)

	restoreDone := make(chan struct{})
	go func() {
		device.onConnect(client)
		close(restoreDone)
	}()
	waitClosed(t, never.started, "Subscribe token wait did not start")
	assertReturns(t, func() {
		device.onConnectionLost(client, errors.New("loss during Subscribe wait"))
	}, "connection loss invalidation blocked behind Subscribe token wait")
	waitClosed(t, restoreDone, "never-completing Subscribe did not use a finite timeout")
	if device.isReady() {
		t.Fatal("timed-out restoration committed READY")
	}
}

func TestNeverCompletingOnlineStatusTimesOutWithoutHoldingLifecycleLocks(t *testing.T) {
	device := newLifecycleDevice(t)
	device.mqttOperationTimeout = 500 * time.Millisecond
	client := newLifecycleFakeClient()
	never := newNeverCompletingToken()
	client.setPublishToken(device.statusTopic(), never)
	installClient(t, device, client)

	restoreDone := make(chan struct{})
	go func() {
		device.onConnect(client)
		close(restoreDone)
	}()
	waitClosed(t, never.started, "online status token wait did not start")
	assertReturns(t, func() {
		device.onConnectionLost(client, errors.New("loss during status wait"))
	}, "connection loss invalidation blocked behind online status token wait")
	waitClosed(t, restoreDone, "never-completing online status did not use a finite timeout")
	if device.isReady() {
		t.Fatal("timed-out online status committed READY")
	}
}

func TestFreshClientReplacementFencesOldClientCallbacks(t *testing.T) {
	device := newLifecycleDevice(t)
	oldClient := newLifecycleFakeClient()
	establishReady(t, device, oldClient)
	oldSubscriptions := oldClient.subscriptionCount()
	oldOnline := oldClient.countStatus(true)
	if detached := device.disconnectClientForReconnect(); detached != oldClient {
		t.Fatal("explicit reconnect did not detach the old client")
	}
	newClient := newLifecycleFakeClient()
	establishReady(t, device, newClient)

	device.onConnectionLost(oldClient, errors.New("old client loss"))
	device.onConnect(oldClient)

	if !device.isReady() {
		t.Fatal("old client callback invalidated replacement READY state")
	}
	if got := oldClient.subscriptionCount(); got != oldSubscriptions {
		t.Fatalf("old callback added subscriptions: got=%d want=%d", got, oldSubscriptions)
	}
	if got := oldClient.countStatus(true); got != oldOnline {
		t.Fatalf("old callback published online status: got=%d want=%d", got, oldOnline)
	}
}

func TestConcurrentStaleCallbacksCannotAffectFreshClient(t *testing.T) {
	device := newLifecycleDevice(t)
	oldClient := newLifecycleFakeClient()
	establishReady(t, device, oldClient)
	if detached := device.disconnectClientForReconnect(); detached != oldClient {
		t.Fatal("explicit reconnect did not detach the old client")
	}
	newClient := newLifecycleFakeClient()
	establishReady(t, device, newClient)

	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			if i%2 == 0 {
				device.onConnectionLost(oldClient, errors.New("stale loss"))
				return
			}
			device.onConnect(oldClient)
		}(i)
	}
	group.Wait()
	if !device.isReady() {
		t.Fatal("stale callbacks invalidated the fresh client")
	}
}

func TestPublishRequiresCurrentReadyEpoch(t *testing.T) {
	device := newLifecycleDevice(t)
	client := newLifecycleFakeClient()
	installClient(t, device, client)

	if err := device.publishAndWait(context.Background(), simulator.Telemetry{}); err == nil {
		t.Fatal("publish proceeded while simulator was not ready")
	}
	if got := client.countPublished("device/upload/data"); got != 0 {
		t.Fatalf("telemetry published while not ready: %d", got)
	}

	device.onConnect(client)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := device.publishAndWait(ctx, simulator.Telemetry{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ready publish returned %v, want context canceled after publish", err)
	}
	if got := client.countPublished("device/upload/data"); got != 1 {
		t.Fatalf("ready publish count=%d, want 1", got)
	}

	device.onConnectionLost(client, errors.New("test connection loss"))
	if err := device.publishAndWait(context.Background(), simulator.Telemetry{}); err == nil {
		t.Fatal("publish proceeded after reconnect invalidated the ready epoch")
	}
	if got := client.countPublished("device/upload/data"); got != 1 {
		t.Fatalf("publish count after invalidation=%d, want 1", got)
	}
}

func TestBlockingPublishDoesNotBlockLifecycleInvalidation(t *testing.T) {
	t.Run("telemetry publish lets shutdown invalidate before waiting for admission", func(t *testing.T) {
		device := newLifecycleDevice(t)
		client := newLifecycleFakeClient()
		establishReady(t, device, client)
		gate := newPublishCallGate()
		client.setPublishCallGate("device/upload/data", gate)
		telemetry := simulator.Telemetry{BootCounter: 1, Sequence: 42}

		result := make(chan error, 1)
		go func() {
			result <- device.publishAndWait(context.Background(), telemetry)
		}()
		waitClosed(t, gate.started, "telemetry Publish call did not block")
		shutdownDone := make(chan struct{})
		go func() {
			device.shutdown()
			close(shutdownDone)
		}()
		waitForCondition(t, func() bool { return simulatorIsStopping(device) }, "shutdown did not establish terminal state")
		select {
		case <-shutdownDone:
			t.Fatal("shutdown bypassed an admitted Publish")
		default:
		}
		close(gate.release)
		waitClosed(t, shutdownDone, "shutdown did not resume after admitted Publish returned")

		select {
		case err := <-result:
			if !errors.Is(err, errMQTTSessionChanged) {
				t.Fatalf("blocked telemetry publish returned %v, want session change", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("blocked telemetry publish did not return")
		}
		if device.isReady() {
			t.Fatal("shutdown did not invalidate readiness")
		}
	})

	t.Run("online status publish allows connection loss", func(t *testing.T) {
		device := newLifecycleDevice(t)
		client := newLifecycleFakeClient()
		gate := newPublishCallGate()
		client.setPublishCallGate(device.statusTopic(), gate)
		installClient(t, device, client)

		restoreDone := make(chan struct{})
		go func() {
			device.onConnect(client)
			close(restoreDone)
		}()
		waitClosed(t, gate.started, "online status Publish call did not block")
		assertReturns(t, func() {
			device.onConnectionLost(client, errors.New("loss during Publish call"))
		}, "connection loss blocked behind online status Publish")
		close(gate.release)
		waitClosed(t, restoreDone, "online status restoration did not return")

		if device.isReady() {
			t.Fatal("status queued by a stale epoch established readiness")
		}
	})
}

func TestShutdownOrdersOfflineAfterAdmittedOnlineStatus(t *testing.T) {
	device := newLifecycleDevice(t)
	client := newLifecycleFakeClient()
	gate := newPublishCallGate()
	client.setPublishAdmissionGate(device.statusTopic(), gate)
	installClient(t, device, client)

	restoreDone := make(chan struct{})
	go func() {
		device.onConnect(client)
		close(restoreDone)
	}()
	waitClosed(t, gate.started, "online Publish did not pause before its side effect")

	shutdownDone := make(chan struct{})
	go func() {
		device.shutdown()
		close(shutdownDone)
	}()
	waitForCondition(t, func() bool { return simulatorIsStopping(device) }, "shutdown did not establish terminal state")
	select {
	case <-shutdownDone:
		t.Fatal("shutdown published offline before the admitted online Publish completed")
	default:
	}

	close(gate.release)
	waitClosed(t, restoreDone, "admitted online Publish did not return")
	waitClosed(t, shutdownDone, "shutdown did not publish offline after the admitted Publish")

	statuses := client.statusPublications()
	if len(statuses) != 2 {
		t.Fatalf("status publication count=%d, want admitted online then shutdown offline", len(statuses))
	}
	firstOnline, firstOK := statusOnline(statuses[0].payload)
	lastOnline, lastOK := statusOnline(statuses[1].payload)
	if !firstOK || !firstOnline {
		t.Fatalf("first status was not online: %+v", statuses[0])
	}
	if !lastOK || lastOnline || !statuses[1].retained {
		t.Fatalf("final retained status was not offline: %+v", statuses[1])
	}
	if !client.offlinePrecededDisconnect() {
		t.Fatal("final offline status did not precede disconnect")
	}
}

func TestTelemetryTokenCompletionAfterConnectionLossIsNotSuccess(t *testing.T) {
	device := newLifecycleDevice(t)
	client := newLifecycleFakeClient()
	establishReady(t, device, client)
	gate := newGatedToken()
	client.setPublishToken("device/upload/data", gate)

	result := make(chan error, 1)
	go func() {
		result <- device.publishAndWait(context.Background(), simulator.Telemetry{BootCounter: 1, Sequence: 7})
	}()
	waitClosed(t, gate.started, "telemetry token wait did not start")
	device.onConnectionLost(client, errors.New("loss during telemetry token wait"))
	close(gate.release)

	select {
	case err := <-result:
		if !errors.Is(err, errMQTTSessionChanged) {
			t.Fatalf("telemetry publish returned %v, want session change", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("telemetry publish did not return after token completion")
	}
}

func TestTelemetryAckAfterConnectionLossIsNotSuccess(t *testing.T) {
	device := newLifecycleDevice(t)
	client := newLifecycleFakeClient()
	establishReady(t, device, client)
	telemetry := simulator.Telemetry{BootCounter: 1, Sequence: 8}

	result := make(chan error, 1)
	go func() {
		result <- device.publishAndWait(context.Background(), telemetry)
	}()
	waitForCondition(t, func() bool {
		return client.countPublished("device/upload/data") == 1
	}, "telemetry was not published")
	device.onConnectionLost(client, errors.New("loss before telemetry ACK"))
	device.resolveAck(simulator.Ack{BootCounter: 1, Sequence: 8, Status: "stored"})

	select {
	case err := <-result:
		if !errors.Is(err, errMQTTSessionChanged) {
			t.Fatalf("telemetry ACK returned %v, want session change", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("telemetry ACK did not resolve publisher")
	}
}

func TestStaleClientMessagesCannotResolveReplacementQueue(t *testing.T) {
	device := newLifecycleDevice(t)
	oldClient := newLifecycleFakeClient()
	establishReady(t, device, oldClient)
	if device.disconnectClientForReconnect() != oldClient {
		t.Fatal("did not detach old client")
	}
	newClient := newLifecycleFakeClient()
	establishReady(t, device, newClient)

	queue, err := device.telemetryQueue()
	if err != nil {
		t.Fatal(err)
	}
	telemetry := simulator.Telemetry{MAC: simulator.DefaultMAC, BootCounter: 31, Sequence: 310, Timestamp: 1786021200}
	if err := queue.Enqueue(context.Background(), simulator.QueuedTelemetry{Telemetry: telemetry, Payload: []byte(`{"boot_counter":31,"seq":310}`)}); err != nil {
		t.Fatal(err)
	}
	ackPayload := []byte(`{"boot_counter":31,"seq":310,"status":"stored"}`)
	replayDone := make(chan error, 1)
	go func() { replayDone <- device.replayPending(context.Background()) }()
	waitForCondition(t, func() bool { return newClient.countPublished("device/upload/data") == 1 }, "replacement replay did not publish")
	device.onMessage(oldClient, testMQTTMessage{topic: device.ackTopic(), payload: ackPayload})
	if queue.Len() != 1 {
		t.Fatal("stale old-client ACK removed replacement queue item")
	}

	device.onMessage(newClient, testMQTTMessage{topic: device.ackTopic(), payload: ackPayload})
	select {
	case err := <-replayDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("current-client ACK did not resolve replacement replay")
	}
	if queue.Len() != 0 {
		t.Fatal("current-client terminal ACK did not complete queue item")
	}
}

func TestStaleQueueWaiterIsPrunedBeforeFreshReplay(t *testing.T) {
	device := newLifecycleDevice(t)
	oldClient := newLifecycleFakeClient()
	establishReady(t, device, oldClient)
	queue, err := device.telemetryQueue()
	if err != nil {
		t.Fatal(err)
	}
	telemetry := simulator.Telemetry{MAC: simulator.DefaultMAC, BootCounter: 32, Sequence: 320, Timestamp: 1786021200}
	if err := queue.Enqueue(context.Background(), simulator.QueuedTelemetry{Telemetry: telemetry, Payload: []byte(`{"boot_counter":32,"seq":320}`)}); err != nil {
		t.Fatal(err)
	}
	staleWaiter := device.registerQueuedAck(telemetry.Identity())

	device.onConnectionLost(oldClient, errors.New("loss with pending replay"))
	newClient := newLifecycleFakeClient()
	establishReady(t, device, newClient)
	ackMessage := testMQTTMessage{
		topic:   device.ackTopic(),
		payload: []byte(`{"boot_counter":32,"seq":320,"status":"stored"}`),
	}
	replayDone := make(chan error, 1)
	go func() { replayDone <- device.replayPending(context.Background()) }()
	waitForCondition(t, func() bool { return newClient.countPublished("device/upload/data") == 1 }, "fresh replay did not publish")
	assertNoAck(t, staleWaiter, "stale queue waiter")
	device.onMessage(newClient, ackMessage)
	select {
	case err := <-replayDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh replay was blocked by stale queue waiter")
	}
	if queue.Len() != 0 {
		t.Fatalf("current terminal ACK left queue length=%d", queue.Len())
	}

	device.onMessage(newClient, ackMessage)
	if queue.Len() != 0 {
		t.Fatal("duplicate current terminal ACK changed completed queue")
	}
}

func TestAckRegistrationCannotCrossEpochInvalidation(t *testing.T) {
	device := newLifecycleDevice(t)
	oldClient := newLifecycleFakeClient()
	establishReady(t, device, oldClient)
	queue, err := device.telemetryQueue()
	if err != nil {
		t.Fatal(err)
	}
	telemetry := simulator.Telemetry{MAC: simulator.DefaultMAC, BootCounter: 34, Sequence: 340, Timestamp: 1786021200}
	if err := queue.Enqueue(context.Background(), simulator.QueuedTelemetry{Telemetry: telemetry, Payload: []byte(`{"boot_counter":34,"seq":340}`)}); err != nil {
		t.Fatal(err)
	}

	registrationStarted := make(chan struct{})
	registrationRelease := make(chan struct{})
	device.ackRegistrationHook = func() {
		close(registrationStarted)
		<-registrationRelease
	}
	staleWaiterDone := make(chan chan simulator.Ack, 1)
	go func() { staleWaiterDone <- device.registerQueuedAck(telemetry.Identity()) }()
	waitClosed(t, registrationStarted, "ACK registration did not reach the epoch boundary")

	lossDone := make(chan struct{})
	go func() {
		device.onConnectionLost(oldClient, errors.New("loss crossing ACK registration"))
		close(lossDone)
	}()
	select {
	case <-lossDone:
		t.Fatal("connection loss invalidated epoch before ACK registration completed")
	case <-time.After(50 * time.Millisecond):
	}
	close(registrationRelease)
	var staleWaiter chan simulator.Ack
	select {
	case staleWaiter = <-staleWaiterDone:
	case <-time.After(time.Second):
		t.Fatal("ACK registration did not complete")
	}
	waitClosed(t, lossDone, "connection loss did not complete after ACK registration")
	device.ackRegistrationHook = nil

	newClient := newLifecycleFakeClient()
	establishReady(t, device, newClient)
	replayDone := make(chan error, 1)
	go func() { replayDone <- device.replayPending(context.Background()) }()
	waitForCondition(t, func() bool { return newClient.countPublished("device/upload/data") == 1 }, "fresh replay did not publish after crossing registration")
	device.onMessage(newClient, testMQTTMessage{
		topic:   device.ackTopic(),
		payload: []byte(`{"boot_counter":34,"seq":340,"status":"stored"}`),
	})
	assertNoAck(t, staleWaiter, "pruned stale waiter")
	select {
	case err := <-replayDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh replay was blocked by cross-epoch registration")
	}
	if queue.Len() != 0 {
		t.Fatal("fresh terminal ACK did not complete queued telemetry")
	}
}

func TestReadyDoesNotStartBackgroundReplay(t *testing.T) {
	device := newLifecycleDevice(t)
	device.config.Mode = "offline-replay"
	queue, err := device.telemetryQueue()
	if err != nil {
		t.Fatal(err)
	}
	telemetry := simulator.Telemetry{MAC: simulator.DefaultMAC, BootCounter: 33, Sequence: 330, Timestamp: 1786021200}
	if err := queue.Enqueue(context.Background(), simulator.QueuedTelemetry{Telemetry: telemetry, Payload: []byte(`{"boot_counter":33,"seq":330}`)}); err != nil {
		t.Fatal(err)
	}
	client := newLifecycleFakeClient()
	gate := newPublishCallGate()
	client.setPublishCallGate("device/upload/data", gate)
	establishReady(t, device, client)

	select {
	case <-gate.started:
		t.Fatal("READY started replay in a background goroutine")
	case <-time.After(100 * time.Millisecond):
	}

	replayDone := make(chan error, 1)
	go func() { replayDone <- device.replayPending(context.Background()) }()
	waitClosed(t, gate.started, "explicit replay did not publish queued telemetry")
	close(gate.release)
	device.resolveAck(simulator.Ack{BootCounter: telemetry.BootCounter, Sequence: telemetry.Sequence, Status: "stored"})
	select {
	case err := <-replayDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("explicit replay did not complete after terminal ACK")
	}
}

func TestDuplicateRetryDoesNotAcceptDelayedFirstACK(t *testing.T) {
	device := newLifecycleDevice(t)
	client := newLifecycleFakeClient()
	establishReady(t, device, client)
	telemetry := simulator.Telemetry{MAC: simulator.DefaultMAC, BootCounter: 38, Sequence: 380, Timestamp: 1786021200}
	firstDone := make(chan error, 1)
	go func() { firstDone <- device.publishAndWait(context.Background(), telemetry) }()
	waitForCondition(t, func() bool { return client.countPublished("device/upload/data") == 1 }, "first duplicate-mode publish did not start")
	device.onMessage(client, testMQTTMessage{
		topic:   device.ackTopic(),
		payload: []byte(`{"boot_counter":38,"seq":380,"status":"stored"}`),
	})
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("first duplicate-mode publish did not complete")
	}

	gate := newPublishCallGate()
	client.setPublishAdmissionGate("device/upload/data", gate)
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- device.publishItemAndWait(context.Background(), simulator.QueuedTelemetry{
			Telemetry: telemetry,
			Payload:   []byte(`{"boot_counter":38,"seq":380}`),
		}, false)
	}()
	waitClosed(t, gate.started, "second duplicate-mode publish did not reach admission")
	// This is the delayed ACK from the first attempt. It must not satisfy the
	// second attempt before that attempt has passed token/epoch validation.
	device.onMessage(client, testMQTTMessage{
		topic:   device.ackTopic(),
		payload: []byte(`{"boot_counter":38,"seq":380,"status":"stored"}`),
	})
	close(gate.release)
	waitForCondition(t, func() bool { return client.countPublished("device/upload/data") == 2 }, "second duplicate-mode publish did not complete admission")
	device.onMessage(client, testMQTTMessage{
		topic:   device.ackTopic(),
		payload: []byte(`{"boot_counter":38,"seq":380,"status":"duplicate"}`),
	})
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("second duplicate-mode publish accepted no current terminal ACK")
	}
}

func TestTerminalACKDuringFailedPublishRemainsForRetry(t *testing.T) {
	device := newLifecycleDevice(t)
	queue, err := device.telemetryQueue()
	if err != nil {
		t.Fatal(err)
	}
	telemetry := simulator.Telemetry{MAC: simulator.DefaultMAC, BootCounter: 37, Sequence: 370, Timestamp: 1786021200}
	if err := queue.Enqueue(context.Background(), simulator.QueuedTelemetry{Telemetry: telemetry, Payload: []byte(`{"boot_counter":37,"seq":370}`)}); err != nil {
		t.Fatal(err)
	}
	client := newLifecycleFakeClient()
	gate := newGatedToken()
	client.setPublishToken("device/upload/data", gatedErrorToken{gatedToken: gate, err: errors.New("publish failed")})
	establishReady(t, device, client)

	firstAttempt := make(chan error, 1)
	go func() { firstAttempt <- device.replayPending(context.Background()) }()
	waitClosed(t, gate.started, "failed replay did not wait for publish token")
	device.onMessage(client, testMQTTMessage{
		topic:   device.ackTopic(),
		payload: []byte(`{"boot_counter":37,"seq":370,"status":"stored"}`),
	})
	close(gate.release)
	select {
	case err := <-firstAttempt:
		if err == nil {
			t.Fatal("failed publish unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("failed replay did not return")
	}
	if queue.Len() != 1 {
		t.Fatal("failed publish removed queue item despite terminal ACK race")
	}

	if err := device.replayPending(context.Background()); err != nil {
		t.Fatalf("retry did not consume deferred terminal ACK: %v", err)
	}
	if queue.Len() != 0 {
		t.Fatal("successful retry left deferred terminal ACK pending")
	}
}

func TestTerminalACKBeforeQuarantineReleaseCompletesFreshReplay(t *testing.T) {
	device := newLifecycleDevice(t)
	device.config.AckTimeout = 100 * time.Millisecond
	queue, err := device.telemetryQueue()
	if err != nil {
		t.Fatal(err)
	}
	telemetry := simulator.Telemetry{MAC: simulator.DefaultMAC, BootCounter: 36, Sequence: 360, Timestamp: 1786021200}
	if err := queue.Enqueue(context.Background(), simulator.QueuedTelemetry{Telemetry: telemetry, Payload: []byte(`{"boot_counter":36,"seq":360}`)}); err != nil {
		t.Fatal(err)
	}
	oldClient := newLifecycleFakeClient()
	establishReady(t, device, oldClient)
	device.onConnectionLost(oldClient, errors.New("quarantine before fresh replay"))
	newClient := newLifecycleFakeClient()
	establishReady(t, device, newClient)
	device.queuedACKReleaseHook = func(identity ackKey) {
		if identity != telemetry.Identity() {
			t.Fatalf("release hook identity=%+v, want %+v", identity, telemetry.Identity())
		}
		device.resolveAck(simulator.Ack{BootCounter: 36, Sequence: 360, Status: "stored"})
	}

	if err := device.replayPending(context.Background()); err != nil {
		t.Fatalf("fresh replay failed when ACK crossed quarantine release: %v", err)
	}
	if queue.Len() != 0 {
		t.Fatal("terminal ACK crossing quarantine release left queue item pending")
	}
}

func TestDelayedACKAfterTimeoutWhileReadyWaitsForFreshReplay(t *testing.T) {
	device := newLifecycleDevice(t)
	device.config.AckTimeout = 10 * time.Millisecond
	client := newLifecycleFakeClient()
	establishReady(t, device, client)
	telemetry := simulator.Telemetry{MAC: simulator.DefaultMAC, BootCounter: 39, Sequence: 390, Timestamp: 1786021200}
	if err := device.publishAndWait(context.Background(), telemetry); err == nil {
		t.Fatal("telemetry without ACK unexpectedly succeeded")
	}
	queue, err := device.telemetryQueue()
	if err != nil {
		t.Fatal(err)
	}
	if queue.Len() != 1 {
		t.Fatal("timeout removed queued telemetry")
	}
	device.onMessage(client, testMQTTMessage{
		topic:   device.ackTopic(),
		payload: []byte(`{"boot_counter":39,"seq":390,"status":"stored"}`),
	})
	if queue.Len() != 1 {
		t.Fatal("post-timeout terminal ACK removed item before retry authorization")
	}
	if err := device.replayPending(context.Background()); err != nil {
		t.Fatalf("fresh replay did not consume deferred terminal ACK: %v", err)
	}
	if queue.Len() != 0 {
		t.Fatal("authorized retry left deferred terminal ACK pending")
	}
}

func TestDelayedACKAfterTimeoutAndDisconnectWaitsForFreshReplay(t *testing.T) {
	device := newLifecycleDevice(t)
	device.config.AckTimeout = 10 * time.Millisecond
	oldClient := newLifecycleFakeClient()
	establishReady(t, device, oldClient)
	telemetry := simulator.Telemetry{MAC: simulator.DefaultMAC, BootCounter: 35, Sequence: 350, Timestamp: 1786021200}
	if err := device.publishAndWait(context.Background(), telemetry); err == nil {
		t.Fatal("telemetry without ACK unexpectedly succeeded")
	}
	queue, err := device.telemetryQueue()
	if err != nil {
		t.Fatal(err)
	}
	if queue.Len() != 1 {
		t.Fatal("timeout removed queued telemetry")
	}

	device.onConnectionLost(oldClient, errors.New("disconnect after timeout"))
	newClient := newLifecycleFakeClient()
	establishReady(t, device, newClient)
	ackPayload := []byte(`{"boot_counter":35,"seq":350,"status":"stored"}`)
	device.onMessage(newClient, testMQTTMessage{topic: device.ackTopic(), payload: ackPayload})
	if queue.Len() != 1 {
		t.Fatal("delayed terminal ACK removed item before fresh replay")
	}

	replayDone := make(chan error, 1)
	go func() { replayDone <- device.replayPending(context.Background()) }()
	waitForCondition(t, func() bool { return newClient.countPublished("device/upload/data") == 1 }, "fresh replay did not publish queued telemetry")
	select {
	case err := <-replayDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh replay did not complete after terminal ACK")
	}
	if queue.Len() != 0 {
		t.Fatal("fresh replay terminal ACK did not complete queue item")
	}
}

func TestDelayedACKAfterConnectionLossDoesNotRemoveQueuedTelemetry(t *testing.T) {
	device := newLifecycleDevice(t)
	client := newLifecycleFakeClient()
	establishReady(t, device, client)
	queue, err := device.telemetryQueue()
	if err != nil {
		t.Fatal(err)
	}
	telemetry := simulator.Telemetry{MAC: simulator.DefaultMAC, BootCounter: 1, Sequence: 81, Timestamp: 1786021200}
	result := make(chan error, 1)
	go func() { result <- device.publishAndWait(context.Background(), telemetry) }()
	waitForCondition(t, func() bool { return client.countPublished("device/upload/data") == 1 }, "telemetry was not published")
	device.onConnectionLost(client, errors.New("loss before delayed ACK"))
	select {
	case err := <-result:
		if !errors.Is(err, errMQTTSessionChanged) {
			t.Fatalf("telemetry publish returned %v, want session change", err)
		}
	case <-time.After(time.Second):
		t.Fatal("telemetry publish did not stop after connection loss")
	}
	device.resolveAck(simulator.Ack{BootCounter: 1, Sequence: 81, Status: "stored"})
	if queue.Len() != 1 {
		t.Fatalf("delayed stale ACK removed queued telemetry: length=%d", queue.Len())
	}
}

func TestShutdownIsTerminalWhileOnConnectIsPaused(t *testing.T) {
	device := newLifecycleDevice(t)
	device.mqttOperationTimeout = time.Second
	client := newLifecycleFakeClient()
	gate := newGatedToken()
	client.setSubscribeToken(device.ackTopic(), gate)
	installClient(t, device, client)

	restoreDone := make(chan struct{})
	go func() {
		device.onConnect(client)
		close(restoreDone)
	}()
	waitClosed(t, gate.started, "paused OnConnect did not reach subscription wait")
	assertReturns(t, device.shutdown, "shutdown blocked behind OnConnect token wait")

	device.onConnectionLost(client, errors.New("late loss after shutdown"))
	device.onConnect(client)
	close(gate.release)
	waitClosed(t, restoreDone, "paused OnConnect did not return after shutdown")

	if device.isReady() {
		t.Fatal("late callback resurrected READY after shutdown")
	}
	if got := client.countStatus(true); got != 0 {
		t.Fatalf("online status published after shutdown began: %d", got)
	}
	if got := client.countStatus(false); got != 1 {
		t.Fatalf("offline status count=%d, want 1", got)
	}
	if got := client.disconnectCount(); got != 1 {
		t.Fatalf("disconnect count=%d, want 1", got)
	}
	if !client.offlinePrecededDisconnect() {
		t.Fatal("offline status was not attempted before disconnect")
	}
}

func newLifecycleDevice(t *testing.T) *deviceSimulator {
	t.Helper()
	generator, err := simulator.NewGenerator(simulator.DefaultMAC, "test-fw", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	return &deviceSimulator{
		config:               config{AckTimeout: time.Second},
		generator:            generator,
		pending:              make(map[ackKey][]chan simulator.Ack),
		readyEvents:          make(chan struct{}, 4),
		mqttOperationTimeout: 100 * time.Millisecond,
	}
}

func installClient(t *testing.T, device *deviceSimulator, client mqtt.Client) {
	t.Helper()
	if err := device.setClient(client); err != nil {
		t.Fatal(err)
	}
}

func establishReady(t *testing.T, device *deviceSimulator, client *lifecycleFakeClient) {
	t.Helper()
	installClient(t, device, client)
	device.onConnect(client)
	if !device.isReady() {
		t.Fatal("test setup did not establish READY")
	}
}

func waitClosed(t *testing.T, done <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}

func assertReturns(t *testing.T, fn func(), message string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal(message)
	}
}

func waitForCondition(t *testing.T, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(message)
}

func simulatorIsStopping(device *deviceSimulator) bool {
	device.lifecycleMu.Lock()
	defer device.lifecycleMu.Unlock()
	return device.stopping
}

type lifecycleFakeClient struct {
	mqtt.Client
	mu                    sync.Mutex
	connected             bool
	subscribeTokens       map[string][]mqtt.Token
	publishTokens         map[string][]mqtt.Token
	publishAdmissionGates map[string][]*publishCallGate
	publishCallGates      map[string][]*publishCallGate
	subscribedTopics      []string
	published             []fakePublish
	actions               []string
	disconnects           int
}

type fakePublish struct {
	topic    string
	retained bool
	payload  interface{}
}

func newLifecycleFakeClient() *lifecycleFakeClient {
	return &lifecycleFakeClient{
		connected:             true,
		subscribeTokens:       make(map[string][]mqtt.Token),
		publishTokens:         make(map[string][]mqtt.Token),
		publishAdmissionGates: make(map[string][]*publishCallGate),
		publishCallGates:      make(map[string][]*publishCallGate),
	}
}

func (f *lifecycleFakeClient) setSubscribeToken(topic string, token mqtt.Token) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subscribeTokens[topic] = append(f.subscribeTokens[topic], token)
}

func (f *lifecycleFakeClient) setPublishToken(topic string, token mqtt.Token) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.publishTokens[topic] = append(f.publishTokens[topic], token)
}

func (f *lifecycleFakeClient) setPublishAdmissionGate(topic string, gate *publishCallGate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.publishAdmissionGates[topic] = append(f.publishAdmissionGates[topic], gate)
}

func (f *lifecycleFakeClient) setPublishCallGate(topic string, gate *publishCallGate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.publishCallGates[topic] = append(f.publishCallGates[topic], gate)
}

func (f *lifecycleFakeClient) Subscribe(topic string, qos byte, _ mqtt.MessageHandler) mqtt.Token {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subscribedTopics = append(f.subscribedTopics, topic)
	f.actions = append(f.actions, "subscribe:"+topic)
	if token := popToken(f.subscribeTokens, topic); token != nil {
		return token
	}
	return fakeSubscribeToken{result: map[string]byte{topic: qos}}
}

func (f *lifecycleFakeClient) Publish(topic string, _ byte, retained bool, payload interface{}) mqtt.Token {
	f.mu.Lock()
	admissionGate := popPublishCallGate(f.publishAdmissionGates, topic)
	f.mu.Unlock()
	if admissionGate != nil {
		admissionGate.markStarted()
		<-admissionGate.release
	}

	f.mu.Lock()
	f.published = append(f.published, fakePublish{topic: topic, retained: retained, payload: payload})
	if online, ok := statusOnline(payload); ok {
		if online {
			f.actions = append(f.actions, "status:online")
		} else {
			f.actions = append(f.actions, "status:offline")
		}
	} else {
		f.actions = append(f.actions, "publish:"+topic)
	}
	token := popToken(f.publishTokens, topic)
	if token == nil {
		token = immediateToken{}
	}
	callGate := popPublishCallGate(f.publishCallGates, topic)
	f.mu.Unlock()

	if callGate != nil {
		callGate.markStarted()
		<-callGate.release
	}
	return token
}

func popPublishCallGate(gates map[string][]*publishCallGate, topic string) *publishCallGate {
	queued := gates[topic]
	if len(queued) == 0 {
		return nil
	}
	gate := queued[0]
	if len(queued) == 1 {
		delete(gates, topic)
	} else {
		gates[topic] = queued[1:]
	}
	return gate
}

func popToken(tokens map[string][]mqtt.Token, topic string) mqtt.Token {
	queued := tokens[topic]
	if len(queued) == 0 {
		return nil
	}
	token := queued[0]
	if len(queued) == 1 {
		delete(tokens, topic)
	} else {
		tokens[topic] = queued[1:]
	}
	return token
}

func (f *lifecycleFakeClient) IsConnected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

func (f *lifecycleFakeClient) Disconnect(_ uint) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected = false
	f.disconnects++
	f.actions = append(f.actions, "disconnect")
}

func (f *lifecycleFakeClient) subscriptionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subscribedTopics)
}

func (f *lifecycleFakeClient) countPublished(topic string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, published := range f.published {
		if published.topic == topic {
			count++
		}
	}
	return count
}

func (f *lifecycleFakeClient) countStatus(online bool) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, published := range f.published {
		if value, ok := statusOnline(published.payload); published.topic == "device/"+simulator.DefaultMAC+"/status" && ok && value == online {
			count++
		}
	}
	return count
}

func (f *lifecycleFakeClient) statusWasRetained(online bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, published := range f.published {
		if value, ok := statusOnline(published.payload); ok && value == online {
			return published.retained
		}
	}
	return false
}

func (f *lifecycleFakeClient) statusPublications() []fakePublish {
	f.mu.Lock()
	defer f.mu.Unlock()
	statuses := make([]fakePublish, 0, len(f.published))
	for _, published := range f.published {
		if _, ok := statusOnline(published.payload); ok {
			statuses = append(statuses, published)
		}
	}
	return statuses
}

func (f *lifecycleFakeClient) disconnectCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.disconnects
}

func (f *lifecycleFakeClient) offlinePrecededDisconnect() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	offline := -1
	disconnect := -1
	for i, action := range f.actions {
		switch action {
		case "status:offline":
			offline = i
		case "disconnect":
			disconnect = i
		}
	}
	return offline >= 0 && disconnect > offline
}

func statusOnline(payload interface{}) (bool, bool) {
	var body []byte
	switch value := payload.(type) {
	case []byte:
		body = value
	case string:
		body = []byte(value)
	default:
		return false, false
	}
	var status struct {
		Online *bool `json:"online"`
	}
	if err := json.Unmarshal(body, &status); err != nil || status.Online == nil {
		return false, false
	}
	return *status.Online, true
}

type publishCallGate struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newPublishCallGate() *publishCallGate {
	return &publishCallGate{started: make(chan struct{}), release: make(chan struct{})}
}

func (g *publishCallGate) markStarted() {
	g.once.Do(func() { close(g.started) })
}

type immediateToken struct {
	err error
}

func (immediateToken) Wait() bool                     { return true }
func (immediateToken) WaitTimeout(time.Duration) bool { return true }
func (immediateToken) Done() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
func (token immediateToken) Error() error { return token.err }

type fakeSubscribeToken struct {
	immediateToken
	result map[string]byte
}

func (token fakeSubscribeToken) Result() map[string]byte { return token.result }

type gatedErrorToken struct {
	*gatedToken
	err error
}

func (t gatedErrorToken) Error() error { return t.err }

type gatedToken struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGatedToken() *gatedToken {
	return &gatedToken{started: make(chan struct{}), release: make(chan struct{})}
}

func (t *gatedToken) markStarted() {
	t.once.Do(func() { close(t.started) })
}

func (t *gatedToken) Wait() bool {
	t.markStarted()
	<-t.release
	return true
}

func (t *gatedToken) WaitTimeout(timeout time.Duration) bool {
	t.markStarted()
	select {
	case <-t.release:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (t *gatedToken) Done() <-chan struct{} { return t.release }
func (t *gatedToken) Error() error          { return nil }

type neverCompletingToken struct {
	started chan struct{}
	once    sync.Once
}

func newNeverCompletingToken() *neverCompletingToken {
	return &neverCompletingToken{started: make(chan struct{})}
}

func (t *neverCompletingToken) markStarted() {
	t.once.Do(func() { close(t.started) })
}

func (t *neverCompletingToken) Wait() bool {
	t.markStarted()
	select {}
}

func (t *neverCompletingToken) WaitTimeout(timeout time.Duration) bool {
	t.markStarted()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	<-timer.C
	return false
}

func (*neverCompletingToken) Done() <-chan struct{} { return make(chan struct{}) }
func (*neverCompletingToken) Error() error          { return nil }

func TestAckWaiterIdentityIsolation(t *testing.T) {
	device := &deviceSimulator{pending: make(map[ackKey][]chan simulator.Ack)}
	wanted := simulator.TelemetryIdentity{BootCounter: 7, Sequence: 11}
	waiter := device.registerAck(wanted)

	device.resolveAck(simulator.Ack{BootCounter: 8, Sequence: 11, Status: "stored"})
	assertNoAck(t, waiter, "different boot counter")
	device.resolveAck(simulator.Ack{BootCounter: 7, Sequence: 12, Status: "stored"})
	assertNoAck(t, waiter, "different sequence")
	device.resolveAck(simulator.Ack{BootCounter: 99, Sequence: 99, Status: "stored"})
	assertNoAck(t, waiter, "unknown identity")

	device.resolveAck(simulator.Ack{BootCounter: 7, Sequence: 11, Status: "stored"})
	select {
	case ack := <-waiter:
		if ack.BootCounter != wanted.BootCounter || ack.Sequence != wanted.Sequence {
			t.Fatalf("resolved wrong waiter identity: %+v", ack)
		}
	case <-time.After(time.Second):
		t.Fatal("matching ACK did not resolve waiter")
	}
}

type testMQTTMessage struct {
	mqtt.Message
	topic   string
	payload []byte
}

func (m testMQTTMessage) Topic() string   { return m.topic }
func (m testMQTTMessage) Payload() []byte { return m.payload }

func assertNoAck(t *testing.T, waiter <-chan simulator.Ack, reason string) {
	t.Helper()
	select {
	case ack := <-waiter:
		t.Fatalf("ACK for %s resolved waiter: %+v", reason, ack)
	default:
	}
}

func TestOfflineQueuePreservesConfiguredRecordedAt(t *testing.T) {
	recordedAt := time.Unix(1786021200, 0).UTC()
	generator, err := simulator.NewGenerator(simulator.DefaultMAC, "test-fw", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	device := &deviceSimulator{
		config:    config{Interval: 5 * time.Second, ReplayCount: 2, RecordedAt: &recordedAt},
		generator: generator,
	}
	device.prepareOfflineQueue()
	if len(device.offlineQueue) != 2 {
		t.Fatalf("unexpected queue length: %d", len(device.offlineQueue))
	}
	if device.offlineQueue[0].Timestamp != recordedAt.Unix() || device.offlineQueue[1].Timestamp != recordedAt.Add(5*time.Second).Unix() {
		t.Fatalf("offline queue changed timestamps: %+v", device.offlineQueue)
	}
}

func TestQueuedTelemetryWaitsForReadyThenReplaysExactPayload(t *testing.T) {
	device := newLifecycleDevice(t)
	queue, err := device.telemetryQueue()
	if err != nil {
		t.Fatal(err)
	}
	telemetry := simulator.Telemetry{MAC: simulator.DefaultMAC, BootCounter: 22, Sequence: 220, Timestamp: 1786021200}
	payload := []byte(`{"mac":"AABBCCDDEEFF","boot_counter":22,"seq":220,"ts":1786021200}`)
	if err := queue.Enqueue(context.Background(), simulator.QueuedTelemetry{Telemetry: telemetry, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	client := newLifecycleFakeClient()
	installClient(t, device, client)
	if err := device.publishQueuedAndWait(context.Background(), simulator.QueuedTelemetry{Telemetry: telemetry, Payload: payload}); err == nil {
		t.Fatal("queued telemetry published before readiness")
	}
	if got := client.countPublished("device/upload/data"); got != 0 {
		t.Fatalf("telemetry published before readiness: %d", got)
	}
	if queue.Len() != 1 {
		t.Fatalf("not-ready attempt removed queue item: %d", queue.Len())
	}

	establishReady(t, device, client)
	replayDone := make(chan error, 1)
	go func() { replayDone <- device.replayPending(context.Background()) }()
	waitForCondition(t, func() bool { return client.countPublished("device/upload/data") == 1 }, "queued replay was not published")
	device.resolveAck(simulator.Ack{BootCounter: telemetry.BootCounter, Sequence: telemetry.Sequence, Status: "stored"})
	select {
	case err := <-replayDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued replay did not complete after ACK")
	}
	if queue.Len() != 0 {
		t.Fatalf("terminal replay ACK left queue length=%d", queue.Len())
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	var got []byte
	for _, published := range client.published {
		if published.topic == "device/upload/data" {
			got, _ = published.payload.([]byte)
			break
		}
	}
	if string(got) != string(payload) {
		t.Fatalf("replay payload=%q, want %q", got, payload)
	}
}
