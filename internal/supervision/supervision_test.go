package supervision

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestBuildSupervisionFrame(t *testing.T) {
	srcMAC := []byte{0x02, 0x50, 0x50, 0xaa, 0xbb, 0x01}
	frame := Build(srcMAC, 42)

	if len(frame) != ethMinLen {
		t.Fatalf("expected %d bytes, got %d", ethMinLen, len(frame))
	}
	// Dst MAC: PRP supervision multicast.
	wantDst := []byte{0x01, 0x15, 0x4e, 0x00, 0x01, 0x00}
	if !bytes.Equal(frame[0:6], wantDst) {
		t.Errorf("dst MAC = % x, want % x", frame[0:6], wantDst)
	}
	// Src MAC.
	if !bytes.Equal(frame[6:12], srcMAC) {
		t.Errorf("src MAC = % x, want % x", frame[6:12], srcMAC)
	}
	// EtherType 0x88FB.
	if frame[12] != 0x88 || frame[13] != 0xfb {
		t.Errorf("ethertype = %02x%02x, want 88fb", frame[12], frame[13])
	}
	// Supervision tag: path(2B, =0) at 14-16, seq at 16-18.
	if got := binary.BigEndian.Uint16(frame[16:18]); got != 42 {
		t.Errorf("seq = %d, want 42", got)
	}
	// TLV: type 20, length 6.
	if frame[18] != LifeCheckDD {
		t.Errorf("TLV type = %d, want %d", frame[18], LifeCheckDD)
	}
	if frame[19] != 6 {
		t.Errorf("TLV length = %d, want 6", frame[19])
	}
	// MacAddressA.
	if !bytes.Equal(frame[20:26], srcMAC) {
		t.Errorf("MacAddressA = % x, want % x", frame[20:26], srcMAC)
	}
}

func TestBuildRCTed(t *testing.T) {
	srcMAC := []byte{0x02, 0x50, 0x50, 0xaa, 0xbb, 0x01}

	frameA := BuildRCTed(srcMAC, 7, 0)
	if len(frameA) != ethMinLen+6 {
		t.Fatalf("expected %d bytes, got %d", ethMinLen+6, len(frameA))
	}
	// Trailer: seq(7) | lan_id(0)<<12|52 | 0x88FB.
	wantTail := []byte{0x00, 0x07, 0x00, 0x34, 0x88, 0xfb}
	if got := frameA[len(frameA)-6:]; !bytes.Equal(got, wantTail) {
		t.Errorf("LAN A trailer = % x, want % x", got, wantTail)
	}

	frameB := BuildRCTed(srcMAC, 7, 1)
	wantTailB := []byte{0x00, 0x07, 0x10, 0x34, 0x88, 0xfb}
	if got := frameB[len(frameB)-6:]; !bytes.Equal(got, wantTailB) {
		t.Errorf("LAN B trailer = % x, want % x", got, wantTailB)
	}
}

func TestParse(t *testing.T) {
	srcMAC := []byte{0x02, 0x50, 0x50, 0xaa, 0xbb, 0x01}
	frame := Build(srcMAC, 99)

	f := Parse(frame)
	if f == nil {
		t.Fatal("Parse returned nil for valid supervision frame")
	}
	if f.SrcMAC != "02:50:50:aa:bb:01" {
		t.Errorf("SrcMAC = %s", f.SrcMAC)
	}
	if f.Seq != 99 {
		t.Errorf("Seq = %d, want 99", f.Seq)
	}
	if f.TLVType != LifeCheckDD {
		t.Errorf("TLVType = %d, want %d", f.TLVType, LifeCheckDD)
	}

	// Non-supervision frame must parse to nil.
	if f := Parse(make([]byte, 60)); f != nil {
		t.Error("Parse should reject non-supervision frames")
	}
}
