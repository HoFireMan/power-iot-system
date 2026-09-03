package persistence

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"power-iot-backend/internal/core/domain"
)

func TestAdminBindingAuditHistoryPostgresAuthorizationProjectionFiltersPaginationAndIntegrity(t *testing.T) {
	db := authDB(t)
	suffix := uuid.NewString()[:12]
	client := domain.Client{Code: "audit-read-client-" + suffix, Name: "Audit Client"}
	source := domain.Shop{ClientID: client.ID, Code: "audit-read-source-" + suffix, Name: "Source Shop", IsActive: true}
	target := domain.Shop{ClientID: client.ID, Code: "audit-read-target-" + suffix, Name: "Target Shop", IsActive: true}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	source.ClientID, target.ClientID = client.ID, client.ID
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	oldPoint := domain.MeasurementPoint{ID: uuid.New(), ShopID: source.ID, Name: "Old Point"}
	newPoint := domain.MeasurementPoint{ID: uuid.New(), ShopID: target.ID, Name: "New Point"}
	if err := db.Create(&oldPoint).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&newPoint).Error; err != nil {
		t.Fatal(err)
	}
	admin := domain.User{Account: "audit-read-admin-" + suffix, PasswordHash: "hash", Name: "Audit Admin", IsAdmin: true, AuthEnabled: true}
	targetOnly := domain.User{Account: "audit-read-target-" + suffix, PasswordHash: "hash", Name: "Target Only", IsAdmin: true, AuthEnabled: true}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&targetOnly).Error; err != nil {
		t.Fatal(err)
	}
	for _, relation := range []domain.UserShopRelation{{UserID: admin.ID, ShopID: source.ID}, {UserID: admin.ID, ShopID: target.ID}, {UserID: targetOnly.ID, ShopID: target.ID}} {
		if err := db.Create(&relation).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2099, 9, 2, 12, 0, 0, 0, time.UTC)
	session := RefreshSession{ID: uuid.New(), UserID: admin.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	targetSession := RefreshSession{ID: uuid.New(), UserID: targetOnly.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&targetSession).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		// The audit table is intentionally immutable, so TRUNCATE is the only
		// normal test cleanup operation that does not exercise a forbidden DML.
		db.Exec("TRUNCATE admin_binding_audits, admin_binding_operations")
		db.Exec("DELETE FROM refresh_sessions WHERE user_id IN (?, ?)", admin.ID, targetOnly.ID)
		db.Exec("DELETE FROM user_shop_relations WHERE user_id IN (?, ?)", admin.ID, targetOnly.ID)
		db.Unscoped().Delete(&domain.User{}, admin.ID)
		db.Unscoped().Delete(&domain.User{}, targetOnly.ID)
		db.Unscoped().Delete(&domain.MeasurementPoint{}, oldPoint.ID)
		db.Unscoped().Delete(&domain.MeasurementPoint{}, newPoint.ID)
		db.Unscoped().Delete(&domain.Shop{}, source.ID)
		db.Unscoped().Delete(&domain.Shop{}, target.ID)
		db.Unscoped().Delete(&domain.Client{}, client.ID)
	}()
	for i, action := range []string{"create_measurement_point", "bind", "replace", "relocate", "unbind"} {
		op := domain.AdminBindingOperation{ID: uuid.New(), OperationID: uuid.New(), IdempotencyKey: "audit-read-" + suffix + action, Operation: action, ScopeKey: "admin-binding:client:" + suffix, ActorID: admin.ID, ScopeSnapshot: json.RawMessage(`{"shop_ids":[]}`), CanonicalRequestHash: make([]byte, 32), ClientID: &client.ID, CommittedResponse: json.RawMessage(`{}`), CommittedAt: &now}
		if err := db.Create(&op).Error; err != nil {
			t.Fatal(err)
		}
		audit := domain.AdminBindingAudit{ID: uuid.New(), OperationID: op.OperationID, RequestIdentity: op.IdempotencyKey, ActorID: admin.ID, ScopeKey: op.ScopeKey, ScopeSnapshot: op.ScopeSnapshot, ClientID: &client.ID, Action: action, OccurredAt: now.Add(time.Duration(i/2) * time.Second), ShopID: &target.ID, OldMeasurementPointID: &oldPoint.ID, NewMeasurementPointID: &newPoint.ID, Reason: "test", Metadata: json.RawMessage(`{}`)}
		if action != "relocate" {
			audit.OldMeasurementPointID, audit.NewMeasurementPointID = nil, nil
		}
		if err := db.Create(&audit).Error; err != nil {
			t.Fatal(err)
		}
	}
	repository := NewAdminBindingAuditHistoryRepository(db)
	page, err := repository.FindAdminBindingAuditHistory(context.Background(), AdminBindingAuditHistoryQuery{UserID: admin.ID, ShopID: target.ID, SessionID: session.ID, Limit: 2})
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if page.Items[0].Action != "unbind" || page.Items[0].ShopName != "Target Shop" {
		t.Fatalf("projection=%+v", page.Items[0])
	}
	second, err := repository.FindAdminBindingAuditHistory(context.Background(), AdminBindingAuditHistoryQuery{UserID: admin.ID, ShopID: target.ID, SessionID: session.ID, Limit: 2, Cursor: page.NextCursor})
	if err != nil || len(second.Items) != 2 {
		t.Fatalf("second page=%+v err=%v", second, err)
	}
	third, err := repository.FindAdminBindingAuditHistory(context.Background(), AdminBindingAuditHistoryQuery{UserID: admin.ID, ShopID: target.ID, SessionID: session.ID, Limit: 2, Cursor: second.NextCursor})
	if err != nil || len(third.Items) != 1 {
		t.Fatalf("same-timestamp third page=%+v err=%v", third, err)
	}
	seen := map[uuid.UUID]bool{}
	for _, item := range append(append(page.Items, second.Items...), third.Items...) {
		if seen[item.ID] {
			t.Fatalf("same-timestamp cursor duplicated audit %s", item.ID)
		}
		seen[item.ID] = true
	}
	if len(seen) != 5 {
		t.Fatalf("same-timestamp cursor skipped rows: %d", len(seen))
	}
	filtered, err := repository.FindAdminBindingAuditHistory(context.Background(), AdminBindingAuditHistoryQuery{UserID: admin.ID, ShopID: target.ID, SessionID: session.ID, Limit: 2, Action: "relocate", MeasurementPointID: &newPoint.ID})
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].OldMeasurementPointName != "Old Point" || filtered.Items[0].NewMeasurementPointName != "New Point" {
		t.Fatalf("filtered page=%+v err=%v", filtered, err)
	}
	if _, err := repository.FindAdminBindingAuditHistory(context.Background(), AdminBindingAuditHistoryQuery{UserID: targetOnly.ID, ShopID: target.ID, SessionID: targetSession.ID, Limit: 50}); err != nil {
		t.Fatal(err)
	}
	// Target-only users must not receive the cross-Shop relocation because the
	// source relation is absent, even though they are authorized to the target.
	targetOnlyPage, err := repository.FindAdminBindingAuditHistory(context.Background(), AdminBindingAuditHistoryQuery{UserID: targetOnly.ID, ShopID: target.ID, SessionID: targetSession.ID, Action: "relocate"})
	if err != nil {
		t.Fatal(err)
	}
	if len(targetOnlyPage.Items) != 0 {
		t.Fatalf("target-only user saw source MP relocation: %+v", targetOnlyPage.Items)
	}
	if err := db.Model(&domain.AdminBindingAudit{}).Where("action = ?", "unbind").Update("reason", "tampered").Error; err == nil {
		t.Fatal("immutable audit update unexpectedly succeeded")
	}
}
