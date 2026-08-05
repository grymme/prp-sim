package engine

import (
	"encoding/binary"
	"fmt"
	"sync"
)

// PRP RCT layout (IEC 62439-3, as implemented by the Linux kernel in
// net/hsr/hsr_main.h). The RCT is 6 bytes:
//
//	Bytes 0-1: Sequence Number (16 bits, big-endian)
//	Bytes 2-3: LAN ID (4 bits, high) + LSDU size (12 bits, low)
//	Bytes 4-5: PRP suffix, always 0x88FB
//
// The LSDU size stored in the RCT is the length of the whole frame after
// padding and including the RCT itself, minus the 14-byte Ethernet header
// (and minus 4 more bytes per VLAN tag). A receiver identifies a PRP frame
// by checking that the last two bytes equal 0x88FB and that the stored
// LSDU size matches the actual frame length.

const (
	// RCTLen is the length of the PRP Redundancy Control Trailer.
	RCTLen = 6
	// PRPSuffix is the value of the last two bytes of the RCT.
	PRPSuffix = 0x88FB
	// ethMinLen is the minimum Ethernet frame size without FCS (ETH_ZLEN).
	ethMinLen = 60
	// vlanEthMinLen is the minimum size for frames carrying a VLAN tag
	// (VLAN_ETH_ZLEN).
	vlanEthMinLen = 64
)

// SequenceManager tracks per-source-MAC sequence numbers. Each PRP node
// owns its own instance so multiple nodes (and tests) never share state.
type SequenceManager struct {
	mu    sync.Mutex
	start uint16
	seqs  map[string]uint16
}

func NewSequenceManager() *SequenceManager {
	return &SequenceManager{start: 0, seqs: make(map[string]uint16)}
}

// NewSequenceManagerWithStart returns a SequenceManager whose first
// sequence number for each source MAC is start. Randomizing the start
// across node restarts prevents a restarted node from reusing seq 0..k
// inside a peer's entry_forget window (which would wrongly discard those
// fresh frames as duplicates).
func NewSequenceManagerWithStart(start uint16) *SequenceManager {
	return &SequenceManager{start: start, seqs: make(map[string]uint16)}
}

// Next returns the next sequence number for a given source MAC and
// advances the counter. Callers must use the returned value for BOTH LAN
// copies of a frame — PRP requires the same sequence number on LAN A and
// LAN B so receivers can discard the duplicate.
func (s *SequenceManager) Next(srcMAC string) uint16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, ok := s.seqs[srcMAC]
	if !ok {
		seq = s.start
	}
	s.seqs[srcMAC] = seq + 1
	return seq
}

// Reset clears the counter for a MAC address.
func (s *SequenceManager) Reset(srcMAC string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.seqs, srcMAC)
}

// IsVLANFrame reports whether the frame carries an 802.1Q/802.1ad tag.
func IsVLANFrame(frame []byte) bool {
	if len(frame) < 14 {
		return false
	}
	et := binary.BigEndian.Uint16(frame[12:14])
	return et == 0x8100 || et == 0x88a8
}

// GetVLANID returns the 802.1Q VLAN ID of the frame and whether a tag is
// present. Only a single (outer) tag is inspected.
func GetVLANID(frame []byte) (int, bool) {
	if !IsVLANFrame(frame) {
		return 0, false
	}
	// TCI = bytes 14-15; VLAN ID = low 12 bits.
	tci := binary.BigEndian.Uint16(frame[14:16])
	return int(tci & 0x0FFF), true
}

// IsMulticastMAC reports whether the destination MAC has the multicast bit
// set (first octet odd).
func IsMulticastMAC(frame []byte) bool {
	return len(frame) > 0 && frame[0]&0x01 == 1
}

