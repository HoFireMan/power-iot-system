// Package auth contains the provider's certificate identity abstraction. The
// provider never embeds certificates or keys; deployment supplies the TLS
// trust roots and the URI SAN identity extracted by its verifier.
package auth

import (
	"context"
	"crypto/x509"
	"errors"
	"net/url"
	"strings"
)

type Role string

const (
	RoleRunbook  Role = "deployment-runbook"
	RoleRunner   Role = "d1l-runner"
	RoleRecovery Role = "d1l-recovery-runner"
)

type Identity struct {
	Role Role
	URI  string
}
type contextKey struct{}

func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(contextKey{}).(Identity)
	return id, ok
}

func ParseURIIdentity(c *x509.Certificate) (Identity, error) {
	if c == nil {
		return Identity{}, errors.New("client certificate required")
	}
	if len(c.URIs) != 1 {
		return Identity{}, errors.New("exactly one URI identity required")
	}
	raw := c.URIs[0].String()
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "spiffe" {
		return Identity{}, errors.New("invalid URI identity")
	}
	switch raw {
	case "spiffe://power-iot/a3/deployment-runbook":
		return Identity{RoleRunbook, raw}, nil
	case "spiffe://power-iot/a3/d1l-runner":
		return Identity{RoleRunner, raw}, nil
	case "spiffe://power-iot/a3/d1l-recovery-runner":
		return Identity{RoleRecovery, raw}, nil
	default:
		return Identity{}, errors.New("unrecognized URI identity")
	}
}
func (id Identity) Valid() bool {
	return id.URI != "" && strings.HasPrefix(id.URI, "spiffe://power-iot/a3/") && id.Role != ""
}
func Allowed(role Role, action string) bool {
	switch role {
	case RoleRunbook:
		return action == "issue" || action == "resolve-issue" || action == "revoke" || action == "expire"
	case RoleRunner:
		return action == "inspect" || action == "consume" || action == "resolve-owner"
	case RoleRecovery:
		return action == "resolve-recovery"
	}
	return false
}
func Require(ctx context.Context, action string) (Identity, error) {
	id, ok := IdentityFromContext(ctx)
	if !ok || !id.Valid() || !Allowed(id.Role, action) {
		return Identity{}, errors.New("unauthorized")
	}
	return id, nil
}
