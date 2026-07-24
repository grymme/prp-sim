package engine

import "testing"

func TestEncodeDecodeRCT(t *testing.T) {
	orig := make([]byte, 60)
	encoded := EncodeRCT(orig, 1, 42)
	if len(encoded) != len(orig)+4 {
		t.Fatalf("expected %d bytes, got %d", len(orig)+4, len(encoded))
	}

	lanID, seq, payload := DecodeRCT(encoded)
	if lanID != 1 || seq != 42 || len(payload) != 60 {
		t.Fatalf("decode failed: lanID=%d seq=%d payloadLen=%d", lanID, seq, len(payload))
	}
}
