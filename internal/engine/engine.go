package engine

import (
	"encoding/binary"
	"fmt"
	"sync"
)

// SequenceManager tracks per-source-MAC sequence numbers.
type SequenceManager struct {
	mu   sync.Mutex
	seqs map[string]uint16
}

var seqMgr = &SequenceManager{
	seqs: make(map[string]uint16),
}

// NextSequence returns the next sequence number for a given source MAC.
func NextSequence(srcMAC string) uint16 {
	seqMgr.mu.Lock()
	defer seqMgr.mu.Unlock()
	seq := seqMgr.seqs[srcMAC]
	seqMgr.seqs[srcMAC] = seq + 1
	return seq
}

// ResetSequence resets the sequence counter for a MAC address.
func ResetSequence(srcMAC string) {
	seqMgr.mu.Lock()
	defer seqMgr.mu.Unlock()
	delete(seqMgr.seqs, srcMAC)
}

// EncodeRCT appends a 4-byte RCT trailer to the frame.
// RCT format (IEC 62439-3):
//   Bytes 0-1: Sequence Number (16 bits, big-endian)
//   Byte 2:    LAN ID (4 bits) + High nibble of LSDU size (4 bits)
//   Byte 3:    Low byte of LSDU size (8 bits)
//
// Note: Westermo uses a suffix detection pattern. The last 2 bytes of the
// RCT are checked to identify PRP frames. The standard suffix value is 0x8100
// (same as VLAN tag ethertype) or 0xFACE.
func EncodeRCT(frame []byte, srcMAC string, lanID int) []byte {
	seq := NextSequence(srcMAC)
	lsduSize := uint16(len(frame))
	rct := make([]byte, 4)

	// Sequence number (16 bits, big-endian)
	binary.BigEndian.PutUint16(rct[0:2], seq)

	// LAN ID (4 bits) + LSDU size (12 bits) packed into 2 bytes
	lanID4 := uint16(lanID & 0x0F)
	packed := (lanID4 << 12) | (lsduSize & 0x0FFF)
	binary.BigEndian.PutUint16(rct[2:4], packed)

	return append(frame, rct...)
}

// DecodeRCT removes and parses the RCT trailer.
// Returns lanID, seq, and the payload without the trailer.
func DecodeRCT(frame []byte) (lanID, seq int, payload []byte, err error) {
	if len(frame) < 4 {
		return 0, 0, nil, fmt.Errorf("frame too short for RCT: %d bytes", len(frame))
	}
	off := len(frame) - 4

	// Sequence number (16 bits, big-endian)
	seq = int(binary.BigEndian.Uint16(frame[off : off+2]))

	// LAN ID (4 bits) + LSDU size (12 bits)
	packed := binary.BigEndian.Uint16(frame[off+2 : off+4])
	lanID = int((packed >> 12) & 0x0F)

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

// GetEtherType returns the EtherType from an Ethernet frame.
func GetEtherType(frame []byte) uint16 {
	if len(frame) < 14 {
		return 0
	}
	return binary.BigEndian.Uint16(frame[12:14])
}

// IsSupervisionFrame checks if the frame is a PRP supervision frame (0x88fb).
func IsSupervisionFrame(frame []byte) bool {
	return GetEtherType(frame) == 0x88fb
}

// IsPRPFrame checks if a frame has a valid PRP RCT trailer.
// It attempts to decode the RCT and validates the LAN ID is in valid range (1-2).
func IsPRPFrame(frame []byte) bool {
	if len(frame) < 4 {
		return false
	}
	lanID, _, _, err := DecodeRCT(frame)
	if err != nil {
		return false
	}
	// Valid LAN IDs are 1 (A) and 2 (B)
	return lanID == 1 || lanID == 2
}
