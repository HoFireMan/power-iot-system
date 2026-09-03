package adminbindingaudit

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"power-iot-backend/internal/adapters/persistence"
)

type historyRepositoryStub struct {
	query persistence.AdminBindingAuditHistoryQuery
	page persistence.AdminBindingAuditHistoryPage
	err error
}
func (s *historyRepositoryStub) FindAdminBindingAuditHistory(_ context.Context, q persistence.AdminBindingAuditHistoryQuery) (persistence.AdminBindingAuditHistoryPage, error) { s.query = q; return s.page, s.err }

func TestHistoryValidatesExactFiltersAndMapsImmutableProjection(t *testing.T) {
	id, point, assignment := uuid.New(), uuid.New(), uuid.New()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	device := uint(7)
	stub := &historyRepositoryStub{page: persistence.AdminBindingAuditHistoryPage{Items: []persistence.AdminBindingAuditHistoryProjection{{ID:id, Action:"relocate", OccurredAt:at, MeasurementPointID:&point, DeviceID:&device, OldAssignmentID:&assignment}}}}
	service := New(stub)
	page, err := service.History(context.Background(), 3, 4, "relocate", point.String(), "7", 50, "", uuid.New())
	if err != nil || len(page.Items) != 1 { t.Fatalf("page=%+v err=%v", page, err) }
	if stub.query.Action != "relocate" || stub.query.MeasurementPointID == nil || *stub.query.DeviceID != 7 || stub.query.Limit != 50 { t.Fatalf("query=%+v", stub.query) }
	if page.Items[0].ID != id || page.Items[0].MeasurementPointID == nil || *page.Items[0].MeasurementPointID != point { t.Fatalf("mapped item=%+v", page.Items[0]) }
	for name, action := range map[string]string{"unknown":"delete", "trimmed":" bind"} {
		t.Run(name, func(t *testing.T) { if _, err := service.History(context.Background(), 3, 4, action, "", "", 50, "", uuid.New()); err != ErrInvalidFilter { t.Fatalf("err=%v", err) } })
	}
	for _, deviceRef := range []string{"0", "-1", "seven", " 7"} {
		if _, err := service.History(context.Background(), 3, 4, "", "", deviceRef, 50, "", uuid.New()); err != ErrInvalidFilter { t.Fatalf("device=%q err=%v", deviceRef, err) }
	}
}
