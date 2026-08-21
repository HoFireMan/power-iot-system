package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"power-iot-a3-deployment-control-plane/internal/ledger"
)

type ConsumeRequest struct {
	ConsumeRequestID string `json:"consume_request_id"`
	AuthorizationID  string `json:"authorization_id"`
	IssuerRequestID  string `json:"issuer_request_id"`
	Operation        string `json:"operation"`
	AttemptID        string `json:"attempt_id"`
	TargetID         string `json:"target_id"`
	InstallerID      string `json:"installer_id"`
	EvidenceHash     string `json:"evidence_hash"`
	Scope            string `json:"scope"`
	Nonce            string `json:"nonce"`
	Envelope         string `json:"envelope"`
	// Secret is retained only for source compatibility and is never decoded
	// from or emitted on the wire.
	Secret string `json:"-"`
	Epoch  int64  `json:"epoch"`
	// ConsumerIdentity is populated only from the authenticated mTLS URI by
	// the API. It is never accepted from the wire request body.
	ConsumerIdentity string `json:"-"`
}
type ConsumeResult struct {
	AuthorizationID         string             `json:"authorization_id"`
	ConsumeRequestID        string             `json:"consume_request_id"`
	State                   ledger.IntentState `json:"state"`
	AuthorizationState      string             `json:"authorization_state,omitempty"`
	TerminalState           string             `json:"terminal_state,omitempty"`
	TerminalCode            string             `json:"terminal_code,omitempty"`
	Detail                  string             `json:"detail,omitempty"`
	PendingConsumeRequestID string             `json:"pending_consume_request_id,omitempty"`
}

func parseID(v, label string) (uuid.UUID, error) {
	id, err := uuid.Parse(v)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid %s", label)
	}
	return id, nil
}
func decodeNonce(v string) ([]byte, error) {
	b, err := base64.RawStdEncoding.DecodeString(v)
	if err != nil || len(b) != 16 {
		return nil, errors.New("invalid nonce")
	}
	return b, nil
}
func secretHash(v string) ([]byte, error) {
	b, err := base64.RawStdEncoding.DecodeString(v)
	if err != nil || len(b) != 32 {
		return nil, errors.New("invalid secret")
	}
	h := sha256.Sum256(b)
	return h[:], nil
}
func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var x byte
	for i := range a {
		x |= a[i] ^ b[i]
	}
	return x == 0
}

// legacyConsume is retained only as a compatibility shim; all callers use CR3.
func (s *Store) legacyConsume(ctx context.Context, req ConsumeRequest) (ConsumeResult, error) {
	return s.Consume(ctx, req)
}

