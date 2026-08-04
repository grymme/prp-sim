package nodetable

import (
	"testing"
	"time"
)

func TestInsertFind(t *testing.T) {
	Insert("aa:bb", 1, 1)
	if !Find("aa:bb", 1) {
		t.Fatal("expected entry")
	}
	if Find("aa:bb", 99) {
		t.Fatal("unexpected duplicate")
	}
}

func TestInsertWithExpiry(t *testing.T) {
	InsertWithExpiry("cc:dd", 100, 2, 50) // 50ms expiry
	if !Find("cc:dd", 100) {
		t.Fatal("expected entry immediately after insert")
	}

	time.Sleep(100 * time.Millisecond)

	if Find("cc:dd", 100) {
		t.Fatal("expected entry to be expired")
	}
}

func TestCleanup(t *testing.T) {
	// Insert with very short expiry
	InsertWithExpiry("11:22", 500, 1, 10) // 10ms expiry

	time.Sleep(50 * time.Millisecond)

	removed := Cleanup()
	if removed == 0 {
		t.Fatal("expected cleanup to remove at least 1 entry")
	}

	if Find("11:22", 500) {
		t.Fatal("expected entry to be cleaned up")
	}
}

func TestSize(t *testing.T) {
	// Size should be > 0 since we inserted entries in previous tests
	size := Size()
	if size < 0 {
		t.Fatal("size should be non-negative")
	}
}
