package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	applicationauth "power-iot-backend/internal/application/auth"
	"power-iot-backend/internal/security"

	"github.com/gin-gonic/gin"
)

// LoginRequest is the transport DTO for the public login endpoint. Account and
// password are passed to the application exactly as supplied by the client.
type LoginRequest struct {
	Account  string `json:"account"`
	Password string `json:"password"`

	accountSet  bool
	passwordSet bool
}

// UnmarshalJSON tracks presence separately from the string value so an empty
// supplied value is not confused with an omitted property.
func (r *LoginRequest) UnmarshalJSON(data []byte) error {
	var payload struct {
		Account  *string `json:"account"`
		Password *string `json:"password"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	r.Account, r.Password = "", ""
	r.accountSet, r.passwordSet = payload.Account != nil, payload.Password != nil
	if payload.Account != nil {
		r.Account = *payload.Account
	}
	if payload.Password != nil {
		r.Password = *payload.Password
	}
	return nil
}

// LoginResponse is the complete and deliberately minimal login wire shape.
type LoginResponse struct {
	TokenType             string `json:"tokenType"`
	AccessToken           string `json:"accessToken"`
	AccessTokenExpiresAt  string `json:"accessTokenExpiresAt"`
	RefreshToken          string `json:"refreshToken"`
	RefreshTokenExpiresAt string `json:"refreshTokenExpiresAt"`
}

// LoginRunner is the only application capability required by this handler.
// HTTP does not inspect users, credentials, tokens, or persistence.
type LoginRunner interface {
	Login(context.Context, string, string) (applicationauth.LoginResult, error)
}

// timestampedLoginRunner is an optional composition seam implemented by the
// accepted B3 transaction runner. It supplies timestamps captured by the same
// application clock that signed and persisted the successful login.
type timestampedLoginRunner interface {
	LoginWithTimestamps(context.Context, string, string) (applicationauth.LoginResultWithTimestamps, error)
}

// LoginHandlerConfig supplies the already-composed B1 limiter and fallback
// clock. Production uses the timestamped B3 runner; the fallback is useful for
// narrow transport test doubles and never changes authentication behavior.
type LoginHandlerConfig struct {
	Runner  LoginRunner
	Limiter *security.AbuseLimiter
	Now     func() time.Time
}

// NewLoginHandler constructs POST /api/v1/auth/login. Failed authentication
// records one B1 failure; successful authentication and validation failures do
// not. All credential and limiter denials use the same public error.
func NewLoginHandler(config LoginHandlerConfig) gin.HandlerFunc {
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return func(c *gin.Context) {
		request, err := decodeLoginRequest(c)
		if err != nil {
			WritePublicError(c, ErrValidation)
			return
		}

		ip := ""
		if sourceIP, ok := ClientIPFromGin(c); ok {
			ip = sourceIP.String()
		}
		if config.Limiter != nil && !config.Limiter.AllowLogin(request.Account, ip) {
			WritePublicError(c, applicationauth.ErrInvalidCredentials)
			return
		}
		if config.Runner == nil {
			WritePublicError(c, errors.New("login capability unavailable"))
			return
		}

		var result applicationauth.LoginResult
		var accessExpiresAt, refreshExpiresAt time.Time
		if timestamped, ok := config.Runner.(timestampedLoginRunner); ok {
			detailed, loginErr := timestamped.LoginWithTimestamps(c.Request.Context(), request.Account, request.Password)
			if loginErr != nil {
				if errors.Is(loginErr, applicationauth.ErrInvalidCredentials) && config.Limiter != nil {
					config.Limiter.RecordLoginFailure(request.Account, ip)
				}
				WritePublicError(c, loginErr)
				return
			}
			result = detailed.LoginResult
			accessExpiresAt, refreshExpiresAt = detailed.AccessTokenExpiresAt, detailed.RefreshTokenExpiresAt
		} else {
			loginResult, loginErr := config.Runner.Login(c.Request.Context(), request.Account, request.Password)
			if loginErr != nil {
				if errors.Is(loginErr, applicationauth.ErrInvalidCredentials) && config.Limiter != nil {
					config.Limiter.RecordLoginFailure(request.Account, ip)
				}
				WritePublicError(c, loginErr)
				return
			}
			result = loginResult
			issuedAt := now().UTC()
			accessExpiresAt = issuedAt.Add(security.AccessTokenTTL)
			refreshExpiresAt = issuedAt.Add(applicationauth.RefreshFamilyTTL)
		}
		if result.AccessToken == "" || result.RefreshToken == "" || accessExpiresAt.IsZero() || refreshExpiresAt.IsZero() {
			WritePublicError(c, applicationauth.ErrInfrastructure)
			return
		}
		c.JSON(http.StatusOK, LoginResponse{
			TokenType: "Bearer", AccessToken: result.AccessToken,
			AccessTokenExpiresAt:  accessExpiresAt.UTC().Format(time.RFC3339),
			RefreshToken:          result.RefreshToken,
			RefreshTokenExpiresAt: refreshExpiresAt.UTC().Format(time.RFC3339),
		})
	}
}

// RegisterLoginRoute adds the versioned login route and no aliases.
func RegisterLoginRoute(router gin.IRouter, config LoginHandlerConfig) {
	if router != nil {
		router.POST("/api/v1/auth/login", NewLoginHandler(config))
	}
}

func decodeLoginRequest(c *gin.Context) (LoginRequest, error) {
	var request LoginRequest
	decoder := json.NewDecoder(c.Request.Body)
	if err := decoder.Decode(&request); err != nil {
		return LoginRequest{}, ErrValidation
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return LoginRequest{}, ErrValidation
	}
	if !request.accountSet || !request.passwordSet || len(request.Password) > security.MaxPasswordBytes {
		return LoginRequest{}, ErrValidation
	}
	return request, nil
}
