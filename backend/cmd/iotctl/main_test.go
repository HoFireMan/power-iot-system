package main

import (
	"strings"
	"testing"
)

func TestSupportedActionUsageIncludesDiagnosticsAlias(t *testing.T) {
	usage := supportedActionUsage()
	for _, action := range []string{"diagnostics", "report_diagnostics"} {
		if !strings.Contains(usage, action) {
			t.Fatalf("supported action usage %q does not contain %q", usage, action)
		}
	}
}
