package ledger

import "testing"

func TestAuthorityLockKey(t *testing.T) {
	if got := ExpectedLockKey(); got != -5490493357814026861 {
		t.Fatalf("lock key=%d", got)
	}
	if LockKey(AuthorityLabel) != ExpectedLockKey() {
		t.Fatal("lock key not deterministic")
	}
}
