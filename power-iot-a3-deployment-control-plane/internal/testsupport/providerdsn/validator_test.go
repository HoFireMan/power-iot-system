package providerdsn

import (
	"os"
	"strings"
	"testing"
)

func TestValidateProviderTestURLRejectsUnsafeEndpointsAndDatabases(t *testing.T) {
	for _, raw := range []string{
		"postgres://user@127.0.0.1:5432/d1l_provider_test_5432",
		"postgres://user@127.0.0.1:55433/d1l_provider_test_55433",
		"postgres://user@127.0.0.1:55434/d1l_provider_test_55434",
		"postgres://user@127.0.0.1:55435,192.0.2.10:55435/d1l_provider_test_fallback_remote",
		"postgres://user@127.0.0.1:55435,127.0.0.1:55436/d1l_provider_test_fallback_local",
		"postgres://user@192.0.2.10:55435/d1l_provider_test_remote",
		"postgres://user@localhost:55435/d1l_provider_test_localhost",
		"postgres://user%zz@127.0.0.1:55435/d1l_provider_test_malformed",
		"postgres://user@127.0.0.1:55435",
		"postgres://user@127.0.0.1:55435/power_iot_db",
		"postgres://user@127.0.0.1:55435/security_schema_integration",
		"postgres://user@127.0.0.1:55435/provider",
	} {
		if err := ValidateProviderTestURL(raw); err == nil {
			t.Errorf("unsafe provider test DSN accepted: %s", raw)
		}
	}

	for _, raw := range []string{
		"postgres://user@127.0.0.1:55435/d1l_provider_test_abc123",
		"postgresql://user:secret@127.0.0.1:55436/d1l_provider_test_def456?sslmode=disable",
	} {
		if err := ValidateProviderTestURL(raw); err != nil {
			t.Errorf("safe provider test DSN rejected: %s: %v", raw, err)
		}
	}
}

func TestValidateProviderTestURLQueryGuard(t *testing.T) {
	base := "postgres://user@127.0.0.1:55435/d1l_provider_test_query"
	for _, raw := range []string{
		base + "?host=192.0.2.10",
		base + "?hostaddr=192.0.2.10",
		base + "?port=5432",
		base + "?port=55433",
		base + "?port=55434",
		base + "?dbname=power_iot_db",
		base + "?database=power_iot_db",
		base + "?user=other",
		base + "?passfile=/tmp/provider-passfile",
		base + "?service=provider",
		base + "?servicefile=/tmp/provider-servicefile",
		base + "?sslkey=/tmp/provider.key",
		base + "?sslcert=/tmp/provider.crt",
		base + "?sslrootcert=/tmp/provider.crt",
		base + "?options=-c%20search_path%3Dpublic",
		base + "?arbitrary=value",
		base + "?HoSt=192.0.2.10",
		base + "?HOSTADDR=192.0.2.10",
		base + "?PoRt=5432",
		base + "?DBNAME=power_iot_db",
		base + "?host=192.0.2.10&host=127.0.0.1",
		base + "?host=&sslmode=disable",
		base + "?hostaddr=&HOSTADDR=192.0.2.10",
		base + "?port=&PORT=55435",
		base + "?dbname=&DBNAME=power_iot_db",
		base + "?sslmode=disable&HOSTADDR=192.0.2.10",
		base + "?SSLMODE=disable",
		base + "?Application_Name=provider-test",
		base + "?sslmode=allow",
		base + "?sslmode=prefer",
		base + "?sslmode=require",
		base + "?sslmode=verify-ca",
		base + "?sslmode=verify-full",
		base + "?%68ost=192.0.2.10",
		base + "?host%61ddr=192.0.2.10",
		base + "?%70ort=5432",
		base + "?%64bname=power_iot_db",
		base + "?sslmode=disable%ZZ",
		base + "?host%ZZ=192.0.2.10",
		base + "?sslmode=disable;host=192.0.2.10",
		base + "?=empty-key",
		base + "?sslmode=disable&sslmode=require",
		base + "?application_name=one&Application_Name=two",
		base + "?sslmode=disable&",
		base + "?sslmode=disable&&application_name=provider-test",
		base + "?sslmode",
		base + "?application_name=",
	} {
		if err := ValidateProviderTestURL(raw); err == nil {
			t.Errorf("unsafe provider test DSN accepted: %s", raw)
		}
	}

	for _, raw := range []string{
		base + "?sslmode=disable",
		base + "?application_name=provider-test",
		base + "?sslmode=disable&application_name=provider-test",
		base + "?application_name=provider%2Dtest",
	} {
		if err := ValidateProviderTestURL(raw); err != nil {
			t.Errorf("safe provider test DSN rejected: %s: %v", raw, err)
		}
	}
}

