package shops

import (
	"context"
	"errors"
	"testing"

	"power-iot-backend/internal/adapters/persistence"
)

type queryStub struct {
	shops   []persistence.ShopProjection
	current *uint
	err     error
	calls   int
	userID  uint
}

func (s *queryStub) FindAuthorizedShops(_ context.Context, userID uint) ([]persistence.ShopProjection, *uint, error) {
	s.calls++
	s.userID = userID
	return s.shops, s.current, s.err
}

func TestGetShopsMapsAuthorizedProjectionAndKeepsOrder(t *testing.T) {
	address := "A"
	current := uint(22)
	repository := &queryStub{current: &current, shops: []persistence.ShopProjection{
		{ID: 11, Code: "one", Name: "One", Address: &address, IsHead: true},
		{ID: 22, Code: "two", Name: "Two"},
	}}
	result, err := New(repository).GetShops(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Shops) != 2 || result.Shops[0].ID != "11" || result.Shops[1].ID != "22" || result.CurrentShopID == nil || *result.CurrentShopID != "22" {
		t.Fatalf("result=%+v", result)
	}
	if result.Shops[0].Address == nil || *result.Shops[0].Address != address || result.Shops[1].Phone != nil || !result.Shops[0].IsHead {
		t.Fatalf("nullable/safe mapping=%+v", result.Shops)
	}
	if repository.calls != 1 || repository.userID != 7 {
		t.Fatalf("calls/user=%d/%d", repository.calls, repository.userID)
	}
}

func TestGetShopsRejectsCurrentShopOutsideAuthorizedSet(t *testing.T) {
	current := uint(99)
	result, err := New(&queryStub{current: &current, shops: []persistence.ShopProjection{{ID: 1}}}).GetShops(context.Background(), 7)
	if err != nil || len(result.Shops) != 1 || result.CurrentShopID != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestGetShopsEmptySetIsEmptyAndNull(t *testing.T) {
	result, err := New(&queryStub{}).GetShops(context.Background(), 7)
	if err != nil || result.Shops == nil || len(result.Shops) != 0 || result.CurrentShopID != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestGetShopsPassesPersistenceFailuresWithoutChangingThem(t *testing.T) {
	want := errors.New("db failure")
	_, err := New(&queryStub{err: want}).GetShops(context.Background(), 7)
	if !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
}
