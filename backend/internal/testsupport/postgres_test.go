package testsupport

import (
	"context"
	"os"
	"testing"
)

func TestValidateSourceRequiresDedicatedEndpointAndDisposableDatabase(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
	}{
		{name: "wrong host", dsn: "postgres://user:secret@localhost:55434/fixture"},
		{name: "wrong port", dsn: "postgres://user:secret@127.0.0.1:5432/fixture"},
		{name: "query host override", dsn: "postgres://user:secret@127.0.0.1:55434/fixture?host=localhost"},
		{name: "power iot", dsn: "postgres://user:secret@127.0.0.1:55434/power_iot"},
		{name: "core", dsn: "postgres://user:secret@127.0.0.1:55434/core"},
		{name: "postgres", dsn: "postgres://user:secret@127.0.0.1:55434/postgres"},
		{name: "template", dsn: "postgres://user:secret@127.0.0.1:55434/template"},
		{name: "baseline", dsn: "postgres://user:secret@127.0.0.1:55434/baseline"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateSource(test.dsn); err == nil {
				t.Fatal("validateSource accepted a non-dedicated or protected database")
			}
		})
	}
}

func TestGeneratedNameIsBoundedAndRecognizable(t *testing.T) {
	name, err := generatedName()
	if err != nil {
		t.Fatal(err)
	}
	if !validGeneratedName(name) || len(name) > 63 {
		t.Fatalf("generated invalid database name %q", name)
	}
	if name == namePrefix+"00000000000000000000000000000000" {
		t.Fatal("generated predictable database name")
	}
}

func TestNewRequiresMigrationCallbackBeforeDatabaseSetup(t *testing.T) {
	_, err := New(context.Background(), "", nil)
	if err == nil {
		t.Fatal("New accepted a nil migration callback")
	}
}

func TestEnvironmentRestorationPreservesUnsetAndExistingValues(t *testing.T) {
	const name = "POWER_IOT_TESTSUPPORT_RESTORE"
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	if err := restoreEnvironment(name, environmentValue{value: "ignored", present: false}); err != nil {
		t.Fatal(err)
	}
	if _, present := os.LookupEnv(name); present {
		t.Fatal("restore recreated an originally unset environment variable")
	}
	if err := restoreEnvironment(name, environmentValue{value: "original", present: true}); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(name); got != "original" {
		t.Fatalf("restored environment value=%q, want original", got)
	}
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
}
