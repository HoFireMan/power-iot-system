package provider

import "testing"

func TestProviderConfigFailClosed(t *testing.T) {
	for _, v := range []string{"", "not a url", "postgres://127.0.0.1:5432/power_iot_db"} {
		if _, e := LoadConfig(func(string) string { return v }); e == nil {
			t.Errorf("accepted %q", v)
		}
	}
}
func TestProviderURL(t *testing.T) {
	c, e := LoadConfig(func(k string) string {
		if k == "D1L_PROVIDER_DATABASE_URL" {
			return "postgres://db.example/provider"
		}
		return ""
	})
	if e != nil || c.DatabaseURL == "" {
		t.Fatal(e)
	}
}
