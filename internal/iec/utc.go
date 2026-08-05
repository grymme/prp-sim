package iec

import "time"

// IEC 61850 UtcTime (8 bytes):
//
//	bytes 0-3: seconds since 1970-01-01 00:00:00 UTC (unsigned, big-endian)
//	bytes 4-6: fraction of a second, scaled by 2^24 (0x000000..0xFFFFFF)
//	byte  7:   quality flags (bit 7 = valid; 0x80 = good/valid)
func EncodeUtcTime(t time.Time) []byte {
	s := t.UTC()
	epoch := s.Unix()
	fracVal := uint32(uint64(s.Nanosecond()) * (1 << 24) / 1_000_000_000)
	b := make([]byte, 8)
	b[0] = byte(epoch >> 24)
	b[1] = byte((epoch >> 16) & 0xff)
	b[2] = byte((epoch >> 8) & 0xff)
	b[3] = byte(epoch & 0xff)
	b[4] = byte((fracVal >> 16) & 0xff)
	b[5] = byte((fracVal >> 8) & 0xff)
	b[6] = byte(fracVal & 0xff)
	b[7] = 0x80 // valid
	return b
}

func DecodeUtcTime(b []byte) time.Time {
	if len(b) != 8 {
		return time.Time{}
	}
	epoch := int64(b[0])<<24 | int64(b[1])<<16 | int64(b[2])<<8 | int64(b[3])
	fracVal := uint32(b[4])<<16 | uint32(b[5])<<8 | uint32(b[6])
	ns := int64(fracVal) * 1_000_000_000 / (1 << 24)
	return time.Unix(epoch, ns).UTC()
}
