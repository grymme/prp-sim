package nodetable

import (
	"testing"
	"time"
)

func TestInsertFind(t *testing.T) {
	tbl := NewTable(256)
	tbl.InsertWithExpiry("aa:bb", 1, 1, 640)
	if !tbl.Find("aa:bb", 1) {
		t.Fatal("expected entry")
	}
	if tbl.Find("aa:bb", 99) {
		t.Fatal("unexpected duplicate")
	}
}

func TestInsertWithExpiry(t *testing.T) {
	tbl := NewTable(256)
	tbl.InsertWithExpiry("cc:dd", 100, 2, 50) // 50ms expiry
	if !tbl.Find("cc:dd", 100) {
		t.Fatal("expected entry immediately after insert")
	}

	time.Sleep(100 * time.Millisecond)

	if tbl.Find("cc:dd", 100) {
		t.Fatal("expected entry to be expired")
	}
}

func TestCleanup(t *testing.T) {
	tbl := NewTable(256)
	// Insert with very short expiry
	tbl.InsertWithExpiry("11:22", 500, 1, 10) // 10ms expiry

	time.Sleep(50 * time.Millisecond)

	removed := tbl.Cleanup()
	if removed == 0 {
		t.Fatal("expected cleanup to remove at least 1 entry")
	}

	if tbl.Find("11:22", 500) {
		t.Fatal("expected entry to be cleaned up")
	}
}

func TestSize(t *testing.T) {
	tbl := NewTable(256)
	tbl.InsertWithExpiry("aa:bb", 7, 0, 640)
	if size := tbl.Size(); size != 1 {
		t.Fatalf("expected size 1, got %d", size)
	}
}

func TestMaxSizeEviction(t *testing.T) {
	tbl := NewTable(2)
	tbl.InsertWithExpiry("m1", 1, 0, 10000)
	tbl.InsertWithExpiry("m2", 1, 0, 10000)
	tbl.InsertWithExpiry("m3", 1, 0, 10000)
	if size := tbl.Size(); size != 2 {
		t.Fatalf("expected size capped at 2, got %d", size)
	}
	// The oldest entry must be gone; the newest two remain.
	if tbl.Find("m1", 1) {
		t.Fatal("expected oldest entry to be evicted")
	}
	if !tbl.Find("m2", 1) || !tbl.Find("m3", 1) {
		t.Fatal("expected newer entries to survive")
	}
}
