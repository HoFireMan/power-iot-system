package security

import (
	"net"
	"net/http"
	"strings"
)

// TrustedProxyConfig can only be made with explicit CIDRs. The zero value
// trusts no proxy, including loopback and private networks.
type TrustedProxyConfig struct {
	networks []*net.IPNet
}

func NewTrustedProxyConfig(cidrs []string) (TrustedProxyConfig, error) {
	if len(cidrs) == 0 {
		return TrustedProxyConfig{}, nil
	}
	out := TrustedProxyConfig{}
	for _, raw := range cidrs {
		_, network, err := net.ParseCIDR(strings.TrimSpace(raw))
		if err != nil {
			return TrustedProxyConfig{}, err
		}
		out.networks = append(out.networks, network)
	}
	return out, nil
}

func (c TrustedProxyConfig) Configured() bool { return len(c.networks) > 0 }
func (c TrustedProxyConfig) trusted(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, network := range c.networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// ResolveClientIP returns the authoritative direct peer unless that peer is
// explicitly trusted. X-Forwarded-For (or RFC7239 Forwarded) is accepted only
// when every hop between the peer and the selected client is a valid address.
func ResolveClientIP(peer net.IP, headers http.Header, config TrustedProxyConfig) net.IP {
	peer = canonicalIP(peer)
	if peer == nil || !config.trusted(peer) {
		return cloneIP(peer)
	}
	if xff := headers.Get("X-Forwarded-For"); xff != "" {
		chain, ok := parseXFF(xff)
		if ok {
			return resolveChain(peer, chain, config)
		}
		return cloneIP(peer)
	}
	if forwarded := headers.Get("Forwarded"); forwarded != "" {
		chain, ok := parseForwarded(forwarded)
		if ok {
			return resolveChain(peer, chain, config)
		}
	}
	return cloneIP(peer)
}

func resolveChain(peer net.IP, chain []net.IP, config TrustedProxyConfig) net.IP {
	// XFF/Forwarded are ordered client to nearest proxy. Starting at the
	// authoritative peer, walk right-to-left through the declared chain.
	for i := len(chain) - 1; i >= 0; i-- {
		if config.trusted(chain[i]) {
			continue
		}
		return cloneIP(chain[i])
	}
	if len(chain) > 0 {
		return cloneIP(chain[0])
	}
	return cloneIP(peer)
}

func parseXFF(value string) ([]net.IP, bool) {
	parts := strings.Split(value, ",")
	if len(parts) == 0 {
		return nil, false
	}
	out := make([]net.IP, 0, len(parts))
	for _, part := range parts {
		ip := net.ParseIP(strings.TrimSpace(part))
		if ip == nil {
			return nil, false
		}
		out = append(out, canonicalIP(ip))
	}
	return out, true
}

func parseForwarded(value string) ([]net.IP, bool) {
	parts := strings.Split(value, ",")
	out := make([]net.IP, 0, len(parts))
	for _, element := range parts {
		var found string
		for _, field := range strings.Split(element, ";") {
			kv := strings.SplitN(strings.TrimSpace(field), "=", 2)
			if len(kv) == 2 && strings.EqualFold(kv[0], "for") {
				found = strings.Trim(strings.TrimSpace(kv[1]), `"`)
				break
			}
		}
		if found == "" {
			return nil, false
		}
		if strings.HasPrefix(found, "[") && strings.HasSuffix(found, "]") {
			found = found[1 : len(found)-1]
		}
		ip := net.ParseIP(found)
		if ip == nil {
			return nil, false
		}
		out = append(out, canonicalIP(ip))
	}
	return out, len(out) > 0
}

func canonicalIP(ip net.IP) net.IP {
	if ip == nil {
		return nil
	}
	if v4 := ip.To4(); v4 != nil {
		return append(net.IP(nil), v4...)
	}
	if v6 := ip.To16(); v6 != nil {
		return append(net.IP(nil), v6...)
	}
	return nil
}
func cloneIP(ip net.IP) net.IP { return canonicalIP(ip) }
