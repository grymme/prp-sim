package engine

// EncodeRCT appends a 4-byte RCT trailer to the frame.
func EncodeRCT(frame []byte, lanID, seq int) []byte {
	return append(frame, 0x00, byte(lanID), byte(seq>>8), byte(seq&0xff))
}

// DecodeRCT removes and parses the RCT trailer.
func DecodeRCT(frame []byte) (lanID, seq int, payload []byte) {
	off := len(frame) - 4
	return int(frame[off+1]), int(frame[off+2])<<8 | int(frame[off+3]), frame[:off]
}
