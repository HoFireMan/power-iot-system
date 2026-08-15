package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"power-iot-a3-deployment-control-plane/internal/ledger"
)

// Consume performs the CR3 three-step protocol: durable PENDING intent, a
// separate CLAIM transaction, then atomic parent/child finalization.
func (s *Store) Consume(ctx context.Context, req ConsumeRequest) (ConsumeResult, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	begin, err := s.beginConsume(ctx, req)
	if err != nil {
		return normalizeConsumeFailure(req, begin, err)
	}
	// A durable terminal/pending-parent result is already complete for this
	// request. Never enter CLAIM again: doing so would turn a loser/replay
	// into an implicit consume retry.
	if begin.PendingConsumeRequestID != "" || (begin.Detail != "" && begin.State != ledger.Pending) {
		return begin, nil
	}
	claim, err := s.claimConsume(ctx, req, begin)
	if err != nil {
		return normalizeConsumeFailure(req, claim, err)
	}
	if claim.State != ledger.Claimed {
		return claim, nil
	}
	out, err := s.finalizeConsume(ctx, mustUUID(req.AuthorizationID), mustUUID(req.ConsumeRequestID))
	if err != nil {
		return normalizeConsumeFailure(req, out, err)
	}
	return out, nil
}

func normalizeConsumeFailure(req ConsumeRequest, out ConsumeResult, err error) (ConsumeResult, error) {
	if err == nil || errors.Is(err, ledger.ErrUnknownCommit) || expectedConsumeRejection(err) {
		return out, err
	}
	if out.AuthorizationID == "" {
		out.AuthorizationID = req.AuthorizationID
	}
	if out.ConsumeRequestID == "" {
		out.ConsumeRequestID = req.ConsumeRequestID
	}
	out.State = ledger.Unknown
	return out, fmt.Errorf("%w: %v", ledger.ErrUnknownCommit, err)
}

func expectedConsumeRejection(err error) bool {
	message := err.Error()
	for _, prefix := range []string{
		"invalid ", "consume binding", "consume request binding", "authorization not found",
		"consume request not found", "authorization is not issuable", "authorization expired",
		"authorization transition forbidden", "consume intent", "intent transition forbidden",
		"attempt already belongs", "issuer request", "authorization changed",
	} {
		if strings.HasPrefix(message, prefix) {
			return true
		}
	}
	return false
}

func mustUUID(s string) uuid.UUID { v, _ := uuid.Parse(s); return v }

