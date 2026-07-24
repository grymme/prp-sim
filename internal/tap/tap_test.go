package tap

import "testing"

func TestCreate(t *testing.T) {
	err := Create("prp0")
	if err != nil {
		t.Fatal(err)
	}
}