func (s *Store) finalizeConsume(ctx context.Context, aid, cid uuid.UUID) (ConsumeResult, error) {
	tx, err := s.beginAuthorityTx(ctx)
	if err != nil {
		return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Unknown}, ledger.ErrUnknownCommit
	}
	defer tx.Rollback()
	// Lock parent then child, and use the complete consume ownership tuple in
	// both predicates. No unrelated child can finalize this authorization.
	var issuer uuid.UUID
	var epoch int64
	var nonce []byte
	var op, target, installer, evidence, scope, authState string
	var authAttempt uuid.UUID
	if err = tx.QueryRowContext(ctx, `SELECT issuer_request_id,epoch_id,nonce,operation,attempt_id,target_id,installer_id,evidence_hash,scope,state FROM d1l_provider.d1l_bootstrap_authorizations WHERE authorization_id=$1 FOR UPDATE`, aid).Scan(&issuer, &epoch, &nonce, &op, &authAttempt, &target, &installer, &evidence, &scope, &authState); err != nil {
		return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Unknown}, err
	}
	var childState string
	var childEpoch int64
	var childNonce []byte
	var childOperation, childTarget, childInstaller, childEvidence, childScope string
	var childAttempt uuid.UUID
	if err = tx.QueryRowContext(ctx, `SELECT state,epoch_id,nonce,operation,attempt_id,target_id,installer_id,evidence_hash,scope FROM d1l_provider.d1l_bootstrap_consume_intents WHERE consume_request_id=$1 AND authorization_id=$2 AND issuer_request_id=$3 FOR UPDATE`, cid, aid, issuer).Scan(&childState, &childEpoch, &childNonce, &childOperation, &childAttempt, &childTarget, &childInstaller, &childEvidence, &childScope); err != nil {
		return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Unknown}, ledger.ErrUnknownCommit
	}
	if authState != "CONSUME_PENDING" || childState != "CLAIMED" || childEpoch != epoch || !equal(childNonce, nonce) || childOperation != op || childAttempt != authAttempt || childTarget != target || childInstaller != installer || childEvidence != evidence || childScope != scope {
		return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Unknown}, ledger.ErrUnknownCommit
	}
	result, err := tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_authorizations SET state='CONSUMED',consume_terminal_at=clock_timestamp(),consume_terminal_code='CONSUMED',consume_consumer='provider-finalize',terminal_at=clock_timestamp(),terminal_code='CONSUMED',terminal_consumer='provider-finalize',updated_at=clock_timestamp() WHERE authorization_id=$1 AND issuer_request_id=$2 AND epoch_id=$3 AND nonce=$4 AND operation=$5 AND attempt_id=$6 AND target_id=$7 AND installer_id=$8 AND evidence_hash=$9 AND scope=$10 AND state='CONSUME_PENDING' AND consume_request_id=$11 AND consume_epoch_id=$3 AND consume_issuer_request_id=$2 AND consume_attempt_id=$6 AND consume_nonce=$4 AND consume_operation=$5 AND consume_target_id=$7 AND consume_installer_id=$8 AND consume_evidence_hash=$9 AND consume_scope=$10`, aid, issuer, epoch, nonce, op, authAttempt, target, installer, evidence, scope, cid)
	if err != nil {
		return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Unknown}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Unknown}, ledger.ErrUnknownCommit
	}
	result, err = tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_consume_intents SET state='CONSUMED',consumed_at=clock_timestamp(),terminal_at=clock_timestamp(),terminal_code='CONSUMED',terminal_consumer='provider-finalize',updated_at=clock_timestamp() WHERE consume_request_id=$1 AND authorization_id=$2 AND issuer_request_id=$3 AND epoch_id=$4 AND nonce=$5 AND operation=$6 AND attempt_id=$7 AND target_id=$8 AND installer_id=$9 AND evidence_hash=$10 AND scope=$11 AND state='CLAIMED'`, cid, aid, issuer, childEpoch, childNonce, childOperation, childAttempt, childTarget, childInstaller, childEvidence, childScope)
	if err != nil {
		return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Unknown}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Unknown}, ledger.ErrUnknownCommit
	}
	if err = tx.Commit(); err != nil {
		return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.Unknown}, ledger.ErrUnknownCommit
	}
	return ConsumeResult{AuthorizationID: aid.String(), ConsumeRequestID: cid.String(), State: ledger.IntentConsumed, AuthorizationState: "CONSUMED", TerminalState: "CONSUMED", TerminalCode: "CONSUMED"}, nil
}

type ResolveConsumeRequest struct {
	ConsumeRequestID string `json:"consume_request_id"`
	AuthorizationID  string `json:"authorization_id"`
	IssuerRequestID  string `json:"issuer_request_id"`
	Operation        string `json:"operation"`
	AttemptID        string `json:"attempt_id"`
	TargetID         string `json:"target_id"`
	InstallerID      string `json:"installer_id"`
	EvidenceHash     string `json:"evidence_hash"`
	Scope            string `json:"scope"`
	Epoch            int64  `json:"epoch"`
	Nonce            string `json:"nonce"`
	// ConsumerIdentity is populated from the authenticated mTLS URI.
	ConsumerIdentity string `json:"-"`
}

