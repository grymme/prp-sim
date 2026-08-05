// internal/iec — minimal BER (Basic Encoding Rules) encoder/decoder
// for IEC 61850 GOOSE (0x61) and SV (0x60) APDUs. Stdlib only.
package iec

// encodeTag writes a BER tag byte: bits 7-6 = class/universal,
// bit 5 = constructed (1), bits 4-0 = number.
func encodeTag(tag byte) byte {
	return tag // caller provides full byte (e.g. 0x61 = application, constructed, 1)
}

// encodeLength writes BER length: short form (≤127) as single byte,
// long form (>127) as 0x80 | len_bytes followed by big-endian length.
func encodeLength(length int) []byte {
	if length < 0 {
		panic("iec: negative length")
	}
	if length < 128 {
		return []byte{byte(length)}
	}
	b := []byte{}
	v := uint(length)
	var lenBytes int
	tmp := v
	for tmp > 0 {
		lenBytes++
		tmp >>= 8
	}
	b = append(b, byte(0x80|lenBytes))
	for i := lenBytes - 1; i >= 0; i-- {
		b = append(b, byte((v>>(8*i))&0xff))
	}
	return b
}

// encodeTLV produces [tag byte][length bytes][payload...].
func encodeTLV(tag byte, payload []byte) []byte {
	return append(append([]byte{tag}, encodeLength(len(payload))...), payload...)
}

// parseTLV reads one TLV from `data` and returns (tag, payload, rest, ok).
func parseTLV(data []byte) (tag byte, payload []byte, rest []byte, ok bool) {
	if len(data) < 2 {
		return 0, nil, data, false
	}
	tagByte := data[0]
	off := 1
	lengthByte := data[off]
	off++
	var length int
	if lengthByte&0x80 == 0 {
		length = int(lengthByte)
	} else {
		numBytes := int(lengthByte & 0x7f)
		if len(data) < off+numBytes {
			return 0, nil, data, false
		}
		length = 0
		for i := 0; i < numBytes; i++ {
			length = (length << 8) | int(data[off+i])
		}
		off += numBytes
	}
	if len(data) < off+length {
		return 0, nil, data, false
	}
	payload = data[off : off+length]
	rest = data[off+length:]
	return tagByte, payload, rest, true
}

// Boolean payload: 0 = false, non-zero = true (standard 01 01 00 / FF).
func Boolean(b bool) []byte {
	if b {
		return []byte{0x01, 0x01, 0xff}
	}
	return []byte{0x01, 0x01, 0x00}
}

// Integer16 produces a 16-bit big-endian integer payload (BER INTEGER).
func Integer16(v uint16) []byte {
	return []byte{byte(v >> 8), byte(v & 0xff)}
}

// String produces a VisibleString payload (BER OCTET STRING or simple bytes; here plain bytes with length).
func String(s string) []byte {
	return []byte(s)
}
