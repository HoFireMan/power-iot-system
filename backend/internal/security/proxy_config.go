package security

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

const TrustedProxyCIDRsEnv = "TRUSTED_PROXY_CIDRS"

var ErrInvalidTrustedProxyConfiguration = errors.New("invalid trusted proxy configuration")

// LoadTrustedProxyConfigFromEnv loads the explicit trusted-proxy allowlist.
// An unset or blank value intentionally produces the zero-value configuration.
func LoadTrustedProxyConfigFromEnv() (TrustedProxyConfig, error) {
	return LoadTrustedProxyConfigFrom(func(name string) string { return os.Getenv(name) })
}

// LoadTrustedProxyConfigFrom is the testable environment seam for the server's
// trusted-proxy policy. No network is trusted unless it is explicitly listed.
func LoadTrustedProxyConfigFrom(get func(string) string) (TrustedProxyConfig, error) {
	if get == nil {
		return TrustedProxyConfig{}, ErrInvalidTrustedProxyConfiguration
	}
	return ParseTrustedProxyCIDRs(get(TrustedProxyCIDRsEnv))
}

// ParseTrustedProxyCIDRs parses a comma-separated list of explicit CIDRs.
// Empty input means direct-peer-only; an empty list item is malformed.
func ParseTrustedProxyCIDRs(value string) (TrustedProxyConfig, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return TrustedProxyConfig{}, nil
	}
	parts := strings.Split(value, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
		if parts[i] == "" {
			return TrustedProxyConfig{}, fmt.Errorf("%w: empty CIDR at position %d", ErrInvalidTrustedProxyConfiguration, i+1)
		}
	}
	config, err := NewTrustedProxyConfig(parts)
	if err != nil {
		return TrustedProxyConfig{}, fmt.Errorf("%w: %v", ErrInvalidTrustedProxyConfiguration, err)
	}
	for _, network := range config.networks {
		if ones, _ := network.Mask.Size(); ones == 0 {
			return TrustedProxyConfig{}, fmt.Errorf("%w: trust-all CIDR is not allowed", ErrInvalidTrustedProxyConfiguration)
		}
	}
	return config, nil
}
