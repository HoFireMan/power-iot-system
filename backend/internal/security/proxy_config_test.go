package security

import (
	"errors"
	"net"
	"net/http"
	"testing"
)

func TestTrustedProxyConfigEnvironmentDefaultsToDirectPeerOnly(t *testing.T) {
	config, err := LoadTrustedProxyConfigFrom(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if config.Configured() {
		t.Fatal("unset configuration trusted a proxy")
	}
	peer := net.ParseIP("127.0.0.1")
	got := ResolveClientIP(peer, http.Header{"X-Forwarded-For": []string{"198.51.100.5"}}, config)
	if !got.Equal(peer) {
		t.Fatalf("unset configuration accepted forwarded client %v", got)
	}
}

func TestTrustedProxyConfigMalformedCIDRFailsClosed(t *testing.T) {
	for _, value := range []string{"not-a-cidr", "203.0.113.0/24,", "203.0.113.0/33", "0.0.0.0/0", "::/0"} {
		t.Run(value, func(t *testing.T) {
			config, err := ParseTrustedProxyCIDRs(value)
			if !errors.Is(err, ErrInvalidTrustedProxyConfiguration) {
				t.Fatalf("error=%v, want invalid trusted proxy configuration", err)
			}
			if config.Configured() {
				t.Fatal("malformed configuration returned trusted networks")
			}
		})
	}
}

func TestTrustedProxyConfigExplicitCIDRsResolveOnlyValidChain(t *testing.T) {
	config, err := LoadTrustedProxyConfigFrom(func(name string) string {
		if name == TrustedProxyCIDRsEnv {
			return "203.0.113.0/24, 192.0.2.0/24"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	peer := net.ParseIP("203.0.113.9")
	valid := ResolveClientIP(peer, http.Header{"X-Forwarded-For": []string{"198.51.100.5, 192.0.2.9, 203.0.113.8"}}, config)
	if !valid.Equal(net.ParseIP("198.51.100.5")) {
		t.Fatalf("valid trusted chain resolved to %v", valid)
	}
	boundary := ResolveClientIP(peer, http.Header{"X-Forwarded-For": []string{"198.51.100.5, 198.18.0.7, 203.0.113.8"}}, config)
	if !boundary.Equal(net.ParseIP("198.18.0.7")) {
		t.Fatalf("untrusted chain boundary resolved to %v", boundary)
	}
}
