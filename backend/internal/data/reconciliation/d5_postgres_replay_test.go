//go:build securityintegration && d5referencepostgres

package reconciliation

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func d5ReplayEvent(t *testing.T, tuple D4OwnerTuple, kind D4EventKind) D4OwnerEvent {
	t.Helper()
	event := D4OwnerEvent{Kind: kind, Tuple: tuple, Approval: d4Approval(t, tuple, kind)}
	if kind == D4EventRecordResult || kind == D4EventTerminal || kind == D4EventResolveRecovery {
		result := d4TestResult(tuple)
		event.Result = &result
	}
	if kind == D4EventRequireRecovery || kind == D4EventResolveRecovery {
		event.Recovery = D4RecoveryUnknown
	}
	if kind == D4EventRequestContinuation || kind == D4EventConsumeContinuation {
		event.continuationProof = true
	}
	return event
}

func d5InsertReplayState(t *testing.T, db *sql.DB, tuple D4OwnerTuple, state D4State, claim uuid.UUID, withResult bool) {
	t.Helper()
	var result any
	var disposition, commitStatus, postStatus, cleanupStatus, certainty, replayDisposition, recoveryClass any
	var unknown, recoveryRequired any
	if withResult {
		r := d4TestResult(tuple)
		encoded, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		result = encoded
		disposition, commitStatus, postStatus, cleanupStatus, certainty = r.Disposition, r.CommitStatus, r.PostVerificationStatus, r.CleanupStatus, r.Certainty
		unknown, recoveryRequired = r.Unknown, r.RecoveryRequired
		recoveryClass, replayDisposition = r.RecoveryClass, r.ReplayDisposition
	}
	if _, err := db.Exec(`INSERT INTO d4_operation_ledger (operation_id,attempt_id,target_fingerprint,generation,state,claim_id,disposition,commit_status,post_verification_status,cleanup_status,certainty,unknown,recovery_required,recovery_class,replay_disposition,safe_result,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,COALESCE($12,false),COALESCE($13,false),COALESCE($14,''),$15,$16::jsonb,$17)`, tuple.OperationID(), tuple.AttemptID(), targetBytes(tuple), tuple.Generation(), state, nullableUUID(claim), disposition, commitStatus, postStatus, cleanupStatus, certainty, unknown, recoveryRequired, recoveryClass, replayDisposition, result, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func TestD5PostgresReplayAllLegalAndForbiddenEdges(t *testing.T) {
	db, _ := newD5TestDB(t)
	installD5TestSchema(t, db)
	store := NewPostgresD4Store(db)
	for from, transitions := range d4TransitionTable {
		for _, to := range transitions {
			kind := D4EventKind("")
			for candidate, candidateTo := range transitionsForState(from) {
				if candidateTo == to {
					kind = candidate
					break
				}
			}
			if kind == "" {
				t.Fatalf("no event for edge %s -> %s", from, to)
			}
			tuple := d4TestTuple(t)
			if from == D4Received && kind == D4EventAdmit {
				if _, err := store.Receive(context.Background(), D4ReceiveRequest{Tuple: tuple, ReceivedAt: time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
				claim, err := store.Claim(context.Background(), D4ClaimRequest{Tuple: tuple, Approval: d4Approval(t, tuple, kind), WorkerID: uuid.New()})
				if err != nil || claim.Record.State != to {
					t.Fatalf("claim edge %s -> %s record=%+v err=%v", from, to, claim.Record, err)
				}
				continue
			}
			claim := uuid.Nil
			if requiresD4Claim(from) {
				claim = uuid.New()
			}
			withResult := from == D4ResultRecorded || from == D4ContinuationPending || from == D4ContinuationConsumed || from == D4Executing || from == D4Admitted || from == D4WaitingForMapping || from == D4RecoveryRequired
			d5InsertReplayState(t, db, tuple, from, claim, withResult)
			event := d5ReplayEvent(t, tuple, kind)
			var revalidator D4OwnerRevalidator
			if kind == D4EventResumeMapping {
				revalidator = &d4Revalidator{}
			}
			record, err := store.Transition(context.Background(), D4TransitionRequest{Tuple: tuple, Expected: from, ClaimID: claim, Revalidator: revalidator, Event: event})
			if err != nil || record.State != to {
				t.Fatalf("legal edge %s -> %s record=%+v err=%v", from, to, record, err)
			}
		}
	}
	for _, from := range D4States() {
		for _, kind := range []D4EventKind{D4EventAdmit, D4EventBeginExecution, D4EventRecordResult, D4EventRequestContinuation, D4EventConsumeContinuation, D4EventWaitForMapping, D4EventResumeMapping, D4EventTerminal, D4EventRequireRecovery, D4EventResolveRecovery} {
			if _, ok := transitionsForState(from)[kind]; ok {
				continue
			}
			tuple := d4TestTuple(t)
			claim := uuid.Nil
			if requiresD4Claim(from) {
				claim = uuid.New()
			}
			d5InsertReplayState(t, db, tuple, from, claim, from != D4Received)
			_, err := store.Transition(context.Background(), D4TransitionRequest{Tuple: tuple, Expected: from, ClaimID: claim, Revalidator: &d4Revalidator{}, Event: d5ReplayEvent(t, tuple, kind)})
			if err == nil {
				t.Fatalf("forbidden edge accepted %s + %s", from, kind)
			}
		}
	}
}

func transitionsForState(state D4State) map[D4EventKind]D4State { return d4TransitionTable[state] }
