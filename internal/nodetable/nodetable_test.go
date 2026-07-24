package nodetable

import "testing"

func TestInsertFind(t *testing.T) {
	Insert("aa:bb", 1, 1)
	if !Find("aa:bb", 1) {
		t.Fatal("expected entry")
	}
	if Find("aa:bb", 99) {
		t.Fatal("unexpected duplicate")
	}
}