// ResolveConsume only performs terminal reconciliation. It cannot restore,
// mint, rebind, reopen, or manufacture CONSUMED.
func (s *Store) ResolveConsume(ctx context.Context, r ResolveConsumeRequest, recovery bool) (ResolveResult, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := s.requireAuthority(ctx); err != nil {
		return ResolveResult{}, err
	}
	aid, err := parseID(r.AuthorizationID, "authorization_id")
	if err != nil {
		return ResolveResult{}, err
	}
	rid, err := parseID(r.IssuerRequestID, "issuer_request_id")
	if err != nil {
		return ResolveResult{}, err
	}
	nonce, err := decodeNonce(r.Nonce)
	if err != nil {
		return ResolveResult{}, err
	}
	attempt, err := parseID(r.AttemptID, "attempt_id")
	if err != nil {
		return ResolveResult{}, err
	}
	tx, err := s.beginAuthorityTx(ctx)
	if err != nil {
		return ResolveResult{}, err
	}
	defer tx.Rollback()
	// Parent first is the canonical lock order shared with Consume.
	var lockedParent uuid.UUID
	if err = tx.QueryRowContext(ctx, `SELECT authorization_id FROM d1l_provider.d1l_bootstrap_authorizations WHERE authorization_id=$1 AND issuer_request_id=$2 FOR UPDATE`, aid, rid).Scan(&lockedParent); err != nil {
		return ResolveResult{}, errors.New("authorization not found")
	}
	var cid uuid.UUID
	if r.ConsumeRequestID != "" {
		cid, err = parseID(r.ConsumeRequestID, "consume_request_id")
		if err != nil {
			return ResolveResult{}, err
		}
	} else if !recovery {
		return ResolveResult{}, errors.New("consume_request_id is required")
	} else {
		err = tx.QueryRowContext(ctx, `SELECT consume_request_id FROM d1l_provider.d1l_bootstrap_consume_intents WHERE authorization_id=$1 AND issuer_request_id=$2 AND epoch_id=$3 AND operation=$4 AND attempt_id=$5 AND target_id=$6 AND installer_id=$7 AND evidence_hash=$8 AND scope=$9 AND nonce=$10 AND state IN ('PENDING','CLAIMED') ORDER BY created_at LIMIT 1 FOR UPDATE`, aid, rid, r.Epoch, r.Operation, attempt, r.TargetID, r.InstallerID, r.EvidenceHash, r.Scope, nonce).Scan(&cid)
		if err != nil {
			return ResolveResult{}, errors.New("pending consume request not found")
		}
	}
	var state, op, target, installer, evidence, scope, consumerIdentity string
	var sa, si uuid.UUID
	var ep int64
	var an uuid.UUID
	var storedNonce []byte
	err = tx.QueryRowContext(ctx, `SELECT state,authorization_id,issuer_request_id,epoch_id,nonce,operation,attempt_id,target_id,installer_id,evidence_hash,scope,consumer_identity FROM d1l_provider.d1l_bootstrap_consume_intents WHERE consume_request_id=$1 FOR UPDATE`, cid).Scan(&state, &sa, &si, &ep, &storedNonce, &op, &an, &target, &installer, &evidence, &scope, &consumerIdentity)
	if err != nil {
		return ResolveResult{}, errors.New("consume request not found")
	}
	ownerIdentity := strings.TrimSpace(r.ConsumerIdentity)
	if ownerIdentity == "" {
		ownerIdentity = "store-direct"
	}
	if sa != aid || si != rid || ep != r.Epoch || !equal(storedNonce, nonce) || op != r.Operation || an != attempt || target != r.TargetID || installer != r.InstallerID || evidence != r.EvidenceHash || scope != r.Scope {
		return ResolveResult{}, errors.New("resolve binding rejected")
	}
	if !recovery && consumerIdentity != ownerIdentity {
		return ResolveResult{}, errors.New("resolve owner rejected")
	}
	var authState string
	var authEpoch int64
	var authNonce []byte
	var authOperation, authTarget, authInstaller, authEvidence, authScope string
	var authAttempt uuid.UUID
	var authTerminal sql.NullString
	var authExpires time.Time
	var authConsumeID uuid.NullUUID
	var authConsumeEpoch sql.NullInt64
	var authConsumeIssuer, authConsumeAttempt uuid.NullUUID
	var authConsumeNonce []byte
	var authConsumeOperation, authConsumeTarget, authConsumeInstaller, authConsumeEvidence, authConsumeScope sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT state,epoch_id,nonce,operation,attempt_id,target_id,installer_id,evidence_hash,scope,expires_at,terminal_code,consume_request_id,consume_epoch_id,consume_issuer_request_id,consume_attempt_id,consume_nonce,consume_operation,consume_target_id,consume_installer_id,consume_evidence_hash,consume_scope FROM d1l_provider.d1l_bootstrap_authorizations WHERE authorization_id=$1 AND issuer_request_id=$2 FOR UPDATE`, aid, rid).Scan(&authState, &authEpoch, &authNonce, &authOperation, &authAttempt, &authTarget, &authInstaller, &authEvidence, &authScope, &authExpires, &authTerminal, &authConsumeID, &authConsumeEpoch, &authConsumeIssuer, &authConsumeAttempt, &authConsumeNonce, &authConsumeOperation, &authConsumeTarget, &authConsumeInstaller, &authConsumeEvidence, &authConsumeScope)
	if err != nil {
		return ResolveResult{}, errors.New("authorization not found")
	}
	if authEpoch != r.Epoch || !equal(authNonce, nonce) || authOperation != r.Operation || authAttempt != attempt || authTarget != r.TargetID || authInstaller != r.InstallerID || authEvidence != r.EvidenceHash || authScope != r.Scope {
		return ResolveResult{}, errors.New("resolve binding rejected")
	}
	if state == "PENDING" {
		if authState != "ISSUED" || authConsumeID.Valid || authConsumeEpoch.Valid || authConsumeIssuer.Valid || authConsumeAttempt.Valid || len(authConsumeNonce) != 0 || authConsumeOperation.Valid || authConsumeTarget.Valid || authConsumeInstaller.Valid || authConsumeEvidence.Valid || authConsumeScope.Valid {
			return ResolveResult{}, errors.New("resolve binding rejected")
		}
	}
	if state == "CLAIMED" || state == "CONSUMED" || state == "UNKNOWN" || state == "CONSUME_UNKNOWN" {
		if !authConsumeID.Valid || authConsumeID.UUID != cid || !authConsumeEpoch.Valid || authConsumeEpoch.Int64 != ep || !authConsumeIssuer.Valid || authConsumeIssuer.UUID != rid || !authConsumeAttempt.Valid || authConsumeAttempt.UUID != attempt || !equal(authConsumeNonce, storedNonce) || !authConsumeOperation.Valid || authConsumeOperation.String != op || !authConsumeTarget.Valid || authConsumeTarget.String != target || !authConsumeInstaller.Valid || authConsumeInstaller.String != installer || !authConsumeEvidence.Valid || authConsumeEvidence.String != evidence || !authConsumeScope.Valid || authConsumeScope.String != scope {
			return ResolveResult{}, errors.New("resolve binding rejected")
		}
	}
	out := ResolveResult{AuthorizationID: aid.String(), IssuerRequestID: rid.String(), ConsumeRequestID: cid.String(), AuthState: ledger.AuthState(authState), IntentState: ledger.IntentState(state), TerminalState: authState}
	if authTerminal.Valid {
		out.TerminalCode = authTerminal.String
	}
	consumer := ownerIdentity
	if recovery {
		consumer = ownerIdentity
	}
	if state == "PENDING" {
		if authState != "ISSUED" {
			return ResolveResult{}, errors.New("intent transition forbidden")
		}
		// A committed ISSUED+PENDING pair is reconciled as a pair. Expiry is
		// the only issuer-owned reason Resolve may use EXPIRED; otherwise it
		// revokes the authorization and aborts the intent atomically.
		reason, code := "REVOKED", "PENDING_CONSUME_RESOLVED"
		var now time.Time
		if e := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return ResolveResult{}, e
		}
		if !authExpires.After(now) {
			reason, code = "EXPIRED", "EXPIRED"
		}
		result, e := tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_authorizations SET state=$1,revoked_reason=$2,terminal_at=clock_timestamp(),terminal_code=$3,terminal_consumer=$4,updated_at=clock_timestamp() WHERE authorization_id=$5 AND issuer_request_id=$6 AND epoch_id=$7 AND nonce=$8 AND operation=$9 AND attempt_id=$10 AND target_id=$11 AND installer_id=$12 AND evidence_hash=$13 AND scope=$14 AND state='ISSUED'`, reason, code, code, consumer, aid, rid, r.Epoch, nonce, r.Operation, an, r.TargetID, r.InstallerID, r.EvidenceHash, r.Scope)
		if e != nil {
			return ResolveResult{}, e
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return ResolveResult{}, errors.New("authorization transition forbidden")
		}
		result, e = tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_consume_intents SET state='ABORTED',terminal_at=clock_timestamp(),terminal_code=$2,terminal_consumer=$3,updated_at=clock_timestamp() WHERE consume_request_id=$1 AND authorization_id=$4 AND issuer_request_id=$5 AND epoch_id=$6 AND nonce=$7 AND operation=$8 AND attempt_id=$9 AND target_id=$10 AND installer_id=$11 AND evidence_hash=$12 AND scope=$13 AND state='PENDING'`, cid, code, consumer, aid, rid, r.Epoch, nonce, r.Operation, an, r.TargetID, r.InstallerID, r.EvidenceHash, r.Scope)
		if e != nil {
			return ResolveResult{}, e
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return ResolveResult{}, errors.New("intent changed")
		}
		out.AuthState = ledger.AuthState(reason)
		out.IntentState = ledger.Aborted
		out.TerminalState, out.TerminalCode = reason, code
	} else if state == "CLAIMED" {
		if authState == "CONSUMED" {
			result, e := tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_consume_intents SET state='CONSUMED',consumed_at=clock_timestamp(),terminal_at=clock_timestamp(),terminal_code='CONSUMED',terminal_consumer=$2,updated_at=clock_timestamp() WHERE consume_request_id=$1 AND authorization_id=$3 AND issuer_request_id=$4 AND epoch_id=$5 AND nonce=$6 AND operation=$7 AND attempt_id=$8 AND target_id=$9 AND installer_id=$10 AND evidence_hash=$11 AND scope=$12 AND state='CLAIMED'`, cid, consumer, aid, rid, r.Epoch, nonce, r.Operation, an, r.TargetID, r.InstallerID, r.EvidenceHash, r.Scope)
			if e != nil {
				return ResolveResult{}, e
			}
			if n, _ := result.RowsAffected(); n != 1 {
				return ResolveResult{}, errors.New("intent changed")
			}
			out.IntentState = ledger.IntentConsumed
			out.TerminalState, out.TerminalCode = "CONSUMED", "CONSUMED"
		} else if authState == "CONSUME_PENDING" {
			result, e := tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_authorizations SET state='CONSUME_UNKNOWN',revoked_reason='CONSUME_OUTCOME_UNKNOWN',consume_terminal_at=clock_timestamp(),consume_terminal_code='CONSUME_OUTCOME_UNKNOWN',consume_consumer=$2,terminal_at=clock_timestamp(),terminal_code='CONSUME_OUTCOME_UNKNOWN',terminal_consumer=$2,updated_at=clock_timestamp() WHERE authorization_id=$1 AND issuer_request_id=$3 AND epoch_id=$4 AND nonce=$5 AND operation=$6 AND attempt_id=$7 AND target_id=$8 AND installer_id=$9 AND evidence_hash=$10 AND scope=$11 AND state='CONSUME_PENDING' AND consume_request_id=$12 AND consume_epoch_id=$4 AND consume_issuer_request_id=$3 AND consume_attempt_id=$7 AND consume_nonce=$5 AND consume_operation=$6 AND consume_target_id=$8 AND consume_installer_id=$9 AND consume_evidence_hash=$10 AND consume_scope=$11`, aid, consumer, rid, r.Epoch, nonce, r.Operation, an, r.TargetID, r.InstallerID, r.EvidenceHash, r.Scope, cid)
			if e != nil {
				return ResolveResult{}, e
			}
			if n, _ := result.RowsAffected(); n != 1 {
				return ResolveResult{}, errors.New("authorization transition forbidden")
			}
			result, e = tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_consume_intents SET state='UNKNOWN',terminal_at=clock_timestamp(),terminal_code='CONSUME_OUTCOME_UNKNOWN',terminal_consumer=$2,updated_at=clock_timestamp() WHERE consume_request_id=$1 AND authorization_id=$3 AND issuer_request_id=$4 AND epoch_id=$5 AND nonce=$6 AND operation=$7 AND attempt_id=$8 AND target_id=$9 AND installer_id=$10 AND evidence_hash=$11 AND scope=$12 AND state='CLAIMED'`, cid, consumer, aid, rid, r.Epoch, nonce, r.Operation, an, r.TargetID, r.InstallerID, r.EvidenceHash, r.Scope)
			if e != nil {
				return ResolveResult{}, e
			}
			if n, _ := result.RowsAffected(); n != 1 {
				return ResolveResult{}, errors.New("intent changed")
			}
			out.AuthState = ledger.AuthState("CONSUME_UNKNOWN")
			out.IntentState = ledger.Unknown
			out.TerminalState, out.TerminalCode = "CONSUME_UNKNOWN", "CONSUME_OUTCOME_UNKNOWN"
		} else {
			return ResolveResult{}, errors.New("intent transition forbidden")
		}
	}
	if err = tx.Commit(); err != nil {
		return ResolveResult{}, err
	}
	return out, nil
}

