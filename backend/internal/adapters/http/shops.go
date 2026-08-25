package httpadapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	applicationshops "power-iot-backend/internal/application/shops"

	"github.com/gin-gonic/gin"
)

// ShopsQuery is the only application capability used by the protected shops
// handler. Authentication and persistence remain outside HTTP.
type ShopsQuery interface {
	GetShops(context.Context, uint) (applicationshops.Shops, error)
}

type ShopsHandlerConfig struct {
	Query    ShopsQuery
	Mutation applicationshops.TariffMutation
}

// NewShopsHandler constructs GET /api/v1/shops. User identity comes
// exclusively from the B4 middleware; no request-supplied shop ID is read.
func NewShopsHandler(config ShopsHandlerConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, ok := IdentityFromGin(c)
		if !ok || config.Query == nil {
			WritePublicError(c, ErrUnauthorized)
			return
		}
		userID64, err := strconv.ParseUint(identity.UserID, 10, 64)
		if err != nil || userID64 == 0 || uint64(uint(userID64)) != userID64 {
			WritePublicError(c, ErrUnauthorized)
			return
		}
		result, err := config.Query.GetShops(c.Request.Context(), uint(userID64))
		if err != nil {
			WritePublicError(c, err)
			return
		}
		shops := make([]shopResponse, 0, len(result.Shops))
		for _, shop := range result.Shops {
			shops = append(shops, shopResponse{
				ID: shop.ID, Code: shop.Code, Name: shop.Name, Address: shop.Address,
				Phone: shop.Phone, IsHead: shop.IsHead, ElectricityTariff: shop.ElectricityTariff,
			})
		}
		c.JSON(http.StatusOK, shopsResponse{Shops: shops, CurrentShopID: result.CurrentShopID})
	}
}

type shopResponse struct {
	ID                string  `json:"id"`
	Code              string  `json:"code"`
	Name              string  `json:"name"`
	Address           *string `json:"address"`
	Phone             *string `json:"phone"`
	IsHead            bool    `json:"isHead"`
	ElectricityTariff *string `json:"tariff,omitempty"`
}

type shopsResponse struct {
	Shops         []shopResponse `json:"shops"`
	CurrentShopID *string        `json:"currentShopId"`
}

// NewShopTariffHandler updates only the explicit tariff classification. The
// application capability performs the relational admin/Shop authorization.
func NewShopTariffHandler(mutation applicationshops.TariffMutation) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, ok := IdentityFromGin(c)
		if !ok || mutation == nil {
			WritePublicError(c, ErrUnauthorized)
			return
		}
		actorID, err := parseExternalID(identity.UserID)
		if err != nil {
			WritePublicError(c, ErrUnauthorized)
			return
		}
		shopID, err := parseExternalID(c.Param("shopId"))
		if err != nil {
			WritePublicError(c, ErrValidation)
			return
		}
		var body struct {
			Tariff string `json:"tariff"`
		}
		decoder := json.NewDecoder(c.Request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil || strings.TrimSpace(body.Tariff) != body.Tariff || !applicationshops.ValidTariff(body.Tariff) {
			WritePublicError(c, ErrValidation)
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			WritePublicError(c, ErrValidation)
			return
		}
		if err := applicationshops.UpdateTariff(c.Request.Context(), mutation, actorID, shopID, body.Tariff); err != nil {
			WritePublicError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// RegisterShopsRoute adds the protected shops read and narrow tariff mutation.
func RegisterShopsRoute(router gin.IRouter, authenticator AccessTokenAuthenticator, query ShopsQuery, mutation ...applicationshops.TariffMutation) {
	if router != nil {
		router.GET("/api/v1/shops", AuthenticationMiddleware(authenticator), NewShopsHandler(ShopsHandlerConfig{Query: query}))
		if len(mutation) > 0 {
			router.PATCH("/api/v1/shops/:shopId", AuthenticationMiddleware(authenticator), NewShopTariffHandler(mutation[0]))
		}
	}
}
