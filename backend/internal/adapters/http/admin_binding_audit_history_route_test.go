package httpadapter

import (
	"context"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	applicationadminaudit "power-iot-backend/internal/application/adminbindingaudit"
)

type routeAuditHistoryStub struct{}

func (routeAuditHistoryStub) History(context.Context, uint, uint, string, string, string, int, string, uuid.UUID) (applicationadminaudit.HistoryPage, error) {
	return applicationadminaudit.HistoryPage{}, nil
}

func TestAdminBindingAuditHistoryRouteIsExactlyOneVersionedGet(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterAdminBindingAuditHistoryRoute(router, routeLogoutStub{}, routeAuditHistoryStub{}, nil)
	routes := router.Routes()
	if len(routes) != 1 || routes[0].Method != "GET" || routes[0].Path != "/api/v1/shops/:shopId/admin/binding-audits" {
		t.Fatalf("routes=%v", routes)
	}
}
