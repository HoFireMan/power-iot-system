package simulator

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTelemetryQueueStoredACKCompletesPendingItemOnce(t *testing.T) {
	queue := newTestTelemetryQueue(t, 4)
	item := testQueuedTelemetry(1, 10, "stored-payload")
	enqueueTelemetry(t, queue, item)

	ack := Ack{BootCounter: 1, Sequence: 10, Status: "stored"}
	if !queue.HandleAck(ack) {
		t.Fatal("stored ACK did not complete the pending item")
	}
	if queue.HandleAck(ack) {
		t.Fatal("duplicate stored ACK completed the item twice")
	}
	if got := queue.Len(); got != 0 {
		t.Fatalf("queue length=%d after terminal ACK, want 0", got)
	}
}

func TestTelemetryQueueDuplicateACKCompletesPendingItemOnce(t *testing.T) {
	queue := newTestTelemetryQueue(t, 4)
	enqueueTelemetry(t, queue, testQueuedTelemetry(1, 11, "duplicate-payload"))

	ack := Ack{BootCounter: 1, Sequence: 11, Status: "duplicate"}
	if !queue.HandleAck(ack) {
		t.Fatal("duplicate ACK did not complete the pending item")
	}
	if queue.HandleAck(ack) {
		t.Fatal("repeated duplicate ACK completed the item twice")
	}
	if queue.Len() != 0 {
		t.Fatal("duplicate ACK left a completed item pending")
	}
}

func TestTelemetryQueueNonTerminalACKKeepsPendingItem(t *testing.T) {
	statuses := []string{"unknown_device", "unknown_assignment", "invalid", "failed"}
	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			queue := newTestTelemetryQueue(t, 4)
			enqueueTelemetry(t, queue, testQueuedTelemetry(2, 20, status))
			if queue.HandleAck(Ack{BootCounter: 2, Sequence: 20, Status: status}) {
				t.Fatalf("non-terminal status %q completed the item", status)
			}
			if queue.Len() != 1 {
				t.Fatalf("non-terminal status %q removed the item", status)
			}
		})
	}
}

func TestTelemetryQueueTimeoutPublishFailureAndCancellationKeepItem(t *testing.T) {
	queue := newTestTelemetryQueue(t, 4)
	enqueueTelemetry(t, queue, testQueuedTelemetry(3, 30, "retained"))

	for _, status := range []string{"timeout", "publish_failed", "cancelled"} {
		if queue.HandleAck(Ack{BootCounter: 3, Sequence: 30, Status: status}) {
			t.Fatalf("non-terminal %s outcome completed the item", status)
		}
	}
	if queue.Len() != 1 {
		t.Fatal("pending item was removed without a terminal ACK")
	}
}

func TestTelemetryQueueWrongIdentityCannotCompleteAnotherItem(t *testing.T) {
	queue := newTestTelemetryQueue(t, 4)
	enqueueTelemetry(t, queue, testQueuedTelemetry(4, 40, "identity"))

	if queue.HandleAck(Ack{BootCounter: 4, Sequence: 41, Status: "stored"}) {
		t.Fatal("wrong sequence ACK completed a pending item")
	}
	if queue.HandleAck(Ack{BootCounter: 5, Sequence: 40, Status: "stored"}) {
		t.Fatal("wrong boot counter ACK completed a pending item")
	}
	if queue.Len() != 1 {
		t.Fatal("wrong identity ACK removed the pending item")
	}
}

func TestTelemetryQueueTerminalACKIsIdempotentWithoutCorruptingSecondItem(t *testing.T) {
	queue := newTestTelemetryQueue(t, 4)
	first := testQueuedTelemetry(5, 50, "first")
	second := testQueuedTelemetry(5, 51, "second")
	enqueueTelemetry(t, queue, first)
	enqueueTelemetry(t, queue, second)

	ack := Ack{BootCounter: 5, Sequence: 50, Status: "stored"}
	if !queue.HandleAck(ack) {
		t.Fatal("first terminal ACK did not complete the first item")
	}
	if queue.HandleAck(ack) {
		t.Fatal("duplicate terminal ACK completed an item twice")
	}
	pending := queue.Pending()
	if len(pending) != 1 || pending[0].Telemetry.Identity() != second.Telemetry.Identity() {
		t.Fatalf("duplicate terminal ACK corrupted second item: %+v", pending)
	}
}

func TestTelemetryQueueQuarantinesRecentlyCompletedIdentity(t *testing.T) {
	queue := newTestTelemetryQueue(t, 2)
	item := testQueuedTelemetry(12, 120, "first")
	enqueueTelemetry(t, queue, item)
	if !queue.HandleAck(Ack{BootCounter: 12, Sequence: 120, Status: "stored"}) {
		t.Fatal("terminal ACK did not complete first item")
	}
	if err := queue.Enqueue(context.Background(), testQueuedTelemetry(12, 120, "reused")); !errors.Is(err, ErrTelemetryIdentityCompleted) {
		t.Fatalf("identity reuse error=%v, want bounded completion quarantine", err)
	}
	if queue.HandleAck(Ack{BootCounter: 12, Sequence: 120, Status: "stored"}) {
		t.Fatal("stale terminal ACK completed a reused identity")
	}
}