func (s *Store) beginConsume(ctx context.Context, req ConsumeRequest) (ConsumeResult, error) {
	if err := s.requireAuthority(ctx); err != nil {
		return ConsumeResult{}, err
	}
	cid, err := parseID(req.ConsumeRequestID, "consume_request_id")
	if err != nil {
		return ConsumeResult{}, err
	}
	aid, err := parseID(req.AuthorizationID, "authorization_id")
	if err != nil {
		return ConsumeResult{}, err
	}
	rid, err := parseID(req.IssuerRequestID, "issuer_request_id")
	if err != nil {
		return ConsumeResult{}, err
	}
	env, err := parseEnvelope(req.Envelope)
	if err != nil {
		return ConsumeResult{}, err
	}
	nonce, err := decodeNonce(req.Nonce)
	if err != nil {
		return ConsumeResult{}, err
	}
	if env.authID != aid || env.epoch != req.Epoch || !equal(env.nonce, nonce) || req.Scope != ledger.ScopeControlCatalogInstall || strings.TrimSpace(req.Operation) == "" || strings.TrimSpace(req.TargetID) == "" || strings.TrimSpace(req.InstallerID) == "" || strings.TrimSpace(req.EvidenceHash) == "" {
		return ConsumeResult{}, errors.New("consume binding rejected")
	}
	attempt, err := parseID(req.AttemptID, "attempt_id")
	if err != nil {
		return ConsumeResult{}, err
	}
	h := sha256Bytes(env.secret)
	tx, err := s.beginAuthorityTx(ctx)
	if err != nil {
		return ConsumeResult{}, err
	}
	defer tx.Rollback()
	// Parent is always locked before the child, including idempotency reads.
	var locked uuid.UUID
	var lockedState string
	var lockedTerminalCode sql.NullString
	var lockedConsumeID uuid.NullUUID
	if err = tx.QueryRowContext(ctx, `SELECT authorization_id,state,terminal_code,consume_request_id FROM d1l_provider.d1l_bootstrap_authorizations WHERE authorization_id=$1 AND issuer_request_id=$2 FOR UPDATE`, aid, rid).Scan(&locked, &lockedState, &lockedTerminalCode, &lockedConsumeID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ConsumeResult{}, errors.New("authorization not found")
		}
		return ConsumeResult{}, err
	}
	var ea, er, eat uuid.UUID
	var ep int64
	var en []byte
	var eo, et, ei, ee, es, est, childConsumerIdentity string
	var childTerminalCode sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT authorization_id,issuer_request_id,epoch_id,nonce,operation,attempt_id,target_id,installer_id,evidence_hash,scope,consumer_identity,state,terminal_code FROM d1l_provider.d1l_bootstrap_consume_intents WHERE consume_request_id=$1 FOR UPDATE`, cid).Scan(&ea, &er, &ep, &en, &eo, &eat, &et, &ei, &ee, &es, &childConsumerIdentity, &est, &childTerminalCode)
	if err == nil {
		consumerIdentity := strings.TrimSpace(req.ConsumerIdentity)
		if consumerIdentity == "" {
			consumerIdentity = "store-direct"
		}
		if ea != aid || er != rid || ep != req.Epoch || !equal(en, env.nonce) || eo != req.Operation || eat.String() != req.AttemptID || et != req.TargetID || ei != req.InstallerID || ee != req.EvidenceHash || es != req.Scope || childConsumerIdentity != consumerIdentity {
			return ConsumeResult{}, errors.New("consume request binding mismatch")
		}
		if est == "PENDING" && lockedState != "ISSUED" {
			if lockedState == "CONSUME_PENDING" {
				if err = tx.Commit(); err != nil {
					return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Unknown}, ledger.ErrUnknownCommit
				}
				return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Claimed, AuthorizationState: lockedState, Detail: "IN_PROGRESS", PendingConsumeRequestID: cid.String()}, nil
			}
			return ConsumeResult{}, errors.New("intent transition forbidden")
		}
		if est == "CLAIMED" && lockedState != "CONSUME_PENDING" && lockedState != "CONSUMED" && lockedState != "CONSUME_UNKNOWN" {
			return ConsumeResult{}, errors.New("intent transition forbidden")
		}
		out := ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.IntentState(est), AuthorizationState: lockedState, Detail: "durable outcome"}
		if lockedState != "ISSUED" && lockedState != "CONSUME_PENDING" {
			out.TerminalState = lockedState
			if lockedTerminalCode.Valid {
				out.TerminalCode = lockedTerminalCode.String
			} else if childTerminalCode.Valid {
				out.TerminalCode = childTerminalCode.String
			}
		}
		if err = tx.Commit(); err != nil {
			return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Unknown}, ledger.ErrUnknownCommit
		}
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ConsumeResult{}, err
	}
	var ast string
	var aepoch int64
	var anonce, verifier []byte
	var scope, op, target, installer, evidence string
	var aattempt uuid.UUID
	var expiry time.Time
	if err = tx.QueryRowContext(ctx, `SELECT state,epoch_id,nonce,secret_verifier,scope,operation,attempt_id,target_id,installer_id,evidence_hash,expires_at FROM d1l_provider.d1l_bootstrap_authorizations WHERE authorization_id=$1 AND issuer_request_id=$2 FOR UPDATE`, aid, rid).Scan(&ast, &aepoch, &anonce, &verifier, &scope, &op, &aattempt, &target, &installer, &evidence, &expiry); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ConsumeResult{}, errors.New("authorization not found")
		}
		return ConsumeResult{}, err
	}
	if aepoch != req.Epoch || !equal(anonce, env.nonce) || scope != req.Scope || op != req.Operation || aattempt != attempt || target != req.TargetID || installer != req.InstallerID || evidence != req.EvidenceHash || !equal(verifier, h) {
		return ConsumeResult{}, errors.New("consume binding rejected")
	}
	var now time.Time
	if err = tx.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return ConsumeResult{}, err
	}
	if ast != "ISSUED" {
		// A fresh request against an already-linearized parent is a durable
		// outcome, not a binding failure. Keep the caller's request ID stable;
		// the pending owner ID is carried separately for owner Resolve.
		out := ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), AuthorizationState: ast}
		if ast == "CONSUME_PENDING" {
			out.TerminalState = ""
			out.TerminalCode = ""
		} else {
			out.TerminalState = ast
			if lockedTerminalCode.Valid {
				out.TerminalCode = lockedTerminalCode.String
			}
		}
		switch ast {
		case "CONSUME_PENDING":
			out.State = ledger.Claimed
			out.Detail = "IN_PROGRESS"
			if lockedConsumeID.Valid {
				out.PendingConsumeRequestID = lockedConsumeID.UUID.String()
			}
		case "CONSUMED":
			out.State = ledger.IntentConsumed
			out.Detail = "durable outcome"
		case "CONSUME_UNKNOWN":
			out.State = ledger.Unknown
			out.Detail = "outcome unknown"
		default:
			out.State = ledger.Aborted
			out.Detail = "terminal outcome"
		}
		if err = tx.Commit(); err != nil {
			return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Unknown}, ledger.ErrUnknownCommit
		}
		return out, nil
	}
	if !expiry.After(now) {
		var liveID uuid.UUID
		var liveState string
		liveErr := tx.QueryRowContext(ctx, `SELECT consume_request_id,state FROM d1l_provider.d1l_bootstrap_consume_intents WHERE authorization_id=$1 AND issuer_request_id=$2 AND state IN ('PENDING','CLAIMED') ORDER BY created_at LIMIT 1 FOR UPDATE`, aid, rid).Scan(&liveID, &liveState)
		if liveErr != nil && !errors.Is(liveErr, sql.ErrNoRows) {
			return ConsumeResult{}, liveErr
		}
		if liveErr == nil && liveState == "CLAIMED" {
			return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Claimed, AuthorizationState: "CONSUME_PENDING", Detail: "IN_PROGRESS", PendingConsumeRequestID: liveID.String()}, nil
		}
		result, e := tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_authorizations SET state='EXPIRED',revoked_reason='EXPIRED',terminal_at=clock_timestamp(),terminal_code='EXPIRED',terminal_consumer='provider-consume',updated_at=clock_timestamp() WHERE authorization_id=$1 AND issuer_request_id=$2 AND epoch_id=$3 AND nonce=$4 AND operation=$5 AND attempt_id=$6 AND target_id=$7 AND installer_id=$8 AND evidence_hash=$9 AND scope=$10 AND state='ISSUED' AND consume_request_id IS NULL`, aid, rid, aepoch, anonce, op, aattempt, target, installer, evidence, scope)
		if e != nil {
			return ConsumeResult{}, e
		}
		if n, e := result.RowsAffected(); e != nil || n != 1 {
			return ConsumeResult{}, errors.New("authorization transition forbidden")
		}
		if liveErr == nil {
			result, e = tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_consume_intents SET state='ABORTED',terminal_at=clock_timestamp(),terminal_code='EXPIRED',terminal_consumer='provider-consume',updated_at=clock_timestamp() WHERE consume_request_id=$1 AND authorization_id=$2 AND issuer_request_id=$3 AND state='PENDING'`, liveID, aid, rid)
			if e != nil {
				return ConsumeResult{}, e
			}
			if n, e := result.RowsAffected(); e != nil || n != 1 {
				return ConsumeResult{}, errors.New("consume intent changed")
			}
		}
		if err = tx.Commit(); err != nil {
			return ConsumeResult{}, err
		}
		return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Aborted, AuthorizationState: "EXPIRED", TerminalState: "EXPIRED", TerminalCode: "EXPIRED", Detail: "terminal outcome"}, nil
	}
	// A different caller request must observe the already-live owner intent
	// rather than colliding with the one-live-intent index as a generic 409.
	var pendingID uuid.UUID
	var pendingState string
	if err = tx.QueryRowContext(ctx, `SELECT consume_request_id,state FROM d1l_provider.d1l_bootstrap_consume_intents WHERE authorization_id=$1 AND state IN ('PENDING','CLAIMED') ORDER BY created_at LIMIT 1 FOR UPDATE`, aid).Scan(&pendingID, &pendingState); err == nil {
		if err = tx.Commit(); err != nil {
			return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Unknown}, ledger.ErrUnknownCommit
		}
		return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.IntentState(pendingState), AuthorizationState: lockedState, Detail: "IN_PROGRESS", PendingConsumeRequestID: pendingID.String()}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ConsumeResult{}, err
	}
	consumerIdentity := strings.TrimSpace(req.ConsumerIdentity)
	if consumerIdentity == "" {
		// Direct store callers predate the API identity seam. They remain bound
		// to one local owner rather than creating an unowned intent.
		consumerIdentity = "store-direct"
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO d1l_provider.d1l_bootstrap_consume_intents(consume_request_id,authorization_id,issuer_request_id,epoch_id,nonce,operation,attempt_id,target_id,installer_id,evidence_hash,scope,consumer_identity,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'PENDING')`, cid, aid, rid, req.Epoch, env.nonce, req.Operation, attempt, req.TargetID, req.InstallerID, req.EvidenceHash, req.Scope, consumerIdentity)
	if err != nil {
		return ConsumeResult{}, err
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		return ConsumeResult{}, errors.New("consume intent was not created")
	}
	// BEGIN commits only the durable child intent. The parent remains ISSUED;
	// CLAIM is the sole ISSUED -> CONSUME_PENDING linearization.
	if err = tx.Commit(); err != nil {
		return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Unknown}, ledger.ErrUnknownCommit
	}
	return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Pending, AuthorizationState: "ISSUED"}, nil
}

