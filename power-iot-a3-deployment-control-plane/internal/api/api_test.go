package api

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
)

func TestTLSConfigRequiresVerifiedClientCertificates(t *testing.T) {
	roots := x509.NewCertPool()
	cfg := TLSConfig(&tls.Config{ClientCAs: roots})
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion=%v", cfg.MinVersion)
	}
	if cfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("ClientAuth=%v", cfg.ClientAuth)
	}
	if cfg.ClientCAs != roots {
		t.Fatal("deployment trust roots were not preserved")
	}
}
