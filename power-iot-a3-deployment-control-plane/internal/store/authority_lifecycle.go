package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"power-iot-a3-deployment-control-plane/internal/ledger"
)

func (s *Store) lifecycle(ctx context.Context, authorizationID, issuerRequestID, action string) (ResolveResult, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := s.requireAuthority(ctx); err != nil {
		return ResolveResult{}, err
	}
	aid, err := parseID(authorizationID, "authorization_id")
	if err != nil {
		return ResolveResult{}, err
	}
	rid, err := parseID(issuerRequestID, "issuer_request_id")
	if err != nil {
		return ResolveResult{}, err
	}
	tx, err := s.beginAuthorityTx(ctx)
	if err != nil {
		return ResolveResult{}, err
	}
	defer tx.Rollback()
	var state string
	var terminal sql.NullString
	var expiresAt time.Time
	if err = tx.QueryRowContext(ctx, `SELECT state,terminal_code,expires_at FROM d1l_provider.d1l_bootstrap_authorizations WHERE authorization_id=$1 AND issuer_request_id=$2 FOR UPDATE`, aid, rid).Scan(&state, &terminal, &expiresAt); err != nil {
		return ResolveResult{}, errors.New("authorization not found")
	}
	out := ResolveResult{AuthorizationID: aid.String(), IssuerRequestID: rid.String(), AuthState: ledger.AuthState(state), TerminalState: state}
	if terminal.Valid {
		out.TerminalCode = terminal.String
	}
	var cid uuid.NullUUID
	var intent sql.NullString
	_ = tx.QueryRowContext(ctx, `SELECT consume_request_id,state FROM d1l_provider.d1l_bootstrap_consume_intents WHERE authorization_id=$1 AND issuer_request_id=$2 AND state IN ('PENDING','CLAIMED') ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, aid, rid).Scan(&cid, &intent)
	if cid.Valid {
		out.ConsumeRequestID = cid.UUID.String()
		out.IntentState = ledger.IntentState(intent.String)
		out.Detail = "IN_PROGRESS"
		if action == "expire" {
			var now time.Time
			if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
				return ResolveResult{}, err
			}
			// Expire is a conditional CAS: a trusted issuer must not destroy
			// an unexpired presentation. Claimed work remains untouched.
			if expiresAt.After(now) {
				if err = tx.Commit(); err != nil {
					return ResolveResult{}, err
				}
				return out, nil
			}
		}
		// Issuer resolution never touches a claimed request. A pending pair,
		// however, must not leave a live child under the terminal parent.
		if intent.String == "CLAIMED" {
			if err = tx.Commit(); err != nil {
				return ResolveResult{}, err
			}
			return out, nil
		}
		if intent.String == "PENDING" {
			reason := "REVOKED"
			if action == "expire" {
				reason = "EXPIRED"
			}
			if state == "ISSUED" {
				result, e := tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_authorizations SET state=$1,revoked_reason=$2,terminal_at=clock_timestamp(),terminal_code=$2,terminal_consumer=$3,updated_at=clock_timestamp() WHERE authorization_id=$4 AND issuer_request_id=$5 AND state='ISSUED'`, reason, reason, "provider-"+action, aid, rid)
				if e != nil {
					return ResolveResult{}, e
				}
				if n, _ := result.RowsAffected(); n != 1 {
					return ResolveResult{}, errors.New("authorization changed")
				}
			}
			result, e := tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_consume_intents SET state='ABORTED',terminal_at=clock_timestamp(),terminal_code=$2,terminal_consumer=$3,updated_at=clock_timestamp() WHERE consume_request_id=$1 AND authorization_id=$4 AND issuer_request_id=$5 AND state='PENDING'`, cid.UUID, reason, "provider-"+action, aid, rid)
			if e != nil {
				return ResolveResult{}, e
			}
			if n, _ := result.RowsAffected(); n != 1 {
				return ResolveResult{}, errors.New("consume intent changed")
			}
			if state == "ISSUED" {
				out.AuthState = ledger.AuthState(reason)
				out.TerminalState, out.TerminalCode = reason, reason
			}
			out.IntentState = ledger.Aborted
			if err = tx.Commit(); err != nil {
				return ResolveResult{}, err
			}
			return out, nil
		}
	}
	if state != "ISSUED" {
		if err = tx.Commit(); err != nil {
			return ResolveResult{}, err
		}
		return out, nil
	}
	reason := "REVOKED"
	if action == "expire" {
		var now time.Time
		if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return ResolveResult{}, err
		}
		if expiresAt.After(now) {
			if err = tx.Commit(); err != nil {
				return ResolveResult{}, err
			}
			return out, nil
		}
		reason = "EXPIRED"
	}
	q := `UPDATE d1l_provider.d1l_bootstrap_authorizations SET state=$1,revoked_reason=$2,terminal_at=clock_timestamp(),terminal_code=$2,terminal_consumer=$3,updated_at=clock_timestamp() WHERE authorization_id=$4 AND issuer_request_id=$5 AND state='ISSUED' AND ($1 <> 'EXPIRED' OR expires_at <= clock_timestamp())`
	result, err := tx.ExecContext(ctx, q, reason, reason, "provider-"+action, aid, rid)
	if err != nil {
		return ResolveResult{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ResolveResult{}, errors.New("authorization changed")
	}
	out.AuthState = ledger.AuthState(reason)
	out.TerminalState = reason
	out.TerminalCode = reason
	if err = tx.Commit(); err != nil {
		return ResolveResult{}, err
	}
	return out, nil
}

func (s *Store) Expire(ctx context.Context, authorizationID, issuerRequestID string) (ResolveResult, error) {
	return s.lifecycle(ctx, authorizationID, issuerRequestID, "expire")
}
func (s *Store) Revoke(ctx context.Context, authorizationID, issuerRequestID string) (ResolveResult, error) {
	return s.lifecycle(ctx, authorizationID, issuerRequestID, "revoke")
}
