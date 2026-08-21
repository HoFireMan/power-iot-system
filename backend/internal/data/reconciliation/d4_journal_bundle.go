package reconciliation

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	D4BundleVersion   uint32 = 1
	D4BundleVersionV2 uint32 = 2
)

// D4SafeCorrelation contains only non-authorizing evidence correlation. It is
// the compatible extension used when D5 requires the complete D018 seam.
type D4SafeCorrelation struct {
	FactsDigest           string `json:"facts_digest"`
	ProofDigest           string `json:"proof_digest"`
	PredicateIdentity     string `json:"predicate_identity"`
	PredicateVersion      string `json:"predicate_version"`
	ProvenanceDigest      string `json:"provenance_digest"`
	PostCommitFactsDigest string `json:"post_commit_facts_digest,omitempty"`
	PostCommitFactsAsOf   string `json:"post_commit_facts_as_of,omitempty"`
}

func (c D4SafeCorrelation) Validate() error {
	for name, value := range map[string]string{"facts_digest": c.FactsDigest, "proof_digest": c.ProofDigest, "provenance_digest": c.ProvenanceDigest} {
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != 32 {
			return fmt.Errorf("safe correlation %s must be a 32-byte hex digest", name)
		}
	}
	if c.PredicateIdentity == "" || c.PredicateVersion == "" {
		return errors.New("safe correlation predicate identity and version are required")
	}
	if c.PostCommitFactsDigest != "" {
		decoded, err := hex.DecodeString(c.PostCommitFactsDigest)
		if err != nil || len(decoded) != 32 {
			return errors.New("safe correlation post-commit facts digest is invalid")
		}
	}
	return nil
}

// D4JournalEvent is an allow-listed semantic event. It contains no owner
// bearer, source proof, physical handle, transaction, session, D007, or D010.
type D4JournalEvent struct {
	EventID     uuid.UUID       `json:"event_id"`
	Version     uint64          `json:"version"`
	Tuple       D4OwnerTuple    `json:"tuple"`
	From        D4State         `json:"from"`
	To          D4State         `json:"to"`
	Result      *D4SafeResult   `json:"result,omitempty"`
	Recovery    D4RecoveryClass `json:"recovery_class,omitempty"`
	Correlation string          `json:"correlation,omitempty"`
	OccurredAt  time.Time       `json:"occurred_at"`
}

func legalD4Edge(from, to D4State) bool {
	if from == to {
		return from == D4Terminal
	}
	for _, next := range d4TransitionTable[from] {
		if next == to {
			return true
		}
	}
	return false
}

func (e D4JournalEvent) Validate() error {
	if e.EventID == uuid.Nil || e.Version == 0 || !e.Tuple.Valid() || !validD4State(e.From) || !validD4State(e.To) || e.OccurredAt.IsZero() {
		return errors.New("D4 journal event has incomplete safe identity")
	}
	if !legalD4Edge(e.From, e.To) {
		return fmt.Errorf("journal event records an illegal D4 edge %s -> %s", e.From, e.To)
	}
	if e.Result != nil {
		if err := e.Result.ValidateFor(e.Tuple); err != nil {
			return err
		}
	}
	if !validD4RecoveryClass(e.Recovery) {
		return errors.New("journal recovery class is invalid")
	}
	if e.From == D4Terminal || (e.To == D4Terminal && e.Result == nil) {
		return errors.New("terminal journal event requires a safe result")
	}
	return nil
}

// D4Journal is observational only. Replay must not invoke an owner seam.
type D4Journal interface {
	Append(context.Context, D4JournalEvent) error
	Replay(context.Context, func(D4JournalEvent) error) error
}

type InMemoryD4Journal struct {
	mu     sync.Mutex
	events map[uuid.UUID]D4JournalEvent
	order  []uuid.UUID
}

func NewInMemoryD4Journal() *InMemoryD4Journal {
	return &InMemoryD4Journal{events: make(map[uuid.UUID]D4JournalEvent)}
}

func (j *InMemoryD4Journal) Append(ctx context.Context, event D4JournalEvent) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := event.Validate(); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.events == nil {
		j.events = make(map[uuid.UUID]D4JournalEvent)
	}
	if existing, ok := j.events[event.EventID]; ok {
		existingJSON, existingErr := json.Marshal(existing)
		eventJSON, eventErr := json.Marshal(event)
		if existingErr != nil || eventErr != nil || !bytes.Equal(existingJSON, eventJSON) {
			return errors.New("D4 journal event identity conflict")
		}
		return nil
	}
	copyEvent := event
	if event.Result != nil {
		copyResult := *event.Result
		copyEvent.Result = &copyResult
	}
	j.events[event.EventID] = copyEvent
	j.order = append(j.order, event.EventID)
	return nil
}

func (j *InMemoryD4Journal) Replay(ctx context.Context, apply func(D4JournalEvent) error) error {
	if j == nil || apply == nil {
		return errors.New("D4 journal replay requires a journal and observer")
	}
	j.mu.Lock()
	ids := append([]uuid.UUID(nil), j.order...)
	events := make([]D4JournalEvent, 0, len(ids))
	for _, id := range ids {
		event := j.events[id]
		if event.Result != nil {
			copyResult := *event.Result
			event.Result = &copyResult
		}
		events = append(events, event)
	}
	j.mu.Unlock()
	for _, event := range events {
		if err := contextErr(ctx); err != nil {
			return err
		}
		if err := apply(event); err != nil {
			return err
		}
	}
	return nil
}