// EncodeRCT pads the frame to the minimum Ethernet size, appends the 6-byte
// PRP RCT trailer and returns the result. seq must be the sequence number
// assigned to this frame (the same for both LAN copies); lanID must be 0
// (LAN A) or 1 (LAN B).
func EncodeRCT(frame []byte, seq uint16, lanID int) []byte {
	vlan := IsVLANFrame(frame)
	minSize := ethMinLen
	if vlan {
		minSize = vlanEthMinLen
	}

	// Pad to minimum frame size so the trailer always sits at the end of
	// the frame on the wire (short frames like ARP would otherwise be
	// padded by the NIC after the trailer, corrupting it).
	padded := make([]byte, max(len(frame), minSize))
	copy(padded, frame)

	// LSDU size = total length including RCT, minus Ethernet header,
	// minus one VLAN tag.
	lsdu := len(padded) + RCTLen - 14
	if vlan {
		lsdu -= 4
	}

	rct := make([]byte, RCTLen)
	binary.BigEndian.PutUint16(rct[0:2], seq)
	packed := (uint16(lanID&0x0F) << 12) | (uint16(lsdu) & 0x0FFF)
	binary.BigEndian.PutUint16(rct[2:4], packed)
	binary.BigEndian.PutUint16(rct[4:6], PRPSuffix)

	return append(padded, rct...)
}

// DecodeRCT validates the RCT trailer of a received frame and returns the
// LAN ID, the sequence number and the original frame without the trailer.
// The frame must end with the 0x88FB suffix and the stored LSDU size must
// match the actual frame length, as required by the kernel.
func DecodeRCT(frame []byte) (lanID, seq int, payload []byte, err error) {
	if len(frame) < 14+RCTLen {
		return 0, 0, nil, fmt.Errorf("frame too short for RCT: %d bytes", len(frame))
	}
	off := len(frame) - RCTLen

	// The PRP suffix must be present.
	if binary.BigEndian.Uint16(frame[off+4:off+6]) != PRPSuffix {
		return 0, 0, nil, fmt.Errorf("missing PRP suffix 0x%04x", PRPSuffix)
	}

	seq = int(binary.BigEndian.Uint16(frame[off : off+2]))
	packed := binary.BigEndian.Uint16(frame[off+2 : off+4])
	lanID = int((packed >> 12) & 0x0F)
	storedLSDU := int(packed & 0x0FFF)

	// LSDU size must match: total length minus Ethernet header (and VLAN
	// tag if present), i.e. len(frame) - 14 or len(frame) - 18.
	expected := len(frame) - 14
	if IsVLANFrame(frame) {
		expected -= 4
	}
	if storedLSDU != expected {
		return 0, 0, nil, fmt.Errorf("LSDU size mismatch: stored %d, actual %d", storedLSDU, expected)
	}

	return lanID, seq, frame[:off], nil
}

// GetSrcMAC returns the source MAC address from an Ethernet frame.
func GetSrcMAC(frame []byte) string {
	if len(frame) < 12 {
		return ""
	}
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		frame[6], frame[7], frame[8], frame[9], frame[10], frame[11])
}

// GetDstMAC returns the destination MAC address from an Ethernet frame.
func GetDstMAC(frame []byte) string {
	if len(frame) < 6 {
		return ""
	}
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		frame[0], frame[1], frame[2], frame[3], frame[4], frame[5])
}

// GetEtherType returns the EtherType from an Ethernet frame, skipping VLAN
// tags (802.1Q/802.1ad) so tagged frames are classified correctly.
func GetEtherType(frame []byte) uint16 {
	off := 12
	for {
		if len(frame) < off+2 {
			return 0
		}
		et := binary.BigEndian.Uint16(frame[off : off+2])
		if et == 0x8100 || et == 0x88a8 {
			// Skip the 4-byte VLAN header.
			off += 4
			if len(frame) < off+2 {
				return 0
			}
			continue
		}
		return et
	}
}

// IsSupervisionFrame checks if the frame is a PRP supervision frame (0x88fb).
func IsSupervisionFrame(frame []byte) bool {
	return GetEtherType(frame) == 0x88fb
}

// IsPRPFrame checks if a frame carries a valid PRP RCT trailer: it must end
// with the 0x88FB suffix, have a matching LSDU size and a valid LAN ID
// (0 for LAN A, 1 for LAN B).
func IsPRPFrame(frame []byte) bool {
	lanID, _, _, err := DecodeRCT(frame)
	if err != nil {
		return false
	}
	return lanID == 0 || lanID == 1
}
