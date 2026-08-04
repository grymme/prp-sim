package supervision

import (
	"encoding/binary"

	"prp-gns3/internal/engine"
)

// PRP supervision frames (IEC 62439-3 / kernel net/hsr).
//
// Layout (untagged, PRP V1):
//
//	Dst MAC    : 01-15-4E-00-01-00 (PRP multicast)
//	Src MAC    : node MAC
//	EtherType  : 0x88FB
//	path(2B)   : 0 (PRP; HSR uses path bits)
//	seq(2B)    : supervision sequence number
//	TLV(2B)    : type=20 (LIFE_CHECK_DD), length=6
//	MacAddrA(6): MAC address of the node
//	pad to 60, then a 6-byte RCT trailer per LAN (lan_id 0 or 1).
//
// The kernel identifies supervision frames by dst MAC + EtherType and
// validates the RCT's LSDU size against HSR_V1_SUP_LSDUSIZE (52).

const (
	// SuperMulticastMAC is the PRP supervision multicast address.
	SuperMulticastMAC = "01:15:4e:00:01:00"
	// LifeCheckDD is the PRP V1 life-check TLV for duplicate discard.
	LifeCheckDD = 20
	// supLSDUSize is HSR_V1_SUP_LSDUSIZE used by the kernel for
	// supervision frames (52 = 60 - 14 + 6 RCT).
	supLSDUSize = 52
	// ethMinLen is the minimum Ethernet frame size (ETH_ZLEN).
	ethMinLen = 60
)

// Frame is a parsed PRP supervision frame.
type Frame struct {
	SrcMAC    string
	Seq       uint16
	TLVType   byte
	MacAddrA  [6]byte
	IsRedBox  bool
	RedBoxMAC [6]byte // set when TLVType == PRP_TLV_REDBOX_MAC
}

// Build constructs a supervision frame for a node with the given source MAC
// and sequence number, padded and ready for an RCT trailer to be appended.
func Build(srcMAC []byte, seq uint16) []byte {
	payload := make([]byte, 0, ethMinLen)
	// Ethernet header.
	payload = append(payload, 0x01, 0x15, 0x4e, 0x00, 0x01, 0x00) // dst
	payload = append(payload, srcMAC...)                          // src
	payload = append(payload, 0x88, 0xfb)                         // EtherType

	// Supervision tag: path(2B, =0 for PRP) + seq(2B).
	tag := make([]byte, 4)
	binary.BigEndian.PutUint16(tag[2:4], seq)
	payload = append(payload, tag...)
	// TLV: LIFE_CHECK_DD, length 6.
	payload = append(payload, LifeCheckDD, 6)
	// MacAddressA.
	payload = append(payload, srcMAC...)

	// Pad to minimum frame size (the 6-byte RCT is appended after this).
	if len(payload) < ethMinLen {
		payload = append(payload, make([]byte, ethMinLen-len(payload))...)
	}
	return payload
}

// BuildRCTed returns the supervision frame with the PRP RCT trailer for the
// given LAN ID (0 = A, 1 = B). The LSDU size matches HSR_V1_SUP_LSDUSIZE.
func BuildRCTed(srcMAC []byte, seq uint16, lanID int) []byte {
	frame := Build(srcMAC, seq)
	rct := make([]byte, engine.RCTLen)
	binary.BigEndian.PutUint16(rct[0:2], seq)
	packed := (uint16(lanID&0x0F) << 12) | supLSDUSize
	binary.BigEndian.PutUint16(rct[2:4], packed)
	binary.BigEndian.PutUint16(rct[4:6], engine.PRPSuffix)
	return append(frame, rct...)
}

// Parse validates and extracts fields from a received supervision frame.
// Returns nil if the frame is not a valid PRP supervision frame.
func Parse(frame []byte) *Frame {
	if len(frame) < 20 {
		return nil
	}
	// Dst MAC must be the PRP supervision multicast.
	if frame[0] != 0x01 || frame[1] != 0x15 || frame[2] != 0x4e ||
		frame[3] != 0x00 || frame[4] != 0x01 || frame[5] != 0x00 {
		return nil
	}
	// EtherType 0x88FB.
	if frame[12] != 0x88 || frame[13] != 0xfb {
		return nil
	}

	f := &Frame{
		SrcMAC: engine.GetSrcMAC(frame),
		Seq:    binary.BigEndian.Uint16(frame[16:18]),
	}
	if len(frame) >= 20 {
		f.TLVType = frame[18]
		copy(f.MacAddrA[:], frame[20:26])
	}
	if f.TLVType == 30 && len(frame) >= 32 { // PRP_TLV_REDBOX_MAC
		f.IsRedBox = true
		copy(f.RedBoxMAC[:], frame[26:32])
	}
	return f
}
