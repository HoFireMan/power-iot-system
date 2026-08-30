package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"power-iot-backend/internal/adapters/persistence"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type sessionOverviewQueryStub struct {
	userID, shopID uint
	sessionID      uuid.UUID
	called         bool
}

func (q *sessionOverviewQueryStub) FindAdminBindingOverview(context.Context, uint, uint) (persistence.AdminBindingOverview, error) {
	return persistence.AdminBindingOverview{}, errors.New("legacy overview seam must not be used by HTTP")
}

func (q *sessionOverviewQueryStub) FindAdminBindingOverviewForSession(_ context.Context, userID, shopID uint, sessionID uuid.UUID) (persistence.AdminBindingOverview, error) {
	q.userID, q.shopID, q.sessionID, q.called = userID, shopID, sessionID, true
	return persistence.AdminBindingOverview{}, nil
}

func TestAdminBindingOverviewHTTPUsesSessionAwareQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	sessionID := uuid.New()
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/device-bindings?shopId=7", nil)
	SetIdentity(c, Identity{UserID: "42", SessionID: sessionID.String()})

	query := &sessionOverviewQueryStub{}
	overviewHandler(query)(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d", recorder.Code, http.StatusOK)
	}
	if !query.called || query.userID != 42 || query.shopID != 7 || query.sessionID != sessionID {
		t.Fatalf("session-aware query received user=%d shop=%d session=%s called=%t", query.userID, query.shopID, query.sessionID, query.called)
	}
}

type legacyOverviewQueryStub struct{}

func (legacyOverviewQueryStub) FindAdminBindingOverview(context.Context, uint, uint) (persistence.AdminBindingOverview, error) {
	return persistence.AdminBindingOverview{}, nil
}

func TestAdminBindingOverviewHTTPRejectsLegacyQuerySeam(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	identity := Identity{UserID: "42", SessionID: uuid.NewString()}
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/device-bindings?shopId=7", nil)
	SetIdentity(c, identity)

	overviewHandler(legacyOverviewQueryStub{})(c)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want %d for unavailable session-aware capability", recorder.Code, http.StatusInternalServerError)
	}
}