// SafeProjectionBytes is the serialization boundary for semantic output. The
// scanner is defense in depth for hand-built bytes; typed projections already
// exclude forbidden authority material by construction.
func SafeProjectionBytes(value interface{}) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if err := ValidateSafeProjectionBytes(encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

var d4SafeFieldAllowList = map[string]struct{}{
	"event_id": {}, "version": {}, "tuple": {}, "from": {}, "to": {}, "result": {}, "recovery_class": {}, "correlation": {}, "occurred_at": {},
	"operation_id": {}, "attempt_id": {}, "target_fingerprint_digest": {}, "generation": {}, "disposition": {}, "commit_status": {}, "post_verification_status": {}, "cleanup_status": {}, "certainty": {}, "unknown": {}, "recovery_required": {}, "replay_disposition": {},
	"bundle_version": {}, "state": {}, "d018_seams": {}, "created_at": {}, "seam": {}, "required_binding": {}, "owner": {}, "evidence_in_pr1": {}, "future_d5_proof": {}, "implementation": {}, "facts_digest": {}, "proof_digest": {}, "predicate_identity": {}, "predicate_version": {}, "provenance_digest": {}, "post_commit_facts_digest": {}, "post_commit_facts_as_of": {},
}

func ValidateSafeProjectionBytes(encoded []byte) error {
	lower := strings.ToLower(string(encoded))
	for _, forbidden := range []string{"d007", "d010", "bearer", "session", "transaction", "connection", "dsn", "secret", "credential", "password", "advisory_lock", "fence", "000006", "create table", "alter table"} {
		if strings.Contains(lower, forbidden) {
			return fmt.Errorf("forbidden authority material %q in safe projection", forbidden)
		}
	}
	var value interface{}
	if err := json.Unmarshal(encoded, &value); err != nil {
		return fmt.Errorf("safe projection is not JSON: %w", err)
	}
	var walk func(interface{}) error
	walk = func(node interface{}) error {
		switch value := node.(type) {
		case map[string]interface{}:
			for key, child := range value {
				if _, ok := d4SafeFieldAllowList[key]; !ok {
					return fmt.Errorf("field %q is not in the D4 safe allow-list", key)
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		case []interface{}:
			for _, child := range value {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value)
}

// D4ToD5Bundle is a versioned semantic handoff, not a physical persistence
// contract. D5 chooses its own schema, SQL, transaction layout, and migration.
type D4ToD5Bundle struct {
	Version     uint32                   `json:"bundle_version"`
	Tuple       D4OwnerTuple             `json:"tuple"`
	State       D4State                  `json:"state"`
	Result      *D4SafeResult            `json:"result,omitempty"`
	Correlation *D4SafeCorrelation       `json:"correlation,omitempty"`
	Recovery    D4RecoveryClass          `json:"recovery_class,omitempty"`
	D018        []D018SeamInventoryEntry `json:"d018_seams"`
	CreatedAt   time.Time                `json:"created_at"`
}

func NewD4ToD5Bundle(record D4Record) (D4ToD5Bundle, error) {
	bundle := D4ToD5Bundle{Version: D4BundleVersion, Tuple: record.Tuple, State: record.State, Recovery: record.Recovery, D018: D018SeamInventory(), CreatedAt: time.Now().UTC()}
	if record.Result != nil {
		result := *record.Result
		bundle.Result = &result
	}
	if err := bundle.Validate(); err != nil {
		return D4ToD5Bundle{}, err
	}
	return bundle, nil
}

// NewD4ToD5BundleV2 creates the complete safe bundle required by D5.
func NewD4ToD5BundleV2(record D4Record, correlation D4SafeCorrelation) (D4ToD5Bundle, error) {
	bundle := D4ToD5Bundle{Version: D4BundleVersionV2, Tuple: record.Tuple, State: record.State, Correlation: &correlation, Recovery: record.Recovery, D018: D018SeamInventory(), CreatedAt: time.Now().UTC()}
	if record.Result != nil {
		result := *record.Result
		bundle.Result = &result
	}
	if err := bundle.ValidateForD5(); err != nil {
		return D4ToD5Bundle{}, err
	}
	return bundle, nil
}

func (b D4ToD5Bundle) Validate() error {
	if (b.Version != D4BundleVersion && b.Version != D4BundleVersionV2) || !b.Tuple.Valid() || !validD4State(b.State) || !validD4RecoveryClass(b.Recovery) || b.CreatedAt.IsZero() || len(b.D018) == 0 {
		return errors.New("D4-to-D5 bundle is incomplete or unsupported")
	}
	if err := ValidateD018SeamInventory(b.D018); err != nil {
		return err
	}
	if b.Result != nil {
		if err := b.Result.ValidateFor(b.Tuple); err != nil {
			return err
		}
	}
	if b.Version == D4BundleVersionV2 {
		if b.Correlation == nil {
			return errors.New("D4-to-D5 V2 bundle correlation is required")
		}
		if err := b.Correlation.Validate(); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return ValidateSafeProjectionBytes(encoded)
}

func (b D4ToD5Bundle) ValidateForD5() error {
	if err := b.Validate(); err != nil {
		return err
	}
	if b.Version != D4BundleVersionV2 || b.Correlation == nil {
		return errors.New("D5 requires a complete versioned safe bundle")
	}
	return nil
}

func (b D4ToD5Bundle) MarshalSafe() ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return SafeProjectionBytes(b)
}

func (b D4ToD5Bundle) MarshalSafeForD5() ([]byte, error) {
	if err := b.ValidateForD5(); err != nil {
		return nil, err
	}
	return SafeProjectionBytes(b)
}
