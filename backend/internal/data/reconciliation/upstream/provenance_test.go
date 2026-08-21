package upstream

import (
	"testing"

	"github.com/google/uuid"
	"power-iot-backend/internal/data/reconciliation/sourceowner"
)

func TestProducerRejectsRawAndCallerFreshness(t *testing.T) {
	binding := Binding{OperationID: uuid.New(), AttemptID: uuid.New(), RouteIntent: D1OwnerIssueRoute}
	if _, err := Produce(sourceowner.Evidence{}, binding, "owner-v1"); err == nil {
		t.Fatal("zero/raw evidence accepted")
	}
	if _, err := Produce(sourceowner.Evidence{}, binding, "owner-v1"); err == nil {
		t.Fatal("caller freshness accepted")
	}
}

func TestProducerRejectsZeroTargetAndEveryNonOwnerRoute(t *testing.T) {
	base := Binding{OperationID: uuid.New(), AttemptID: uuid.New(), TargetFingerprint: [32]byte{1}, RouteIntent: D1OwnerIssueRoute}
	cases := []Binding{
		{OperationID: base.OperationID, AttemptID: base.AttemptID, RouteIntent: D1OwnerIssueRoute},
		{OperationID: base.OperationID, AttemptID: base.AttemptID, TargetFingerprint: base.TargetFingerprint, RouteIntent: "provider"},
		{OperationID: base.OperationID, AttemptID: base.AttemptID, TargetFingerprint: base.TargetFingerprint, RouteIntent: "legacy"},
		{OperationID: base.OperationID, AttemptID: base.AttemptID, TargetFingerprint: base.TargetFingerprint, RouteIntent: "diagnostic"},
		{OperationID: base.OperationID, AttemptID: base.AttemptID, TargetFingerprint: base.TargetFingerprint, RouteIntent: " D1_ISSUE"},
	}
	for _, binding := range cases {
		if _, err := Produce(sourceowner.Evidence{}, binding, "owner-v1"); err == nil {
			t.Fatalf("unapproved binding accepted: %+v", binding)
		}
	}
	for _, ownerVersion := range []string{"", " ", "\t"} {
		if _, err := Produce(sourceowner.Evidence{}, base, ownerVersion); err == nil {
			t.Fatalf("owner version %q accepted", ownerVersion)
		}
	}
}
