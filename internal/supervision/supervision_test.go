package supervision

import (
	"testing"
)

func TestBuildHSRParse(t *testing.T) {
	src := []byte{0x02, 0x50, 0x50, 0x00, 0x00, 0x42}
	frame := BuildHSR(src, 99, 0) // path 0 (pure HSR)

	f := Parse(frame)
	if f == nil {
		t.Fatal("Parse returned nil")
	}
	if f.Seq != 99 {
		t.Errorf("seq = %d, want 99", f.Seq)
	}
	if f.Path != 0 {
		t.Errorf("path = %d, want 0", f.Path)
	}
	if f.TLVType != HSRLifeCheck {
		t.Errorf("TLVType = %d, want %d (LIFE_CHECK)", f.TLVType, HSRLifeCheck)
	}
	if got := macString(f.MacAddrA[:]); got != macString(src) {
		t.Errorf("MacAddrA = %s, want %s", got, macString(src))
	}
}

func TestBuildHSRPath(t *testing.T) {
	// HSR-PRP coupling: path = (NetId<<1)|LanId; NetId=2, LanId=B(1) -> 5.
	src := []byte{0x02, 0x50, 0x50, 0x00, 0x00, 0x43}
	frame := BuildHSR(src, 1, 5)
	f := Parse(frame)
	if f == nil {
		t.Fatal("Parse returned nil")
	}
	if f.Path != 5 {
		t.Errorf("path = %d, want 5", f.Path)
	}
}

func TestParseRejectsNonSupervision(t *testing.T) {
	// A plain Ethernet frame must not parse as supervision.
	if f := Parse([]byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x88, 0xb8}); f != nil {
		t.Error("non-supervision frame parsed as supervision")
	}
}

func macString(b []byte) string {
	s := ""
	for i, x := range b {
		if i > 0 {
			s += ":"
		}
		s += string([]byte{
			'0' + (x>>4)&0xf,
			'0' + x&0xf,
		})
	}
	return s
}
