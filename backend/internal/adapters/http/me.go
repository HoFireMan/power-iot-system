package httpadapter

import (
	"context"
	"net/http"
	"strconv"

	applicationme "power-iot-backend/internal/application/me"

	"github.com/gin-gonic/gin"
)

// MeQuery is the only application capability used by the protected /me
// handler. Authentication and persistence remain outside HTTP.
type MeQuery interface {
	GetMe(context.Context, uint) (applicationme.Profile, error)
}

type MeHandlerConfig struct {
	Query MeQuery
}

// NewMeHandler constructs GET /api/v1/me. The user ID is taken exclusively
// from the minimal identity established by B4 middleware.
func NewMeHandler(config MeHandlerConfig) gin.HandlerFunc {
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
		profile, err := config.Query.GetMe(c.Request.Context(), uint(userID64))
		if err != nil {
			WritePublicError(c, err)
			return
		}
		c.JSON(http.StatusOK, meResponse{
			ID: profile.ID, Account: profile.Account, Name: profile.Name,
			Email: profile.Email, Phone: profile.Phone, IsAdmin: profile.IsAdmin,
			CurrentShopID: profile.CurrentShopID,
		})
	}
}

type meResponse struct {
	ID            string  `json:"id"`
	Account       string  `json:"account"`
	Name          string  `json:"name"`
	Email         *string `json:"email"`
	Phone         *string `json:"phone"`
	IsAdmin       bool    `json:"isAdmin"`
	CurrentShopID *string `json:"currentShopId"`
}

// RegisterMeRoute adds only the protected, versioned GET /me route.
func RegisterMeRoute(router gin.IRouter, authenticator AccessTokenAuthenticator, query MeQuery) {
	if router != nil {
		router.GET("/api/v1/me", AuthenticationMiddleware(authenticator), NewMeHandler(MeHandlerConfig{Query: query}))
	}
}