func TestValidateProviderTestURLRejectsCredentialBearingAndInvalidSSLQueries(t *testing.T) {
	base := "postgres://user@127.0.0.1:55435/d1l_provider_test_credentials"
	for _, tc := range []struct {
		name   string
		raw    string
		secret string
	}{
		{
			name:   "query password",
			raw:    base + "?password=PROVIDER_DSN_SECRET_SENTINEL",
			secret: "PROVIDER_DSN_SECRET_SENTINEL",
		},
		{
			name:   "query password and invalid sslmode",
			raw:    base + "?password=PROVIDER_DSN_SECRET_SENTINEL&sslmode=not-a-mode",
			secret: "PROVIDER_DSN_SECRET_SENTINEL",
		},
		{
			name:   "userinfo password and invalid query",
			raw:    "postgres://user:PROVIDER_DSN_SECRET_SENTINEL@127.0.0.1:55435/d1l_provider_test_credentials?sslmode=not-a-mode",
			secret: "PROVIDER_DSN_SECRET_SENTINEL",
		},
		{
			name: "invalid sslmode",
			raw:  base + "?sslmode=not-a-mode",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateProviderTestURL(tc.raw)
			if err == nil {
				t.Fatal("unsafe provider test DSN accepted")
			}
			if strings.Contains(err.Error(), tc.raw) {
				t.Fatalf("provider test DSN error leaked raw DSN: %v", err)
			}
			if tc.secret != "" && strings.Contains(err.Error(), tc.secret) {
				t.Fatalf("provider test DSN error leaked secret: %v", err)
			}
		})
	}
}

func TestValidateProviderTestURLRejectsPortMatrixAndNonLocalAuthorities(t *testing.T) {
	for _, tc := range []struct {
		port string
		want bool
	}{
		{port: "55435", want: true},
		{port: "55436", want: true},
		{port: "1", want: true},
		{port: "0"},
		{port: "65536"},
		{port: ""},
	} {
		raw := "postgres://user@127.0.0.1:" + tc.port + "/d1l_provider_test_matrix"
		err := ValidateProviderTestURL(raw)
		if (err == nil) != tc.want {
			t.Fatalf("port %q accepted=%v want=%v err=%v", tc.port, err == nil, tc.want, err)
		}
	}
}

func TestValidateProviderTestURLGuardRedactsInput(t *testing.T) {
	raw := "postgres://user:secret@192.0.2.10:55435/d1l_provider_test_redaction"
	err := ValidateProviderTestURL(raw)
	if err == nil || strings.Contains(err.Error(), raw) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("provider test DSN error leaked input: %v", err)
	}
}

func TestValidateProviderTestURLRejectsEnvironmentCollisions(t *testing.T) {
	raw := "postgres://user@127.0.0.1:55435/d1l_provider_test_collision"
	for _, key := range []string{"DATABASE_URL", "TEST_DATABASE_URL", "TEST_MIGRATION_DATABASE_URL"} {
		t.Setenv(key, raw)
		if err := ValidateProviderTestURL(raw); err == nil {
			t.Fatalf("provider test DSN reused %s", key)
		}
		t.Setenv(key, "")
	}
	if got := os.Getenv("DATABASE_URL"); got != "" {
		t.Fatalf("test environment cleanup failed: DATABASE_URL=%q", got)
	}
}
