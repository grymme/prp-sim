package engine

import (
	"encoding/binary"
	"fmt"
)

// HSR (High-availability Seamless Redundancy) framing, per IEC 62439-3 and
// the Linux kernel implementation (net/hsr/hsr_main.h,
// include/linux/if_hsr.h).
//
// The HSR tag is 6 bytes, inserted after the EtherType — and after any
// VLAN tag — and before the payload:
//
//	+------------+---------------+----------------+-----------------+
//	| Path (4b)  | LSDU size(12b)| sequence nr(16)| encap proto(16) |
//	+------------+---------------+----------------+-----------------+
//	    bytes 0-1                   bytes 2-3        bytes 4-5
//
//   - path occupies the 4 most significant bits of the first 16-bit word
//     (path << 12), LSDU size the low 12 bits (mask 0x0FFF).
//   - LSDU size = 6 (tag) + payload length.
//   - encap_proto is the EtherType of the encapsulated frame (the original
//     frame's EtherType moved into the tag; the tag itself uses 0x892F).
//
// The EtherType of the tag is 0x892F.

const (
	// HSREtherType is the HSR EtherType (0x892f).
	HSREtherType = 0x892f
	// HSRTagLen is the size of the HSR tag in bytes.
	HSRTagLen = 6
	// HSRV1SupLSDUSize is the fixed LSDU size of HSR supervision frames
	// (kernel HSR_V1_SUP_LSDUSIZE).
	HSRV1SupLSDUSize = 52
)

// EncodeHSR builds an HSR frame body: EtherType 0x892F + 6-byte tag +
// payload. The LSDU size field holds the actual payload length (6 + len),
// as in the kernel; NIC padding (if any) comes after the payload and is
// sliced off on decode using the LSDU field.
//
// path is the 4-bit PathId (0 in pure HSR; (NetId<<1)|LanId for HSR-PRP).
// encapProto is the encapsulated EtherType of the original frame.
func EncodeHSR(payload []byte, path int, seq uint16, encapProto uint16) []byte {
	lsdu := HSRTagLen + len(payload)

	tag := make([]byte, HSRTagLen)
	word := (uint16(path&0x0F) << 12) | (uint16(lsdu) & 0x0FFF)
	binary.BigEndian.PutUint16(tag[0:2], word)
	binary.BigEndian.PutUint16(tag[2:4], seq)
	binary.BigEndian.PutUint16(tag[4:6], encapProto)

	out := make([]byte, 2+HSRTagLen+len(payload))
	binary.BigEndian.PutUint16(out[0:2], HSREtherType)
	copy(out[2:8], tag)
	copy(out[8:], payload)
	return out
}

// DecodeHSR validates and parses a frame carrying an HSR tag. It expects
// the frame to start with a 14-byte Ethernet header (or 18 with one VLAN
// tag), followed by the HSR tag, and returns (path, seq, encapProto,
// payload, error). The payload is sliced by the LSDU size field, so any
// trailing wire padding is stripped.
func DecodeHSR(frame []byte) (path, seq int, encapProto uint16, payload []byte, err error) {
	off, err := hsrTagOffset(frame)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	if len(frame) < off+HSRTagLen {
		return 0, 0, 0, nil, fmt.Errorf("frame too short for HSR tag: %d bytes", len(frame))
	}
	word := binary.BigEndian.Uint16(frame[off : off+2])
	path = int(word >> 12)
	lsdu := int(word & 0x0FFF)
	seq = int(binary.BigEndian.Uint16(frame[off+2 : off+4]))
	encapProto = binary.BigEndian.Uint16(frame[off+4 : off+6])

	// LSDU size = tag + payload (kernel definition). It must not exceed
	// the frame; excess bytes are wire padding.
	if lsdu < HSRTagLen || off+lsdu > len(frame) {
		return 0, 0, 0, nil, fmt.Errorf("HSR LSDU size out of range: %d (frame %d)", lsdu, len(frame))
	}
	return path, seq, encapProto, frame[off+HSRTagLen : off+lsdu], nil
}

// IsHSRFrame reports whether the frame carries an HSR tag (EtherType
// 0x892F after any VLAN tag).
func IsHSRFrame(frame []byte) bool {
	return GetEtherType(frame) == HSREtherType
}

// RewriteHSRPath returns a copy of the frame with the 4-bit PathId field
// replaced. Used when forwarding a frame around the ring (path 0 -> 1).
func RewriteHSRPath(frame []byte, path int) ([]byte, error) {
	off, err := hsrTagOffset(frame)
	if err != nil {
		return nil, err
	}
	if len(frame) < off+HSRTagLen {
		return nil, fmt.Errorf("frame too short for HSR tag: %d bytes", len(frame))
	}
	out := append([]byte(nil), frame...)
	word := binary.BigEndian.Uint16(out[off : off+2])
	word = (word & 0x0FFF) | (uint16(path&0x0F) << 12)
	binary.BigEndian.PutUint16(out[off:off+2], word)
	return out, nil
}

// RewriteHSRPathLane sets only the lane bit (bit 0 of the path nibble)
// of the PathId to the given egress lane (0 = LAN A, 1 = LAN B),
// preserving any coupling NetId bits (path bits 3-1) and the LSDU size
// field (low 12 bits). This mirrors the kernel's hsr_set_path_id: the
// path field identifies the lane the frame travels on, and every ring
// node forwards to the other port with the lane bit set for the egress
// port.
func RewriteHSRPathLane(frame []byte, laneA bool) ([]byte, error) {
	off, err := hsrTagOffset(frame)
	if err != nil {
		return nil, err
	}
	if len(frame) < off+HSRTagLen {
		return nil, fmt.Errorf("frame too short for HSR tag: %d bytes", len(frame))
	}
	out := append([]byte(nil), frame...)
	word := binary.BigEndian.Uint16(out[off : off+2])
	var lane uint16
	if !laneA {
		lane = 1
	}
	word = (word & 0xEFFF) | (lane << 12) // keep NetId bits (path 3-1) + LSDU, set lane bit 0
	binary.BigEndian.PutUint16(out[off:off+2], word)
	return out, nil
}

// StripHSR returns the original (untagged) Ethernet frame carried inside
// an HSR frame: MAC header + [VLAN tag] + encap_proto + payload.
func StripHSR(frame []byte) ([]byte, error) {
	off, err := hsrTagOffset(frame)
	if err != nil {
		return nil, err
	}
	_, _, encap, payload, err := DecodeHSR(frame)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(frame)-HSRTagLen)
	out = append(out, frame[:12]...)
	if off > 14 {
		// VLAN tag present between the MAC header and the HSR tag.
		out = append(out, frame[12:off-2]...)
	}
	out = append(out, byte(encap>>8), byte(encap))
	out = append(out, payload...)
	return out, nil
}

// hsrTagOffset returns the byte offset where the HSR tag starts: after the
// Ethernet header (14) and after any VLAN tag (18 with one tag).
func hsrTagOffset(frame []byte) (int, error) {
	if len(frame) < 14 {
		return 0, fmt.Errorf("frame too short: %d bytes", len(frame))
	}
	off := 12
	et := binary.BigEndian.Uint16(frame[off : off+2])
	if et == 0x8100 || et == 0x88a8 {
		if len(frame) < 18 {
			return 0, fmt.Errorf("frame too short for VLAN-tagged HSR: %d bytes", len(frame))
		}
		off = 16
		et = binary.BigEndian.Uint16(frame[off : off+2])
	}
	if et != HSREtherType {
		return 0, fmt.Errorf("not an HSR frame: ethertype 0x%04x", et)
	}
	return off + 2, nil
}