func (s *Store) ResolveIssue(ctx context.Context, requestID string) (ResolveResult, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := s.requireAuthority(ctx); err != nil {
		return ResolveResult{}, err
	}
	rid, err := parseID(requestID, "issuer_request_id")
	if err != nil {
		return ResolveResult{}, err
	}
	tx, err := s.beginAuthorityTx(ctx)
	if err != nil {
		return ResolveResult{}, err
	}
	defer tx.Rollback()
	var role, state string
	var attempt uuid.UUID
	var aid uuid.NullUUID
	err = tx.QueryRowContext(ctx, `SELECT issuer_role,state,attempt_id,authorization_id FROM d1l_provider.d1l_issue_requests WHERE issuer_request_id=$1 FOR UPDATE`, rid).Scan(&role, &state, &attempt, &aid)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err = tx.ExecContext(ctx, `INSERT INTO d1l_provider.d1l_issue_requests(issuer_request_id,issuer_role,attempt_id,state,terminal_at,terminal_code,terminal_consumer) VALUES($1,'deployment-runbook',$2,'CANCELLED',clock_timestamp(),'RESOLVE_ISSUE_NOT_FOUND','resolve-issue') ON CONFLICT (issuer_request_id) DO NOTHING`, rid, uuid.New()); err != nil {
			return ResolveResult{}, err
		}
		err = tx.QueryRowContext(ctx, `SELECT issuer_role,state,attempt_id,authorization_id FROM d1l_provider.d1l_issue_requests WHERE issuer_request_id=$1 FOR UPDATE`, rid).Scan(&role, &state, &attempt, &aid)
	}
	if err != nil {
		return ResolveResult{}, errors.New("issuer request tombstone unavailable")
	}
	out := ResolveResult{IssuerRequestID: rid.String(), AuthState: ledger.AuthState(state), TerminalState: state}
	if state == "REQUESTED" {
		result, e := tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_issue_requests SET state='TERMINAL',terminal_at=clock_timestamp(),terminal_code='SECRET_UNAVAILABLE',terminal_consumer='resolve-issue',updated_at=clock_timestamp() WHERE issuer_request_id=$1 AND state='REQUESTED'`, rid)
		if e != nil {
			return ResolveResult{}, e
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return ResolveResult{}, errors.New("issue request changed")
		}
		out.AuthState = ledger.AuthState("TERMINAL")
		out.TerminalState, out.TerminalCode = "TERMINAL", "SECRET_UNAVAILABLE"
	}
	if aid.Valid {
		out.AuthorizationID = aid.UUID.String()
		var authState string
		var consumeID uuid.NullUUID
		var intentState sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT state FROM d1l_provider.d1l_bootstrap_authorizations WHERE authorization_id=$1 AND issuer_request_id=$2 FOR UPDATE`, aid.UUID, rid).Scan(&authState)
		if err != nil {
			return ResolveResult{}, err
		}
		out.AuthState = ledger.AuthState(authState)
		err = tx.QueryRowContext(ctx, `SELECT consume_request_id,state FROM d1l_provider.d1l_bootstrap_consume_intents WHERE authorization_id=$1 AND issuer_request_id=$2 AND state IN ('PENDING','CLAIMED') ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, aid.UUID, rid).Scan(&consumeID, &intentState)
		if err == nil {
			out.AuthState = ledger.AuthState(authState)
			out.ConsumeRequestID = consumeID.UUID.String()
			out.IntentState = ledger.IntentState(intentState.String)
			if authState != "ISSUED" && authState != "CONSUME_PENDING" {
				childState, code := "ABORTED", "AUTHORITY_TERMINAL_CHILD"
				if intentState.String == "CLAIMED" {
					childState = "UNKNOWN"
				}
				result, e := tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_consume_intents SET state=$1,terminal_at=clock_timestamp(),terminal_code=$2,terminal_consumer='resolve-issue',updated_at=clock_timestamp() WHERE consume_request_id=$3 AND authorization_id=$4 AND issuer_request_id=$5 AND state IN ('PENDING','CLAIMED')`, childState, code, consumeID.UUID, aid.UUID, rid)
				if e != nil {
					return ResolveResult{}, e
				}
				if n, _ := result.RowsAffected(); n != 1 {
					return ResolveResult{}, errors.New("consume intent changed")
				}
				out.IntentState = ledger.IntentState(childState)
				out.TerminalState, out.TerminalCode = authState, code
				out.Detail = "terminal child reconciled"
			} else {
				out.Detail = "IN_PROGRESS"
				out.TerminalState = "IN_PROGRESS"
			}
		} else if errors.Is(err, sql.ErrNoRows) && authState == "ISSUED" {
			result, e := tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_authorizations SET state='REVOKED',revoked_reason='SECRET_UNAVAILABLE',terminal_at=clock_timestamp(),terminal_code='SECRET_UNAVAILABLE',terminal_consumer='resolve-issue',updated_at=clock_timestamp() WHERE authorization_id=$1 AND issuer_request_id=$2 AND state='ISSUED'`, aid.UUID, rid)
			if e != nil {
				return ResolveResult{}, e
			}
			if n, _ := result.RowsAffected(); n != 1 {
				return ResolveResult{}, errors.New("authorization changed")
			}
			out.AuthState = ledger.Revoked
			out.TerminalState, out.TerminalCode = "REVOKED", "SECRET_UNAVAILABLE"
			out.Detail = "SECRET_UNAVAILABLE"
		} else if !errors.Is(err, sql.ErrNoRows) {
			return ResolveResult{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return ResolveResult{}, err
	}
	return out, nil
}
