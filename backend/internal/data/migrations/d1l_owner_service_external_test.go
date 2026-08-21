package migrations_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"power-iot-backend/internal/data/migrations"
)

func TestD1LOwnerServicePublicProjectionHasNoRawActivationMaterial(t *testing.T) {
	typ := reflect.TypeOf(migrations.D1LLeaseIssueResult{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		if strings.Contains(name, "activation") || strings.Contains(name, "presentation") || strings.Contains(name, "verifier") || strings.Contains(name, "secret") || strings.Contains(name, "nonce") {
			t.Fatalf("public issue projection has bearer-material field %q", typ.Field(i).Name)
		}
	}
	projection := migrations.D1LLeaseIssueResult{Status: "ISSUED", TargetFingerprint: []byte{1}, EvidenceDigest: []byte{2}}
	formatted := strings.ToLower(fmt.Sprintf("%+v", projection))
	for _, forbidden := range []string{"activation", "presentation", "verifier", "secret", "nonce"} {
		if strings.Contains(formatted, forbidden) {
			t.Fatalf("formatted public issue projection contains %q: %s", forbidden, formatted)
		}
	}
	serviceType := reflect.TypeOf((*migrations.D1LOwnerService)(nil))
	for i := 0; i < serviceType.NumMethod(); i++ {
		method := serviceType.Method(i)
		for out := 0; out < method.Type.NumOut(); out++ {
			resultType := method.Type.Out(out)
			if resultType.Kind() != reflect.Struct || !strings.Contains(strings.ToLower(resultType.String()), "d1llease") {
				continue
			}
			for field := 0; field < resultType.NumField(); field++ {
				name := strings.ToLower(resultType.Field(field).Name)
				if strings.Contains(name, "activation") || strings.Contains(name, "presentation") || strings.Contains(name, "verifier") || strings.Contains(name, "secret") || strings.Contains(name, "nonce") {
					t.Fatalf("public method %s returns raw-material field %q in %s", method.Name, resultType.Field(field).Name, resultType)
				}
			}
		}
	}
}
