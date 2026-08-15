// Package migrations embeds the provider-owned migration used by the authority.
package migrations

import _ "embed"

// Bootstrap is the sole schema source applied at provider startup.
//
//go:embed 001_d1l_bootstrap_authorizations.sql
var Bootstrap string
