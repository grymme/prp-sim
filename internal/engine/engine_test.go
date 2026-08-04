package engine

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestEncodeDecodeRCT verifies that a frame with an RCT trailer round-trips
// and that the wire format matches the Linux kernel's PRP format
// (net/hsr/hsr_main.h): seq(2) | lan_id(4b)<<12 | lsdu(12b) | 0x88FB.
func TestEncodeDecodeRCT(t *testing.T) {
	orig := make([]byte, 60)
	copy(orig, []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66}) // dst
	copy(orig[6:12], []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff})
	orig[12] = 0x08
	orig[13] = 0x00

	encoded := EncodeRCT(orig, 7, 0) // LAN A = 0
	if len(encoded) != len(orig)+RCTLen {
		t.Fatalf("expected %d bytes, got %d", len(orig)+RCTLen, len(encoded))
	}

	// Golden check of the trailer bytes (LSDU = 60+6-14 = 52 = 0x034).
	wantTail := []byte{0x00, 0x07, 0x00, 0x34, 0x88, 0xfb}
	if got := encoded[len(encoded)-RCTLen:]; !bytes.Equal(got, wantTail) {
		t.Fatalf("trailer = % x, want % x", got, wantTail)
	}

	lanID, seq, payload, err := DecodeRCT(encoded)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if lanID != 0 {
		t.Errorf("expected lanID=0, got %d", lanID)
	}
	if seq != 7 {
		t.Errorf("expected seq=7, got %d", seq)
	}
	if !bytes.Equal(payload, orig) {
		t.Error("payload mismatch after round trip")
	}
}

// TestEncodeRCTPadsShortFrames verifies that short frames (e.g. ARP) are
// padded to the minimum Ethernet size BEFORE the RCT is appended, so the
// trailer always sits at the end of the wire frame.
func TestEncodeRCTPadsShortFrames(t *testing.T) {
	// 42-byte payload frame (ARP size) -> must become 60 + 6 = 66 bytes.
	orig := make([]byte, 42)
	orig[12] = 0x08
	orig[13] = 0x06

	encoded := EncodeRCT(orig, 0, 1) // LAN B = 1
	if len(encoded) != 66 {
		t.Fatalf("expected padded frame of 66 bytes, got %d", len(encoded))
	}

	// LSDU for padded frame = 66 - 14 = 52.
	lanID, _, payload, err := DecodeRCT(encoded)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if lanID != 1 {
		t.Errorf("expected lanID=1, got %d", lanID)
	}
	if !bytes.Equal(payload[:42], orig) {
		t.Error("original payload corrupted by padding")
	}
}

// TestEncodeRCTVLAN verifies LSDU math for VLAN-tagged frames (subtract the
// 4-byte tag) and that EtherType detection skips the tag.
func TestEncodeRCTVLAN(t *testing.T) {
	orig := make([]byte, 70)
	orig[12] = 0x81 // 802.1Q
	orig[13] = 0x00
	orig[16] = 0x08 // inner EtherType: IPv4
	orig[17] = 0x00

	encoded := EncodeRCT(orig, 1, 0)
	// 70 bytes is >= VLAN_ETH_ZLEN(64), no padding: lsdu = 70+6-14-4 = 58.
	if len(encoded) != 76 {
		t.Fatalf("expected 76 bytes, got %d", len(encoded))
	}

	lanID, _, payload, err := DecodeRCT(encoded)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if lanID != 0 {
		t.Errorf("expected lanID=0, got %d", lanID)
	}
	if !bytes.Equal(payload, orig) {
		t.Error("payload mismatch")
	}
}

func TestEncodeDecodeRCTSequenceNumber(t *testing.T) {
	orig := make([]byte, 60)
	mac := "test-mac-1"

	ResetSequence(mac)

	// Both LAN copies of the same frame MUST carry the same seq.
	seq := NextSequence(mac)
	encA := EncodeRCT(orig, seq, 0)
	encB := EncodeRCT(orig, seq, 1)

	_, seqA, _, _ := DecodeRCT(encA)
	_, seqB, _, _ := DecodeRCT(encB)
	if seqA != seqB {
		t.Errorf("LAN copies must share seq: A=%d B=%d", seqA, seqB)
	}
	if seqA != 0 {
		t.Errorf("expected first seq=0, got %d", seqA)
	}

	// Next frame gets seq=1.
	seq2 := NextSequence(mac)
	if seq2 != 1 {
		t.Errorf("expected second seq=1, got %d", seq2)
	}
}

func TestGetSrcMAC(t *testing.T) {
	frame := []byte{
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, // dst: broadcast
		0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, // src
		0x08, 0x00, 0x00, 0x00, 0x00, 0x00,
	}
	if mac := GetSrcMAC(frame); mac != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("expected src MAC, got %s", mac)
	}
}

func TestGetEtherType(t *testing.T) {
	plain := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x08, 0x00}
	if et := GetEtherType(plain); et != 0x0800 {
		t.Errorf("plain: expected 0x0800, got %#x", et)
	}

	vlan := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x81, 0x00, 0x00, 0x64, 0x08, 0x06}
	if et := GetEtherType(vlan); et != 0x0806 {
		t.Errorf("vlan: expected 0x0806, got %#x", et)
	}
}

func TestIsPRPFrame(t *testing.T) {
	// Encoded frame must be recognized.
	frame := make([]byte, 60)
	enc := EncodeRCT(frame, 0, 0)
	if !IsPRPFrame(enc) {
		t.Error("encoded frame should be detected as PRP")
	}

	// A random frame that happens to end in bytes that decode to lanID 0/1
	// must NOT be detected without the 0x88FB suffix and matching LSDU.
	rand := make([]byte, 60)
	binary.BigEndian.PutUint16(rand[56:58], 1) // lan_id=0, lsdu=1 (mismatch)
	binary.BigEndian.PutUint16(rand[58:60], 0x1234)
	if IsPRPFrame(rand) {
		t.Error("frame without PRP suffix should not be detected as PRP")
	}
}
