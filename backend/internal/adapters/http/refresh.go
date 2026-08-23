package httpadapter

import (
	"bytes"
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

// RefreshRequest is the exact wire request for POST /api/v1/auth/refresh.
// Presence is tracked independently from the string value.
type RefreshRequest struct {
	RefreshToken string `json:"refreshToken"`
	present      bool
}

// UnmarshalJSON rejects duplicate properties, unknown properties, and any
// non-string refreshToken without interpreting the token itself.
func (r *RefreshRequest) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return ErrValidation
	}
	seen := make(map[string]struct{})
	var token string
	present := false
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return ErrValidation
		}
		key, ok := keyToken.(string)
		if !ok || key != "refreshToken" {
			return ErrValidation
		}
		if _, duplicate := seen[key]; duplicate {
			return ErrValidation
		}
		seen[key] = struct{}{}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return ErrValidation
		}
		var value *string
		if err := json.Unmarshal(raw, &value); err != nil || value == nil {
			return ErrValidation
		}
		token, present = *value, true
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return ErrValidation
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrValidation
	}
	r.RefreshToken, r.present = token, present
	return nil
}

// RefreshRunner is the source-IP-aware B3 capability required by transport.
type RefreshRunner interface {
	RefreshWithSourceIP(context.Context, string, string) (applicationauth.RefreshResult, error)
}

type timestampedRefreshRunner interface {
	RefreshWithSourceIPWithTimestamps(context.Context, string, string) (applicationauth.RefreshResultWithTimestamps, error)
}

// RefreshHandlerConfig supplies the already-composed B3 runner and a fallback
// clock for narrow transport doubles that do not expose metadata.
type RefreshHandlerConfig struct {
	Runner RefreshRunner
	Now    func() time.Time
}

// NewRefreshHandler constructs POST /api/v1/auth/refresh. HTTP only validates
// the envelope, resolves trusted source IP context, and maps application
// outcomes; all token policy remains in B3/PRE1.
func NewRefreshHandler(config RefreshHandlerConfig) gin.HandlerFunc {
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return func(c *gin.Context) {
		request, err := decodeRefreshRequest(c)
		if err != nil || !request.present {
			WritePublicError(c, ErrValidation)
			return
		}
		if config.Runner == nil {
			WritePublicError(c, applicationauth.ErrInfrastructure)
			return
		}
		sourceIP := ""
		if ip, ok := ClientIPFromGin(c); ok {
			sourceIP = ip.String()
		}
		var result applicationauth.RefreshResult
		var accessExpiresAt, refreshExpiresAt time.Time
		if timestamped, ok := config.Runner.(timestampedRefreshRunner); ok {
			detailed, runErr := timestamped.RefreshWithSourceIPWithTimestamps(c.Request.Context(), request.RefreshToken, sourceIP)
			if runErr != nil {
				writeRefreshError(c, runErr)
				return
			}
			result = detailed.RefreshResult
			accessExpiresAt, refreshExpiresAt = detailed.AccessTokenExpiresAt, detailed.RefreshTokenExpiresAt
		} else {
			result, err = config.Runner.RefreshWithSourceIP(c.Request.Context(), request.RefreshToken, sourceIP)
			if err != nil {
				writeRefreshError(c, err)
				return
			}
			issuedAt := now().UTC()
			accessExpiresAt, refreshExpiresAt = issuedAt.Add(security.AccessTokenTTL), issuedAt.Add(applicationauth.RefreshFamilyTTL)
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

func decodeRefreshRequest(c *gin.Context) (RefreshRequest, error) {
	var request RefreshRequest
	decoder := json.NewDecoder(c.Request.Body)
	if err := decoder.Decode(&request); err != nil {
		return RefreshRequest{}, ErrValidation
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return RefreshRequest{}, ErrValidation
	}
	return request, nil
}

func writeRefreshError(c *gin.Context, err error) {
	if errors.Is(err, ErrInvalidCredentials) || errors.Is(err, applicationauth.ErrInvalidCredentials) || errors.Is(err, ErrUnauthorized) || errors.Is(err, applicationauth.ErrUnauthorized) {
		WritePublicError(c, ErrUnauthorized)
		return
	}
	WritePublicError(c, err)
}

// RegisterRefreshRoute adds exactly the versioned refresh endpoint.
func RegisterRefreshRoute(router gin.IRouter, config RefreshHandlerConfig) {
	if router != nil {
		router.POST("/api/v1/auth/refresh", NewRefreshHandler(config))
	}
}
