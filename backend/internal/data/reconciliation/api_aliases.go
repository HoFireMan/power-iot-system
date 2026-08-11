package reconciliation

import (
	"context"
	"errors"
	"time"
)

// Versioned aliases make the v5 boundary explicit at call sites while keeping
// the short names convenient for package-local orchestration.
type SourceFactsV5 = FactSet
type ClientFactV5 = ClientFact
type ShopFactV5 = ShopFact
type DeviceFactV5 = DeviceFact
type MeasurementPointFactV5 = MeasurementPointFact
type DeviceAssignmentFactV5 = DeviceAssignmentFact
type AdminOperationFactV5 = AdminOperationFact
type AdminAuditFactV5 = AdminAuditFact

type V5FreshRecheck interface {
	RecheckV5(context.Context, ExclusiveFence) (FactSet, error)
}

func CollectSourceFactsV5(c ReadOnlyCollector, ctx context.Context, asOf time.Time) (FactSet, error) {
	if c == nil {
		return FactSet{}, errors.New("read-only fact collector is required")
	}
	return c.CollectV5(ctx, asOf)
}
func ClassifyV5(facts FactSet) ([]Decision, error) { return Classify(facts) }
func BuildDeterministicPlan(facts FactSet, artifact *MappingArtifact) (Plan, error) {
	return BuildPlan(facts, artifact)
}
