package httpadapter

import (
	"context"
	"net/http"
	"strconv"

	applicationshops "power-iot-backend/internal/application/shops"

	"github.com/gin-gonic/gin"
)

// ShopsQuery is the only application capability used by the protected shops
// handler. Authentication and persistence remain outside HTTP.
type ShopsQuery interface {
	GetShops(context.Context, uint) (applicationshops.Shops, error)
}

type ShopsHandlerConfig struct {
	Query ShopsQuery
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
				Phone: shop.Phone, IsHead: shop.IsHead,
			})
		}
		c.JSON(http.StatusOK, shopsResponse{Shops: shops, CurrentShopID: result.CurrentShopID})
	}
}

type shopResponse struct {
	ID      string  `json:"id"`
	Code    string  `json:"code"`
	Name    string  `json:"name"`
	Address *string `json:"address"`
	Phone   *string `json:"phone"`
	IsHead  bool    `json:"isHead"`
}

type shopsResponse struct {
	Shops         []shopResponse `json:"shops"`
	CurrentShopID *string        `json:"currentShopId"`
}

// RegisterShopsRoute adds only the protected, versioned GET /shops route.
func RegisterShopsRoute(router gin.IRouter, authenticator AccessTokenAuthenticator, query ShopsQuery) {
	if router != nil {
		router.GET("/api/v1/shops", AuthenticationMiddleware(authenticator), NewShopsHandler(ShopsHandlerConfig{Query: query}))
	}
}
