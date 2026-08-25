package billingenergy

import (
	"context"
	"errors"
	"testing"
	"time"

	core "power-iot-backend/internal/core/billingenergy"
)

type repositoryStub struct {
	called bool
	facts  core.Facts
	err    error
}

func (r *repositoryStub) FindBillingEnergy(_ context.Context, _ uint, _ uint, month core.BillingMonth, now func() time.Time) (core.Facts, error) {
	r.called = true
	snapshot := now().UTC()
	period, err := month.Period(snapshot)
	if err != nil {
		return core.Facts{}, err
	}
	r.facts.PeriodStart = period.Start
	r.facts.PeriodEnd = period.End
	r.facts.Cutoff = period.Cutoff
	r.facts.Snapshot = snapshot
	if r.err != nil {
		return core.Facts{}, r.err
	}
	return r.facts, nil
}

func TestServiceValidatesMonthAndDelegatesPeriodAndSnapshot(t *testing.T) {
	repository := &repositoryStub{}
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	service := New(repository, func() time.Time { return now })
	facts, err := service.Find(context.Background(), 7, 9, "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if !repository.called || facts.ShopID != 0 || !facts.Cutoff.Equal(now) || !facts.Snapshot.Equal(now) {
		t.Fatalf("facts=%+v called=%t", facts, repository.called)
	}
	repository.called = false
	if _, err := service.Find(context.Background(), 7, 9, "2026-8"); err == nil || repository.called {
		t.Fatalf("invalid month err=%v called=%t", err, repository.called)
	}
}

func TestServicePreservesRepositoryErrorsAndRequiresScope(t *testing.T) {
	expected := errors.New("repository failed")
	repository := &repositoryStub{err: expected}
	service := New(repository, func() time.Time { return time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC) })
	if _, err := service.Find(context.Background(), 1, 2, "2026-07"); !errors.Is(err, expected) {
		t.Fatalf("err=%v", err)
	}
	for _, input := range [][2]uint{{0, 1}, {1, 0}} {
		if _, err := service.Find(context.Background(), input[0], input[1], "2026-07"); !errors.Is(err, ErrBillingEnergyNotFound) {
			t.Fatalf("scope=%v err=%v", input, err)
		}
	}
}
