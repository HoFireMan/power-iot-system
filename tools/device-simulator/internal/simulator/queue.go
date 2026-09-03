package simulator

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	// ErrInvalidTelemetryQueueCapacity reports a queue that cannot provide a
	// bounded in-memory buffer.
	ErrInvalidTelemetryQueueCapacity = errors.New("telemetry queue capacity must be positive")
	// ErrTelemetryIdentityPending prevents two independently encoded values with
	// the same protocol identity from being pending at the same time. Without
	// this rule, an ACK could not identify which local value it completed.
	ErrTelemetryIdentityPending = errors.New("telemetry identity is already pending")
	// ErrTelemetryIdentityCompleted is a bounded local quarantine for recently
	// completed identities. The wire ACK has no attempt/generation field, so
	// immediate identity reuse would let a delayed ACK complete a new item.
	ErrTelemetryIdentityCompleted = errors.New("telemetry identity was recently completed")
)

// QueuedTelemetry is the immutable local replay unit. Payload is retained in
// its encoded form so replay never regenerates a sample or changes its ts.
type QueuedTelemetry struct {
	Telemetry Telemetry
	Payload   []byte
}

// Identity returns the protocol identity of the queued value.
func (q QueuedTelemetry) Identity() TelemetryIdentity {
	return q.Telemetry.Identity()
}

// TelemetryQueue is a bounded, FIFO, ACK-driven in-memory queue. Items leave
// only after a matching terminal stored/duplicate/lifecycle_blocked ACK. A timeout, publish
// error, cancellation, disconnect, or non-terminal ACK does not mutate it.
type TelemetryQueue struct {
	mu             sync.Mutex
	capacity       int
	items          []QueuedTelemetry
	byID           map[TelemetryIdentity]struct{}
	completed      map[TelemetryIdentity]struct{}
	completedOrder []TelemetryIdentity
	changed        chan struct{}
}

// NewTelemetryQueue creates a bounded queue. Capacity is deliberately explicit
// because simulator memory is local and non-durable; callers must choose how
// much data they are willing to retain.
func NewTelemetryQueue(capacity int) (*TelemetryQueue, error) {
	if capacity <= 0 {
		return nil, fmt.Errorf("%w: %d", ErrInvalidTelemetryQueueCapacity, capacity)
	}
	return &TelemetryQueue{
		capacity:       capacity,
		items:          make([]QueuedTelemetry, 0, capacity),
		byID:           make(map[TelemetryIdentity]struct{}, capacity),
		completed:      make(map[TelemetryIdentity]struct{}, capacity),
		completedOrder: make([]TelemetryIdentity, 0, capacity),
		changed:        make(chan struct{}),
	}, nil
}

// Enqueue waits for capacity without dropping an existing pending item. The
// caller's payload is copied so queued replay remains stable if the caller
// reuses its byte buffer.
func (q *TelemetryQueue) Enqueue(ctx context.Context, item QueuedTelemetry) error {
	if q == nil {
		return errors.New("telemetry queue is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		q.mu.Lock()
		identity := item.Identity()
		if _, exists := q.byID[identity]; exists {
			q.mu.Unlock()
			return ErrTelemetryIdentityPending
		}
		if _, completed := q.completed[identity]; completed {
			q.mu.Unlock()
			return ErrTelemetryIdentityCompleted
		}
		if len(q.items) < q.capacity {
			item.Payload = append([]byte(nil), item.Payload...)
			q.items = append(q.items, item)
			q.byID[identity] = struct{}{}
			q.mu.Unlock()
			return nil
		}
		changed := q.changed
		q.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

// HandleAck removes exactly one pending item for a matching terminal ACK. It
// returns true only for the one call that performed that removal.
func (q *TelemetryQueue) HandleAck(ack Ack) bool {
	if q == nil || !ack.IsTerminal() {
		return false
	}
	identity := ack.Identity()
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.byID[identity]; !ok {
		return false
	}
	for i := range q.items {
		if q.items[i].Identity() != identity {
			continue
		}
		q.items = append(q.items[:i], q.items[i+1:]...)
		delete(q.byID, identity)
		q.rememberCompletedLocked(identity)
		close(q.changed)
		q.changed = make(chan struct{})
		return true
	}
	// byID and items are maintained together. Keep the method conservative if
	// a future change violates that invariant rather than removing another ID.
	return false
}

func (q *TelemetryQueue) rememberCompletedLocked(identity TelemetryIdentity) {
	if _, exists := q.completed[identity]; exists {
		return
	}
	q.completed[identity] = struct{}{}
	q.completedOrder = append(q.completedOrder, identity)
	if len(q.completedOrder) > q.capacity {
		oldest := q.completedOrder[0]
		q.completedOrder = q.completedOrder[1:]
		delete(q.completed, oldest)
	}
}

// Len returns the number of pending, not terminally acknowledged items.
func (q *TelemetryQueue) Len() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// Pending returns a stable FIFO snapshot. Both the slice and payload bytes are
// copied, preventing a stale replay snapshot from mutating queue contents.
func (q *TelemetryQueue) Pending() []QueuedTelemetry {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	pending := make([]QueuedTelemetry, len(q.items))
	for i, item := range q.items {
		pending[i] = item
		pending[i].Payload = append([]byte(nil), item.Payload...)
	}
	return pending
}

// Item returns an exact queued value for identity, if it is still pending.
func (q *TelemetryQueue) Item(identity TelemetryIdentity) (QueuedTelemetry, bool) {
	if q == nil {
		return QueuedTelemetry{}, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, item := range q.items {
		if item.Identity() == identity {
			item.Payload = append([]byte(nil), item.Payload...)
			return item, true
		}
	}
	return QueuedTelemetry{}, false
}

// IsPending checks identity against the live queue, not an earlier replay
// snapshot. Replay callers must perform this check immediately before sending.
func (q *TelemetryQueue) IsPending(identity TelemetryIdentity) bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.byID[identity]
	return ok
}

// Capacity reports the configured bound.
func (q *TelemetryQueue) Capacity() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.capacity
}
