package engine

import (
	"encoding/binary"
	"testing"
)

// assembleFrame prepends an Ethernet header (dst/src) to a body that
// already starts with its EtherType.
func ethHeader(frame []byte) []byte {
	// 12-byte MAC header: dst(6) src(6), then body at 12 (its EtherType
	// lands at 12:14).
	out := make([]byte, 12+len(frame))
	out[0] = 0x01
	out[1] = 0x0c
	out[2] = 0xcd
	out[4] = 0x01
	out[6] = 0x02
	copy(out[12:], frame)
	return out
}

func TestEncodeHSRGolden(t *testing.T) {
	// Payload of 46 bytes (minimum Ethernet payload) so no padding is
	// added; LSDU = 6 (tag) + 46 = 52.
	payload := make([]byte, 46)
	payload[0] = 0xaa
	body := EncodeHSR(payload, 0, 0x1234)
	frame := ethHeader(body)

	// EtherType at offset 12.
	if got := binary.BigEndian.Uint16(frame[12:14]); got != HSREtherType {
		t.Fatalf("EtherType = 0x%04x, want 0x%04x", got, HSREtherType)
	}
	// Tag: word = path<<12 | lsdu (52), seq = 0x1234. Tag starts at 14
	// (after EtherType at 12:14); payload at 20.
	word := binary.BigEndian.Uint16(frame[14:16])
	if path := word >> 12; path != 0 {
		t.Errorf("path = %d, want 0", path)
	}
	if lsdu := word & 0x0FFF; lsdu != 52 {
		t.Errorf("LSDU size = %d, want 52", lsdu)
	}
	if seq := binary.BigEndian.Uint16(frame[16:18]); seq != 0x1234 {
		t.Errorf("seq = 0x%04x, want 0x1234", seq)
	}
	if frame[20] != 0xaa {
		t.Errorf("payload byte 0 = 0x%02x, want 0xaa (tag must be before payload)", frame[20])
	}
}

func TestEncodeHSRPath(t *testing.T) {
	// PathId for HSR-PRP: NetId=3, LanId=B(=1) -> path = 3<<1 | 1 = 7.
	payload := make([]byte, 46)
	body := EncodeHSR(payload, 7, 1)
	frame := ethHeader(body)
	word := binary.BigEndian.Uint16(frame[14:16])
	if path := word >> 12; path != 7 {
		t.Errorf("path = %d, want 7", path)
	}
}

func TestDecodeHSR(t *testing.T) {
	payload := []byte{1, 2, 3, 4, 5}
	body := EncodeHSR(payload, 5, 99)
	frame := ethHeader(body)
	path, seq, got, err := DecodeHSR(frame)
	if err != nil {
		t.Fatalf("DecodeHSR: %v", err)
	}
	if path != 5 || seq != 99 {
		t.Errorf("path/seq = %d/%d, want 5/99", path, seq)
	}
	if len(got) != len(payload) || got[0] != 1 || got[4] != 5 {
		t.Errorf("payload = %v, want %v", got, payload)
	}
}

func TestDecodeHSRShortPayloadPadding(t *testing.T) {
	// Short payload: encoder pads to minimum, decoder must strip the
	// padding and return the original payload.
	payload := []byte{0xde, 0xad}
	body := EncodeHSR(payload, 0, 1)
	frame := ethHeader(body)
	_, _, got, err := DecodeHSR(frame)
	if err != nil {
		t.Fatalf("DecodeHSR: %v", err)
	}
	if len(got) != len(payload) || got[0] != 0xde || got[1] != 0xad {
		t.Errorf("decoded payload = %v, want %v (padding must be stripped)", got, payload)
	}
}

func TestDecodeHSRVLAN(t *testing.T) {
	// HSR tag comes after the VLAN tag: header(14) + vlan(4) + tag(6).
	payload := []byte{9, 8, 7}
	body := EncodeHSR(payload, 1, 2)
	frame := ethHeader(body)
	// Insert a VLAN tag between the header EtherType and the HSR tag.
	vlan := make([]byte, 0, len(frame)+4)
	vlan = append(vlan, frame[:12]...)
	vlan = append(vlan, 0x81, 0x00)    // 802.1Q
	vlan = append(vlan, 0x00, 0x64)    // TCI (vid=100)
	vlan = append(vlan, frame[12:]...) // 0x892F + tag + payload
	path, seq, got, err := DecodeHSR(vlan)
	if err != nil {
		t.Fatalf("DecodeHSR (VLAN): %v", err)
	}
	if path != 1 || seq != 2 {
		t.Errorf("path/seq = %d/%d, want 1/2", path, seq)
	}
	if len(got) != len(payload) || got[0] != 9 {
		t.Errorf("payload = %v, want %v", got, payload)
	}
}

func TestDecodeHSRNegative(t *testing.T) {
	// Not an HSR frame.
	plain := ethHeader([]byte{0x88, 0xb8, 1, 2, 3})
	if _, _, _, err := DecodeHSR(plain); err == nil {
		t.Error("expected error for non-HSR frame")
	}
	// Truncated tag.
	trunc := ethHeader(append(EncodeHSR([]byte{1, 2, 3}, 0, 1), 0x00))
	if _, _, _, err := DecodeHSR(trunc); err != nil {
		t.Error("expected truncation to pass offset check (padding)", err)
	}
	// Corrupted LSDU size.
	bad := ethHeader(EncodeHSR([]byte{1, 2, 3}, 0, 1))
	bad[16] ^= 0xff // corrupt seq bytes -> LSDU word unchanged; corrupt word instead
	bad = ethHeader(EncodeHSR([]byte{1, 2, 3}, 0, 1))
	bad[15] ^= 0xff // corrupt low byte of path_and_LSDU_size word
	if _, _, _, err := DecodeHSR(bad); err == nil {
		t.Error("expected error for corrupted LSDU size")
	}
}

func TestIsHSRFrame(t *testing.T) {
	body := EncodeHSR([]byte{1}, 0, 1)
	if !IsHSRFrame(ethHeader(body)) {
		t.Error("IsHSRFrame should be true")
	}
	// A PRP RCT frame must not be an HSR frame.
	prp := EncodeRCT([]byte{1, 2, 3}, 1, 0)
	if IsHSRFrame(prp) {
		t.Error("IsHSRFrame must reject PRP RCT frames")
	}
	if !IsPRPFrame(prp) {
		t.Error("IsPRPFrame should accept its own frame")
	}
}
