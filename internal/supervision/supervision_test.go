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

func TestBuildRCTedWireLanID(t *testing.T) {
	src := []byte{0x02, 0x42, 0xeb, 0x2c, 0xb5, 0x00}
	fa := BuildRCTed(src, 107, 0) // LAN A
	fb := BuildRCTed(src, 107, 1) // LAN B
	// Wire lan_id nibble: 0xA for A, 0xB for B.
	if got := fa[len(fa)-4] >> 4; got != 0xA {
		t.Errorf("LAN A wire lan_id = 0x%X, want 0xA", got)
	}
	if got := fb[len(fa)-4] >> 4; got != 0xB {
		t.Errorf("LAN B wire lan_id = 0x%X, want 0xB", got)
	}
	if fa[len(fa)-2] != 0x88 || fa[len(fa)-1] != 0xfb {
		t.Error("supervision RCT missing 0x88fb suffix")
	}
}

// TestSupVersionWire: the low 12 bits of the first supervision word must
// carry SupVersion (1) — Wireshark's packet-hsr-prp-supervision.c reads
// this as sup_version and parses the seq number only when it is > 0.
func TestSupVersionWire(t *testing.T) {
	src := []byte{0x02, 0x50, 0x50, 0x00, 0x00, 0x42}

	// PRP supervision (Build): word = 0x0001.
	f := Build(src, 1)
	word := int(f[14])<<8 | int(f[15])
	if word&0x0FFF != SupVersion {
		t.Errorf("PRP sup version = 0x%X, want 0x%X", word&0x0FFF, SupVersion)
	}

	// HSR supervision (BuildHSR) with path 0: word = 0x0001.
	fh := BuildHSR(src, 1, 0)
	word = int(fh[14])<<8 | int(fh[15])
	if word != SupVersion {
		t.Errorf("HSR path0 sup word = 0x%X, want 0x%X", word, SupVersion)
	}
	// HSR supervision with path 7: word = 0x7001 (path<<12 | version).
	fh7 := BuildHSR(src, 1, 7)
	word = int(fh7[14])<<8 | int(fh7[15])
	if word != 0x7000|SupVersion {
		t.Errorf("HSR path7 sup word = 0x%X, want 0x7001", word)
	}

	// Parse must round-trip the version bits too.
	p := Parse(fh7)
	if p == nil {
		t.Fatal("Parse returned nil for HSR supervision")
	}
	if p.Path != 7 {
		t.Errorf("parsed path = %d, want 7", p.Path)
	}
}