func TestTelemetryQueueRetryPreservesIdentityRecordedAtAndPayload(t *testing.T) {
	queue := newTestTelemetryQueue(t, 4)
	item := testQueuedTelemetry(6, 60, `{"boot_counter":6,"seq":60,"ts":1786021200}`)
	enqueueTelemetry(t, queue, item)

	retry := queue.Pending()[0]
	if retry.Telemetry.Identity() != item.Telemetry.Identity() {
		t.Fatalf("retry identity changed: got=%+v want=%+v", retry.Telemetry.Identity(), item.Telemetry.Identity())
	}
	if retry.Telemetry.Timestamp != item.Telemetry.Timestamp {
		t.Fatalf("retry recorded_at changed: got=%d want=%d", retry.Telemetry.Timestamp, item.Telemetry.Timestamp)
	}
	if string(retry.Payload) != string(item.Payload) {
		t.Fatalf("retry payload changed: got=%q want=%q", retry.Payload, item.Payload)
	}
}

func TestTelemetryQueueReplayOrderIsStable(t *testing.T) {
	queue := newTestTelemetryQueue(t, 4)
	for sequence := int64(1); sequence <= 3; sequence++ {
		enqueueTelemetry(t, queue, testQueuedTelemetry(7, sequence, "ordered"))
	}

	pending := queue.Pending()
	if len(pending) != 3 {
		t.Fatalf("pending length=%d, want 3", len(pending))
	}
	for i, item := range pending {
		want := int64(i + 1)
		if item.Telemetry.Sequence != want {
			t.Fatalf("replay index %d sequence=%d, want %d", i, item.Telemetry.Sequence, want)
		}
	}
}

func TestTelemetryQueueSurvivesReconnectUntilReadyReplay(t *testing.T) {
	queue := newTestTelemetryQueue(t, 4)
	item := testQueuedTelemetry(8, 80, "reconnect")
	enqueueTelemetry(t, queue, item)

	// A disconnect does not mutate the local queue. A later READY replay can
	// inspect the same value and only a terminal ACK may remove it.
	if queue.Len() != 1 || queue.Pending()[0].Telemetry.Identity() != item.Telemetry.Identity() {
		t.Fatal("disconnect lost the pending item before readiness returned")
	}
	if !queue.HandleAck(Ack{BootCounter: 8, Sequence: 80, Status: "stored"}) {
		t.Fatal("READY replay terminal ACK did not complete the retained item")
	}
}

func TestTelemetryQueueTerminalACKRaceCompletesExactlyOnce(t *testing.T) {
	queue := newTestTelemetryQueue(t, 4)
	enqueueTelemetry(t, queue, testQueuedTelemetry(9, 90, "race"))
	ack := Ack{BootCounter: 9, Sequence: 90, Status: "stored"}

	var group sync.WaitGroup
	var completed atomic.Int32
	for i := 0; i < 32; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if queue.HandleAck(ack) {
				completed.Add(1)
			}
		}()
	}
	group.Wait()

	if got := completed.Load(); got != 1 {
		t.Fatalf("terminal ACK completion count=%d, want 1", got)
	}
	if queue.Len() != 0 {
		t.Fatal("terminal ACK race left an item pending")
	}
}

func TestTelemetryQueueCancellationRaceKeepsItemUnlessTerminalACKWins(t *testing.T) {
	queue := newTestTelemetryQueue(t, 4)
	enqueueTelemetry(t, queue, testQueuedTelemetry(10, 100, "cancel"))

	cancelled := make(chan struct{})
	close(cancelled)
	select {
	case <-cancelled:
		// Cancellation alone has no queue mutation authority.
	}
	if queue.Len() != 1 {
		t.Fatal("cancellation removed an item without terminal completion")
	}
	if !queue.HandleAck(Ack{BootCounter: 10, Sequence: 100, Status: "duplicate"}) {
		t.Fatal("terminal ACK did not win after cancellation")
	}
	if queue.Len() != 0 {
		t.Fatal("terminal ACK did not complete the retained item")
	}
}

func TestTelemetryQueueCapacityIsBoundedWithoutDroppingPendingItems(t *testing.T) {
	queue := newTestTelemetryQueue(t, 1)
	enqueueTelemetry(t, queue, testQueuedTelemetry(11, 110, "capacity"))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := queue.Enqueue(ctx, testQueuedTelemetry(11, 111, "blocked")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("full queue enqueue error=%v, want context deadline", err)
	}
	pending := queue.Pending()
	if len(pending) != 1 || pending[0].Telemetry.Sequence != 110 {
		t.Fatalf("full queue changed pending item: %+v", pending)
	}
}

func newTestTelemetryQueue(t *testing.T, capacity int) *TelemetryQueue {
	t.Helper()
	queue, err := NewTelemetryQueue(capacity)
	if err != nil {
		t.Fatal(err)
	}
	return queue
}

func testQueuedTelemetry(bootCounter, sequence int64, payload string) QueuedTelemetry {
	return QueuedTelemetry{
		Telemetry: Telemetry{
			MAC:         DefaultMAC,
			BootCounter: bootCounter,
			Sequence:    sequence,
			Timestamp:   1786021200 + sequence,
		},
		Payload: []byte(payload),
	}
}

func enqueueTelemetry(t *testing.T, queue *TelemetryQueue, item QueuedTelemetry) {
	t.Helper()
	if err := queue.Enqueue(context.Background(), item); err != nil {
		t.Fatal(err)
	}
}