func (s *Store) claimConsume(ctx context.Context, req ConsumeRequest, begun ConsumeResult) (ConsumeResult, error) {
	cid := mustUUID(req.ConsumeRequestID)
	aid := mustUUID(req.AuthorizationID)
	rid := mustUUID(req.IssuerRequestID)
	env, err := parseEnvelope(req.Envelope)
	if err != nil {
		return ConsumeResult{}, err
	}
	nonce, err := decodeNonce(req.Nonce)
	if err != nil || !equal(nonce, env.nonce) {
		return ConsumeResult{}, errors.New("consume binding rejected")
	}
	attempt, err := parseID(req.AttemptID, "attempt_id")
	if err != nil {
		return ConsumeResult{}, err
	}
	tx, err := s.beginAuthorityTx(ctx)
	if err != nil {
		return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Unknown}, err
	}
	defer tx.Rollback()

	// The parent is the canonical lock and is always locked before the child.
	var parentState, parentOp, parentTarget, parentInstaller, parentEvidence, parentScope string
	var parentEpoch int64
	var parentNonce []byte
	var parentAttempt uuid.UUID
	var parentConsumeID, parentConsumeIssuer uuid.NullUUID
	var parentConsumeEpoch sql.NullInt64
	var parentConsumeAttempt uuid.NullUUID
	var parentConsumeNonce []byte
	var parentConsumeOp, parentConsumeTarget, parentConsumeInstaller, parentConsumeEvidence, parentConsumeScope sql.NullString
	var expires time.Time
	if err = tx.QueryRowContext(ctx, `SELECT state,epoch_id,nonce,operation,attempt_id,target_id,installer_id,evidence_hash,scope,expires_at,consume_request_id,consume_epoch_id,consume_issuer_request_id,consume_attempt_id,consume_nonce,consume_operation,consume_target_id,consume_installer_id,consume_evidence_hash,consume_scope FROM d1l_provider.d1l_bootstrap_authorizations WHERE authorization_id=$1 AND issuer_request_id=$2 FOR UPDATE`, aid, rid).Scan(&parentState, &parentEpoch, &parentNonce, &parentOp, &parentAttempt, &parentTarget, &parentInstaller, &parentEvidence, &parentScope, &expires, &parentConsumeID, &parentConsumeEpoch, &parentConsumeIssuer, &parentConsumeAttempt, &parentConsumeNonce, &parentConsumeOp, &parentConsumeTarget, &parentConsumeInstaller, &parentConsumeEvidence, &parentConsumeScope); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ConsumeResult{}, errors.New("authorization not found")
		}
		return ConsumeResult{}, err
	}
	if parentEpoch != req.Epoch || !equal(parentNonce, env.nonce) || parentOp != req.Operation || parentAttempt != attempt || parentTarget != req.TargetID || parentInstaller != req.InstallerID || parentEvidence != req.EvidenceHash || parentScope != req.Scope {
		return ConsumeResult{}, errors.New("consume binding rejected")
	}

	// Lock and validate the exact child after its parent.
	var state string
	var ea, er uuid.UUID
	var ep int64
	var en []byte
	var op string
	var at uuid.UUID
	var target, installer, evidence, scope string
	if err = tx.QueryRowContext(ctx, `SELECT state,authorization_id,issuer_request_id,epoch_id,nonce,operation,attempt_id,target_id,installer_id,evidence_hash,scope FROM d1l_provider.d1l_bootstrap_consume_intents WHERE consume_request_id=$1 FOR UPDATE`, cid).Scan(&state, &ea, &er, &ep, &en, &op, &at, &target, &installer, &evidence, &scope); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ConsumeResult{}, errors.New("consume request not found")
		}
		return ConsumeResult{}, err
	}
	if ea != aid || er != rid || ep != req.Epoch || !equal(en, env.nonce) || op != req.Operation || at != attempt || target != req.TargetID || installer != req.InstallerID || evidence != req.EvidenceHash || scope != req.Scope {
		return ConsumeResult{}, errors.New("consume request binding mismatch")
	}
	if state == "PENDING" {
		if parentState != "ISSUED" {
			code := "AUTHORIZATION_TERMINAL"
			if parentState == "CONSUME_PENDING" {
				code = "CONSUME_ALREADY_CLAIMED"
			}
			result, e := tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_consume_intents SET state='ABORTED',terminal_at=clock_timestamp(),terminal_code=$1,terminal_consumer='provider-claim',updated_at=clock_timestamp() WHERE consume_request_id=$2 AND authorization_id=$3 AND issuer_request_id=$4 AND epoch_id=$5 AND nonce=$6 AND operation=$7 AND attempt_id=$8 AND target_id=$9 AND installer_id=$10 AND evidence_hash=$11 AND scope=$12 AND state='PENDING'`, code, cid, aid, rid, ep, en, op, at, target, installer, evidence, scope)
			if e != nil {
				return ConsumeResult{}, e
			}
			if n, e := result.RowsAffected(); e != nil || n != 1 {
				return ConsumeResult{}, errors.New("consume intent changed")
			}
			if err = tx.Commit(); err != nil {
				return ConsumeResult{}, err
			}
			return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Aborted, AuthorizationState: parentState, TerminalState: parentState, TerminalCode: code}, nil
		}
		var now time.Time
		if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return ConsumeResult{}, err
		}
		if !expires.After(now) {
			if parentState != "ISSUED" {
				return ConsumeResult{}, errors.New("intent transition forbidden")
			}
			result, e := tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_authorizations SET state='EXPIRED',revoked_reason='EXPIRED',terminal_at=clock_timestamp(),terminal_code='EXPIRED',terminal_consumer='provider-claim',updated_at=clock_timestamp() WHERE authorization_id=$1 AND issuer_request_id=$2 AND epoch_id=$3 AND nonce=$4 AND operation=$5 AND attempt_id=$6 AND target_id=$7 AND installer_id=$8 AND evidence_hash=$9 AND scope=$10 AND state='ISSUED' AND consume_request_id IS NULL`, aid, rid, ep, en, op, at, target, installer, evidence, scope)
			if e != nil {
				return ConsumeResult{}, e
			}
			if n, _ := result.RowsAffected(); n != 1 {
				return ConsumeResult{}, errors.New("authorization transition forbidden")
			}
			result, e = tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_consume_intents SET state='ABORTED',terminal_at=clock_timestamp(),terminal_code='EXPIRED',terminal_consumer='provider-claim',updated_at=clock_timestamp() WHERE consume_request_id=$1 AND authorization_id=$2 AND issuer_request_id=$3 AND epoch_id=$4 AND nonce=$5 AND operation=$6 AND attempt_id=$7 AND target_id=$8 AND installer_id=$9 AND evidence_hash=$10 AND scope=$11 AND state='PENDING'`, cid, aid, rid, ep, en, op, at, target, installer, evidence, scope)
			if e != nil {
				return ConsumeResult{}, e
			}
			if n, _ := result.RowsAffected(); n != 1 {
				return ConsumeResult{}, errors.New("consume intent changed")
			}
			if err = tx.Commit(); err != nil {
				return ConsumeResult{}, err
			}
			return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Aborted, AuthorizationState: "EXPIRED", TerminalState: "EXPIRED", TerminalCode: "EXPIRED", Detail: "terminal outcome"}, nil
		}
		if parentState != "ISSUED" {
			return ConsumeResult{}, errors.New("intent transition forbidden")
		}
		result, e := tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_authorizations SET state='CONSUME_PENDING',consume_request_id=$1,consume_epoch_id=$2,consume_issuer_request_id=$3,consume_attempt_id=$4,consume_nonce=$5,consume_operation=$6,consume_target_id=$7,consume_installer_id=$8,consume_evidence_hash=$9,consume_scope=$10,consume_claimed_at=clock_timestamp(),updated_at=clock_timestamp() WHERE authorization_id=$11 AND issuer_request_id=$3 AND epoch_id=$2 AND nonce=$5 AND operation=$6 AND attempt_id=$4 AND target_id=$7 AND installer_id=$8 AND evidence_hash=$9 AND scope=$10 AND state='ISSUED' AND consume_request_id IS NULL`, cid, ep, rid, at, en, op, target, installer, evidence, scope, aid)
		if e != nil {
			return ConsumeResult{}, e
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return ConsumeResult{}, errors.New("authorization transition forbidden")
		}
		result, e = tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_consume_intents SET state='CLAIMED',claimed_at=clock_timestamp(),updated_at=clock_timestamp() WHERE consume_request_id=$1 AND authorization_id=$2 AND issuer_request_id=$3 AND epoch_id=$4 AND nonce=$5 AND operation=$6 AND attempt_id=$7 AND target_id=$8 AND installer_id=$9 AND evidence_hash=$10 AND scope=$11 AND state='PENDING'`, cid, aid, rid, ep, en, op, at, target, installer, evidence, scope)
		if e != nil {
			return ConsumeResult{}, e
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return ConsumeResult{}, errors.New("consume intent changed")
		}
		state = "CLAIMED"
		parentState = "CONSUME_PENDING"
	} else if state == "CLAIMED" {
		if parentState != "CONSUME_PENDING" || !parentConsumeID.Valid || parentConsumeID.UUID != cid || !parentConsumeEpoch.Valid || parentConsumeEpoch.Int64 != ep || !parentConsumeIssuer.Valid || parentConsumeIssuer.UUID != rid || !parentConsumeAttempt.Valid || parentConsumeAttempt.UUID != at || !equal(parentConsumeNonce, en) || !parentConsumeOp.Valid || parentConsumeOp.String != op || !parentConsumeTarget.Valid || parentConsumeTarget.String != target || !parentConsumeInstaller.Valid || parentConsumeInstaller.String != installer || !parentConsumeEvidence.Valid || parentConsumeEvidence.String != evidence || !parentConsumeScope.Valid || parentConsumeScope.String != scope {
			return ConsumeResult{}, errors.New("intent transition forbidden")
		}
	}
	if err = tx.Commit(); err != nil {
		return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Unknown}, ledger.ErrUnknownCommit
	}
	return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.IntentState(state), AuthorizationState: parentState}, nil
}

func sha256Bytes(b []byte) []byte { h := sha256.Sum256(b); return h[:] }
