package me

import (
	"context"
	"errors"
	"testing"

	"power-iot-backend/internal/adapters/persistence"
	applicationauth "power-iot-backend/internal/application/auth"
)

type profileRepositoryStub struct {
	projection persistence.UserProfileProjection
	err        error
	calls      int
}

func (s *profileRepositoryStub) FindUserProfile(context.Context, uint) (persistence.UserProfileProjection, error) {
	s.calls++
	return s.projection, s.err
}

func TestGetMeMapsSafeProjectionAndStringIDs(t *testing.T) {
	email, phone, shop := "user@example.test", "+886", uint(23)
	repository := &profileRepositoryStub{projection: persistence.UserProfileProjection{
		ID: 7, Account: "alice", Name: "Alice", Email: &email, Phone: &phone,
		IsAdmin: true, CurrentShopID: &shop,
	}}
	profile, err := New(repository).GetMe(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != "7" || profile.CurrentShopID == nil || *profile.CurrentShopID != "23" || profile.Email == nil || *profile.Email != email || !profile.IsAdmin {
		t.Fatalf("profile=%+v", profile)
	}
	if repository.calls != 1 {
		t.Fatalf("repository calls=%d", repository.calls)
	}
}

func TestGetMePreservesNullOptionalFields(t *testing.T) {
	profile, err := New(&profileRepositoryStub{projection: persistence.UserProfileProjection{ID: 8, Account: "none", Name: "None"}}).GetMe(context.Background(), 8)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Email != nil || profile.Phone != nil || profile.CurrentShopID != nil {
		t.Fatalf("optional fields were not null: %+v", profile)
	}
}

func TestGetMeMapsMissingUserToExistingUnauthorizedOutcome(t *testing.T) {
	profile, err := New(&profileRepositoryStub{err: persistence.ErrUserQueryRecordNotFound}).GetMe(context.Background(), 9)
	if !errors.Is(err, applicationauth.ErrUnauthorized) || profile.ID != "" {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
}
