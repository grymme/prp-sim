package engine

import "testing"

func TestEncodeDecodeRCT(t *testing.T) {
	orig := make([]byte, 60)
	encoded := EncodeRCT(orig, "aa:bb:cc:dd:ee:ff", 1)
	if len(encoded) != len(orig)+4 {
		t.Fatalf("expected %d bytes, got %d", len(orig)+4, len(encoded))
	}

	lanID, seq, payload, err := DecodeRCT(encoded)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if lanID != 1 {
		t.Errorf("expected lanID=1, got %d", lanID)
	}
	if seq != 0 {
		t.Errorf("expected seq=0, got %d", seq)
	}
	if len(payload) != 60 {
		t.Fatalf("expected payload of %d bytes, got %d", 60, len(payload))
	}
}

func TestEncodeDecodeRCTSequenceNumber(t *testing.T) {
	orig := make([]byte, 60)
	mac := "test-mac-1"

	// Reset sequence manager for deterministic test
	ResetSequence(mac)

	// First frame should have seq=0
	encoded1 := EncodeRCT(orig, mac, 1)
	_, seq1, _, _ := DecodeRCT(encoded1)
	if seq1 != 0 {
		t.Errorf("expected first seq=0, got %d", seq1)
	}

	// Second frame should have seq=1
	encoded2 := EncodeRCT(orig, mac, 1)
	_, seq2, _, _ := DecodeRCT(encoded2)
	if seq2 != 1 {
		t.Errorf("expected second seq=1, got %d", seq2)
	}

	// Third frame should have seq=2
	encoded3 := EncodeRCT(orig, mac, 1)
	_, seq3, _, _ := DecodeRCT(encoded3)
	if seq3 != 2 {
		t.Errorf("expected third seq=2, got %d", seq3)
	}
}

func TestGetSrcMAC(t *testing.T) {
	// Minimal Ethernet frame: dst(6) + src(6) + etype(2) + payload
	frame := []byte{
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, // dst MAC: broadcast
		0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, // src MAC
		0x08, 0x00, // EtherType: IPv4
		0x00, 0x00, 0x00, 0x00, // padding
	}

	mac := GetSrcMAC(frame)
	expected := "aa:bb:cc:dd:ee:ff"
	if mac != expected {
		t.Errorf("expected %s, got %s", expected, mac)
	}
}

func TestGetEtherType(t *testing.T) {
	frame := []byte{
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // dst MAC
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // src MAC
		0x88, 0xfb, // EtherType: PRP supervision
	}

	etype := GetEtherType(frame)
	if etype != 0x88fb {
		t.Errorf("expected 0x88fb, got 0x%04x", etype)
	}
}

func TestIsPRPFrame(t *testing.T) {
	// Build a frame with RCT trailer
	frame := make([]byte, 60)
	frame = EncodeRCT(frame, "test-mac", 1)

	// Should be detected as PRP frame
	if !IsPRPFrame(frame) {
		t.Error("encoded frame should be detected as PRP frame")
	}

	// A frame without proper RCT should not be detected
	badFrame := make([]byte, 64)
	if IsPRPFrame(badFrame) {
		t.Error("non-PRP frame should not be detected as PRP")
	}
}
