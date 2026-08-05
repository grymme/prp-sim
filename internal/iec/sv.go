package iec

// BuildSVAPDU creates a real IEC 61850-9-2 SV APDU (0x60).
func BuildSVAPDU(smpCnt uint16, confRev uint16, svID string) []byte {
	tlvList := [][]byte{}
	tlvList = append(tlvList, encodeTLV(0x80, []byte{0x01, 0x01, 0x01})) // noASDU = 1
	tlvList = append(tlvList, encodeTLV(0x81, []byte{0x00, 0x00}))       // security = 0x0000

	// ASDU (0xa2) sequence: svID(80), smpCnt(81), confRev(82), smpSynch(83), smpRate(84), sampl(85)
	asduTlv := [][]byte{}
	asduTlv = append(asduTlv, encodeTLV(0x80, String(svID)))
	asduTlv = append(asduTlv, encodeTLV(0x81, Integer16(smpCnt)))
	asduTlv = append(asduTlv, encodeTLV(0x82, Integer16(confRev)))
	asduTlv = append(asduTlv, encodeTLV(0x83, []byte{0x00}))    // smpSynch = 0 (local)
	asduTlv = append(asduTlv, encodeTLV(0x84, Integer16(4800))) // smpRate = 4800 Hz (example)
	asduTlv = append(asduTlv, encodeTLV(0x85, Integer16(0)))    // sampl int32 value

	asduPayload := []byte{}
	for _, tlv := range asduTlv {
		asduPayload = append(asduPayload, tlv...)
	}
	tlvList = append(tlvList, encodeTLV(0xa2, asduPayload))

	payload := []byte{}
	for _, tlv := range tlvList {
		payload = append(payload, tlv...)
	}
	return append([]byte{0x60}, append(encodeLength(len(payload)), payload...)...)
}

// ParseSVAPDU walks SV APDU and returns (smpCnt, ok).
func ParseSVAPDU(apdu []byte) (uint16, bool) {
	if len(apdu) < 2 || apdu[0] != 0x60 {
		return 0, false
	}
	off := 1
	lengthByte := apdu[off]
	off++
	var length int
	if lengthByte&0x80 == 0 {
		length = int(lengthByte)
	} else {
		numBytes := int(lengthByte & 0x7f)
		length = 0
		for i := 0; i < numBytes; i++ {
			length = (length << 8) | int(apdu[off+i])
		}
		off += numBytes
	}
	data := apdu[off : off+length]

	rest := data
	var smpCnt uint16
	smpCntSet := false
	for len(rest) > 0 {
		tag, payload, r, ok := parseTLV(rest)
		if !ok {
			break
		}
		rest = r
		switch tag {
		case 0x81: // smpCnt
			if len(payload) == 2 {
				smpCnt = uint16(payload[0])<<8 | uint16(payload[1])
				smpCntSet = true
			}
		case 0xa2: // ASDU nested; descend to find smpCnt
			asduRest := payload
			for len(asduRest) > 0 {
				t, pl, r2, ok2 := parseTLV(asduRest)
				if !ok2 {
					break
				}
				asduRest = r2
				if t == 0x81 && len(pl) == 2 {
					smpCnt = uint16(pl[0])<<8 | uint16(pl[1])
					smpCntSet = true
				}
			}
		}
	}
	return smpCnt, smpCntSet
}

// SVState tracks current sample count and updates it with wrap.
func SVState(smpCnt uint16) uint16 {
	return (smpCnt + 1) % 4096
}
