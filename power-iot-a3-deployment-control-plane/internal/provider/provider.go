// Package provider owns the single active authority process and its pinned
// PostgreSQL lock. Readiness is intentionally separate from authorization.
package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"power-iot-a3-deployment-control-plane/internal/ledger"
	"power-iot-a3-deployment-control-plane/internal/store"
)

const AuthorityLockLabel = ledger.AuthorityLabel

func AuthorityLockKey() int64 { return ledger.ExpectedLockKey() }

type Config struct {
	DatabaseURL, HTTPAddr              string
	TLSCertFile, TLSKeyFile, TLSCAFile string
}

func LoadConfig(get func(string) string) (Config, error) {
	u := strings.TrimSpace(get("D1L_PROVIDER_DATABASE_URL"))
	if u == "" {
		return Config{}, errors.New("D1L_PROVIDER_DATABASE_URL is required")
	}
	p, e := url.Parse(u)
	if e != nil || p.Scheme != "postgres" && p.Scheme != "postgresql" || p.Host == "" {
		return Config{}, errors.New("malformed D1L_PROVIDER_DATABASE_URL")
	}
	host := strings.ToLower(p.Hostname())
	if host == "127.0.0.1" || host == "localhost" || strings.Contains(strings.ToLower(p.Path), "power_iot_db") || strings.EqualFold(p.Query().Get("dbname"), "power_iot_db") {
		return Config{}, errors.New("target database is not a provider database")
	}
	cert, key, ca := strings.TrimSpace(get("D1L_PROVIDER_TLS_CERT_FILE")), strings.TrimSpace(get("D1L_PROVIDER_TLS_KEY_FILE")), strings.TrimSpace(get("D1L_PROVIDER_TLS_CA_FILE"))
	if cert == "" || key == "" || ca == "" {
		return Config{}, errors.New("D1L_PROVIDER_TLS_CERT_FILE, D1L_PROVIDER_TLS_KEY_FILE, and D1L_PROVIDER_TLS_CA_FILE are required")
	}
	return Config{DatabaseURL: u, HTTPAddr: strings.TrimSpace(get("D1L_PROVIDER_HTTP_ADDR")), TLSCertFile: cert, TLSKeyFile: key, TLSCAFile: ca}, nil
}

type Authority struct {
	Store   *store.Store
	epoch   atomic.Int64
	enabled atomic.Bool
}

func New(s *store.Store) *Authority { return &Authority{Store: s} }
func (a *Authority) Start(ctx context.Context) error {
	return a.start(ctx, "")
}

// StartWithBootstrap performs first-schema admission only after the pinned
// authority connection owns the singleton lock.
func (a *Authority) StartWithBootstrap(ctx context.Context, bootstrap string) error {
	if strings.TrimSpace(bootstrap) == "" {
		return errors.New("provider bootstrap is required")
	}
	return a.start(ctx, bootstrap)
}

func (a *Authority) start(ctx context.Context, bootstrap string) error {
	if a == nil || a.Store == nil {
		return errors.New("provider store required")
	}
	var e int64
	var err error
	if bootstrap == "" {
		e, err = a.Store.AcquireAuthority(ctx)
	} else {
		e, err = a.Store.AcquireAuthorityWithBootstrap(ctx, bootstrap)
	}
	if err != nil {
		return err
	}
	a.epoch.Store(e)
	a.enabled.Store(true)
	return nil
}
func (a *Authority) Stop() {
	if a != nil {
		a.enabled.Store(false)
		if a.Store != nil {
			a.Store.ReleaseAuthority()
		}
	}
}
func (a *Authority) Ready() bool {
	if a == nil || !a.enabled.Load() || a.Store == nil {
		return false
	}
	if !a.Store.AuthorityHealthy(context.Background()) {
		a.Stop()
		return false
	}
	return true
}
func (a *Authority) Epoch() int64 {
	if a == nil {
		return 0
	}
	return a.epoch.Load()
}
func (a *Authority) RequireMutation() error {
	if !a.Ready() || a.Store == nil || !a.Store.AuthorityHealthy(context.Background()) {
		a.Stop()
		return errors.New("authority is not active")
	}
	return nil
}
func (a *Authority) Monitor(ctx context.Context) {
	if a == nil || a.Store == nil || a.Store.DB == nil {
		return
	}
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			a.Stop()
			return
		case <-t.C:
			if !a.Store.AuthorityHealthy(ctx) {
				a.Stop()
				return
			}
		}
	}
}
func ValidateURL(raw string) error {
	_, e := LoadConfig(func(k string) string {
		if k == "D1L_PROVIDER_DATABASE_URL" {
			return raw
		}
		return ""
	})
	if e != nil {
		return fmt.Errorf("provider config: %w", e)
	}
	return nil
}
