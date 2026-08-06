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
//	path/ver(2B): path(4b)<<12 | HSR version(12b)  — version = 1 (v1)
//	seq(2B)    : supervision sequence number
//	TLV(2B)    : type=20 (LIFE_CHECK_DD), length=6
//	MacAddrA(6): MAC address of the node
//	pad to 60, then a 6-byte RCT trailer per LAN (lan_id 0xA/0xB).
//
// The kernel identifies supervision frames by dst MAC + EtherType and
// validates the RCT's LSDU size against HSR_V1_SUP_LSDUSIZE (52). The
// low 12 bits of the first word carry the supervision version (1 for
// HSRv1/PRPv1), which both the kernel (set_hsr_stag_HSR_ver) and the
// Wireshark dissector (packet-hsr-prp-supervision.c: sup_version) use to
// select the frame layout.

const (
	// SuperMulticastMAC is the PRP/HSR supervision multicast address.
	SuperMulticastMAC = "01:15:4e:00:01:00"
	// LifeCheckDD is the PRP V1 life-check TLV for duplicate discard.
	LifeCheckDD = 20
	// HSRANNOUNCE and HSRLifeCheck are the HSR supervision TLV types
	// (kernel HSR_TLV_ANNOUNCE / HSR_TLV_LIFE_CHECK).
	HSRANNOUNCE  = 22
	HSRLifeCheck = 23
	// RedBoxMACTLV is the PRP V1 redundancy box MAC TLV (type 30).
	RedBoxMACTLV = 30
	// supLSDUSize is HSR_V1_SUP_LSDUSIZE used by the kernel for
	// supervision frames (52 = 60 - 14 + 6 RCT).
	supLSDUSize = 52
	// ethMinLen is the minimum Ethernet frame size (ETH_ZLEN).
	ethMinLen = 60
)

// Frame is a parsed PRP/HSR supervision frame.
type Frame struct {
	SrcMAC    string
	Seq       uint16
	Path      int // 4-bit HSR path (0 for PRP)
	TLVType   byte
	MacAddrA  [6]byte
	IsRedBox  bool
	RedBoxMAC [6]byte // set when TLVType == PRP_TLV_REDBOX_MAC
}

// SupVersion is the supervision frame version carried in the low 12 bits
// of the first tag word (1 = HSRv1/PRPv1, matching the kernel's
// set_hsr_stag_HSR_ver(1) and Wireshark's sup_version check).
const SupVersion = 1

// Build constructs a supervision frame for a node with the given source MAC
// and sequence number, padded and ready for an RCT trailer to be appended.
func Build(srcMAC []byte, seq uint16) []byte {
	payload := make([]byte, 0, ethMinLen)
	// Ethernet header.
	payload = append(payload, 0x01, 0x15, 0x4e, 0x00, 0x01, 0x00) // dst
	payload = append(payload, srcMAC...)                          // src
	payload = append(payload, 0x88, 0xfb)                         // EtherType

	// Supervision tag: path(4b)<<12 | version(12b) + seq(2B).
	tag := make([]byte, 4)
	binary.BigEndian.PutUint16(tag[0:2], SupVersion)
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
// The wire lan_id nibble uses 0xA (LAN A) / 0xB (LAN B), like data frames
// and as the kernel/Wireshark expect.
func BuildRCTed(srcMAC []byte, seq uint16, lanID int) []byte {
	frame := Build(srcMAC, seq)
	rct := make([]byte, engine.RCTLen)
	binary.BigEndian.PutUint16(rct[0:2], seq)
	wireLanID := uint16(lanID&0x0F) | 0xA
	packed := (wireLanID << 12) | supLSDUSize
	binary.BigEndian.PutUint16(rct[2:4], packed)
	binary.BigEndian.PutUint16(rct[4:6], engine.PRPSuffix)
	return append(frame, rct...)
}

// Parse validates and extracts fields from a received supervision frame.
// Returns nil if the frame is not a valid PRP/HSR supervision frame.
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
		Path:   int(binary.BigEndian.Uint16(frame[14:16]) >> 12),
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

// BuildHSR constructs an HSR ring supervision frame. The path field is
// the 4-bit PathId (0 in pure HSR; (NetId<<1)|LanId for HSR-PRP coupling),
// carried in the top 4 bits of the first tag word like a data frame.
// The low 12 bits carry the supervision version (SupVersion = 1), as the
// kernel sets via set_hsr_stag_HSR_ver and Wireshark checks.
// The frame is NOT RCT-tagged — ring supervision is identified by the
// 0x88FB EtherType and the HSR tag layout.
//
// Layout (matches the kernel hsr_sup_tag):
//
//	dst 01:15:4e:00:01:00, src MAC, EtherType 0x88FB
//	path/ver(2B: path<<12 | SupVersion) + seq(2B)
//	TLV type (22 ANNOUNCE | 23 LIFE_CHECK) + length 6
//	MacAddrA (6B)
//	pad to 60
func BuildHSR(srcMAC []byte, seq uint16, path int) []byte {
	payload := make([]byte, 0, ethMinLen)
	payload = append(payload, 0x01, 0x15, 0x4e, 0x00, 0x01, 0x00) // dst
	payload = append(payload, srcMAC...)                          // src
	payload = append(payload, 0x88, 0xfb)                         // EtherType

	// path (top 4 bits) | version (low 12 bits) + seq.
	tag := make([]byte, 4)
	binary.BigEndian.PutUint16(tag[0:2], uint16(path&0x0F)<<12|SupVersion)
	binary.BigEndian.PutUint16(tag[2:4], seq)
	payload = append(payload, tag...)
	// TLV: LIFE_CHECK, length 6.
	payload = append(payload, HSRLifeCheck, 6)
	// MacAddressA.
	payload = append(payload, srcMAC...)

	if len(payload) < ethMinLen {
		payload = append(payload, make([]byte, ethMinLen-len(payload))...)
	}
	return payload
}
