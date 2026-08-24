package httpadapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	applicationauth "power-iot-backend/internal/application/auth"
	applicationmeasurementpointdetail "power-iot-backend/internal/application/measurementpointdetail"
)

type detailHTTPQuery struct {
	result         applicationmeasurementpointdetail.Detail
	calls          int
	userID, shopID uint
	pointID        uuid.UUID
}

func (q *detailHTTPQuery) GetMeasurementPointDetail(_ context.Context, userID, shopID uint, pointID uuid.UUID) (applicationmeasurementpointdetail.Detail, error) {
	q.calls++
	q.userID, q.shopID, q.pointID = userID, shopID, pointID
	return q.result, nil
}

type detailAuthStub struct{}

func (detailAuthStub) AuthenticateAccessToken(context.Context, string) (applicationauth.AuthenticatedIdentity, error) {
	return applicationauth.AuthenticatedIdentity{UserID: 42, SessionID: uuid.New()}, nil
}
func detailHTTPRouter(query MeasurementPointDetailQuery) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	RegisterMeasurementPointDetailRoute(r, detailAuthStub{}, query)
	return r
}

func TestMeasurementPointDetailResponseShapeAndHiddenTechnicalInfo(t *testing.T) {
	pointID := uuid.New()
	zero := 0.0
	query := &detailHTTPQuery{result: applicationmeasurementpointdetail.Detail{
		Point: applicationmeasurementpointdetail.Point{Name: "Main", Shop: applicationmeasurementpointdetail.Shop{Code: "S7", Name: "Shop"}}, Status: "unbound",
		CurrentPowerW: &zero, Today: applicationmeasurementpointdetail.EnergyWindow{Kwh: &zero},
	}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shops/7/measurement-points/"+pointID.String(), nil)
	req.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	detailHTTPRouter(query).ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"shop", "measurementPoint", "currentPower", "todayEnergy", "monthEnergy", "currentDevice", "assignmentHistory"} {
		if _, ok := payload[field]; !ok {
			t.Fatalf("missing %s: %v", field, payload)
		}
	}
	if _, ok := payload["technicalInfo"]; ok {
		t.Fatalf("normal response leaked technical info: %v", payload)
	}
	if query.calls != 1 || query.userID != 42 || query.shopID != 7 || query.pointID != pointID {
		t.Fatalf("query=%+v", query)
	}
}
