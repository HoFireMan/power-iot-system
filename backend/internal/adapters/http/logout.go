package httpadapter

import (
	"context"
	"net/http"
	"strconv"

	applicationauth "power-iot-backend/internal/application/auth"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// LogoutRunner is the only application capability required by the protected
// logout handler. Authentication and revocation remain outside HTTP.
type LogoutRunner interface {
	Logout(context.Context, applicationauth.AuthenticatedIdentity) error
}

type LogoutHandlerConfig struct {
	Runner LogoutRunner
}

// NewLogoutHandler constructs POST /api/v1/auth/logout. B4 middleware must run
// before this handler and establish the minimal identity context.
func NewLogoutHandler(config LogoutHandlerConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, ok := IdentityFromGin(c)
		if !ok || config.Runner == nil {
			WritePublicError(c, ErrUnauthorized)
			return
		}
		userID, err := strconv.ParseUint(identity.UserID, 10, 64)
		if err != nil || userID == 0 || uint64(uint(userID)) != userID {
			WritePublicError(c, ErrUnauthorized)
			return
		}
		sessionID, err := uuid.Parse(identity.SessionID)
		if err != nil || sessionID == uuid.Nil {
			WritePublicError(c, ErrUnauthorized)
			return
		}
		if err := config.Runner.Logout(c.Request.Context(), applicationauth.AuthenticatedIdentity{UserID: uint(userID), SessionID: sessionID}); err != nil {
			WritePublicError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// RegisterLogoutRoute adds the protected versioned logout endpoint. The caller
// supplies accepted B4 authentication middleware explicitly at composition.
func RegisterLogoutRoute(router gin.IRouter, authenticator AccessTokenAuthenticator, config LogoutHandlerConfig) {
	if router != nil {
		router.POST("/api/v1/auth/logout", AuthenticationMiddleware(authenticator), NewLogoutHandler(config))
	}
}
