package provider

import "testing"

func providerEnv(database string) func(string) string {
	return func(k string) string {
		switch k {
		case "D1L_PROVIDER_DATABASE_URL":
			return database
		case "D1L_PROVIDER_TLS_CERT_FILE":
			return "/run/poweriot/secrets/provider.crt"
		case "D1L_PROVIDER_TLS_KEY_FILE":
			return "/run/poweriot/secrets/provider.key"
		case "D1L_PROVIDER_TLS_CA_FILE":
			return "/run/poweriot/secrets/provider-ca.crt"
		default:
			return ""
		}
	}
}

func TestProviderConfigFailClosed(t *testing.T) {
	for _, v := range []string{"", "not a url", "postgres://127.0.0.1:5432/power_iot_db"} {
		if _, e := LoadConfig(providerEnv(v)); e == nil {
			t.Errorf("accepted %q", v)
		}
	}
}

func TestProviderConfigRequiresTLSPaths(t *testing.T) {
	env := providerEnv("postgres://db.example/provider")
	withoutCA := func(k string) string {
		if k == "D1L_PROVIDER_TLS_CA_FILE" {
			return ""
		}
		return env(k)
	}
	if _, err := LoadConfig(withoutCA); err == nil {
		t.Fatal("accepted provider config without TLS CA path")
	}
}

func TestProviderURL(t *testing.T) {
	c, e := LoadConfig(providerEnv("postgres://db.example/provider"))
	if e != nil || c.DatabaseURL == "" {
		t.Fatal(e)
	}
	if c.TLSCertFile == "" || c.TLSKeyFile == "" || c.TLSCAFile == "" {
		t.Fatalf("provider TLS paths missing: %+v", c)
	}
}
