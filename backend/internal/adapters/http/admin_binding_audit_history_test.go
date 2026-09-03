package httpadapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	applicationadminaudit "power-iot-backend/internal/application/adminbindingaudit"
)

type auditHistoryServiceStub struct {
	called bool
	page   applicationadminaudit.HistoryPage
}

func (s *auditHistoryServiceStub) History(_ context.Context, userID, shopID uint, action, point, device string, limit int, cursor string, session uuid.UUID) (applicationadminaudit.HistoryPage, error) {
	s.called = true
	if userID != 9 || shopID != 4 || action != "bind" || point != "point" || device != "7" || limit != 25 || cursor != "next" || session == uuid.Nil {
		panic("unexpected audit query")
	}
	return s.page, nil
}

func TestAdminBindingAuditHistoryHandlerProjectsPageAndFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &auditHistoryServiceStub{}
	router := gin.New()
	router.GET("/api/v1/shops/:shopId/admin/binding-audits", func(c *gin.Context) {
		SetIdentity(c, Identity{UserID: "9", SessionID: uuid.NewString()})
		NewAdminBindingAuditHistoryHandler(service, nil)(c)
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/shops/4/admin/binding-audits?action=bind&measurementPointId=point&deviceId=7&limit=25&cursor=next", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !service.called {
		t.Fatal("service was not called")
	}
}
