package iec

import (
	"time"
)

// BuildGOOSEAPDU creates a real IEC 61850-8-1 GOOSE PDU (0x61).
// Fields in BER order per Table 14: gocbRef(80), timeAllowedToLive(81),
// datSet(82), goID(83), t(84), stNum(85), sqNum(86), test(87), confRev(88),
// ndsCom(89), numDatSetEntries(8a), allData(ab Boolean).
func BuildGOOSEAPDU(stNum uint16, sqNum uint16, t time.Time, test bool, confRev uint16, gcbRef, datSet, goID string) []byte {
	tlvList := [][]byte{}

	tlvList = append(tlvList, encodeTLV(0x80, String(gcbRef)))
	tlvList = append(tlvList, encodeTLV(0x81, Integer16(2000))) // timeAllowedToLive = 2000 ms
	tlvList = append(tlvList, encodeTLV(0x82, String(datSet)))
	tlvList = append(tlvList, encodeTLV(0x83, String(goID)))
	tlvList = append(tlvList, encodeTLV(0x84, EncodeUtcTime(t)))
	tlvList = append(tlvList, encodeTLV(0x85, Integer16(stNum)))
	tlvList = append(tlvList, encodeTLV(0x86, Integer16(sqNum)))
	tlvList = append(tlvList, encodeTLV(0x87, Boolean(test)))
	tlvList = append(tlvList, encodeTLV(0x88, Integer16(confRev)))
	tlvList = append(tlvList, encodeTLV(0x89, Boolean(false))) // ndsCom false
	tlvList = append(tlvList, encodeTLV(0x8a, []byte{0x01}))   // numDatSetEntries = 1
	tlvList = append(tlvList, encodeTLV(0xab, Boolean(test)))  // allData Boolean toggled with state

	// Assemble PDU: 0x61 (application, constructed, 1) + length + TLVs
	payload := []byte{}
	for _, tlv := range tlvList {
		payload = append(payload, tlv...)
	}
	return append([]byte{0x61}, append(encodeLength(len(payload)), payload...)...)
}

// ParseGOOSEAPDU walks the BER TLVs in the APDU payload and extracts fields.
func ParseGOOSEAPDU(apdu []byte) (stNum uint16, sqNum uint16, t time.Time, allData bool, ok bool) {
	if len(apdu) < 2 || apdu[0] != 0x61 {
		return 0, 0, time.Time{}, false, false
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
	if len(apdu) < off+length {
		return 0, 0, time.Time{}, false, false
	}
	data := apdu[off : off+length]

	// Walk TLVs
	stNumSet := false
	sqNumSet := false
	allDataSet := false
	rest := data
	for len(rest) > 0 {
		tag, payload, r, ok := parseTLV(rest)
		if !ok {
			break
		}
		rest = r
		switch tag {
		case 0x85:
			if len(payload) == 2 {
				stNum = uint16(payload[0])<<8 | uint16(payload[1])
				stNumSet = true
			}
		case 0x86:
			if len(payload) == 2 {
				sqNum = uint16(payload[0])<<8 | uint16(payload[1])
				sqNumSet = true
			}
		case 0x84:
			if len(payload) == 8 {
				t = DecodeUtcTime(payload)
			}
		case 0xab:
			if len(payload) == 3 && payload[0] == 0x01 && payload[1] == 0x01 {
				allData = payload[2] != 0x00
				allDataSet = true
			}
		}
	}
	ok = stNumSet && sqNumSet && allDataSet
	return stNum, sqNum, t, allData, ok
}
