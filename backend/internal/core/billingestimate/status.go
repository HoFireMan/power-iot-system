package billingestimate

import "errors"

type Status string

const (
	StatusComplete              Status = "COMPLETE_ESTIMATE"
	StatusPartial               Status = "PARTIAL_DATA_ESTIMATE"
	StatusNoData                Status = "NO_DATA"
	StatusConfigurationRequired Status = "CONFIGURATION_REQUIRED"
	StatusUnsupportedTariff     Status = "UNSUPPORTED_TARIFF"
	StatusUnsupportedPeriod     Status = "UNSUPPORTED_PERIOD"
	StatusRateNotFound          Status = "RATE_NOT_FOUND"
)

type WarningCode string

const (
	WarningPartialMonitoringData       WarningCode = "PARTIAL_MONITORING_DATA"
	WarningBottomDegreeNotModeled      WarningCode = "BOTTOM_DEGREE_NOT_MODELED"
	WarningConflictingTelemetry        WarningCode = "CONFLICTING_TELEMETRY_EXCLUDED"
	WarningAmbiguousAssignment         WarningCode = "AMBIGUOUS_ASSIGNMENT_EXCLUDED"
	WarningLegacyEvidence              WarningCode = "LEGACY_EVIDENCE_EXCLUDED"
	WarningUnattributableEvidence      WarningCode = "UNATTRIBUTABLE_EVIDENCE_EXCLUDED"
	WarningOverlappingEvidenceExcluded WarningCode = "OVERLAPPING_EVIDENCE_EXCLUDED"
)

var (
	ErrEstimateAccess        = errors.New("billing estimate shop access denied")
	ErrConfigurationRequired = errors.New("billing estimate configuration required")
	ErrUnsupportedTariff     = errors.New("billing estimate unsupported tariff")
	ErrUnsupportedPeriod     = errors.New("billing estimate unsupported period")
	ErrRateNotFound          = errors.New("billing estimate rate not found")
	ErrCatalogInvariant      = errors.New("billing estimate catalog invariant failed")
)
