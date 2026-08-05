package iec

import (
	"time"
)

// UtcTime: 4-byte seconds since 1970-01-01 00:00:00 UTC,
// 3-byte fraction, 1-byte flags (0x80 = good).
func EncodeUtcTime(t time.Time) []byte {
	s := t.UTC()
	epoch := int32(s.Unix())
	// fraction removed
	// Standard IEC 61850: fraction = (fraction of second) * 16777216 / 65536? Actually 3-byte value = fraction * 256 / 65536? Keep simple: fraction as 3-byte int from nanoseconds / 16777 (approx).
	fracVal := uint32(s.Nanosecond()) / 16777
	if fracVal > 0xffffff {
		fracVal = 0xffffff
	}
	b := make([]byte, 8)
	b[0] = byte(epoch >> 24)
	b[1] = byte((epoch >> 16) & 0xff)
	b[2] = byte((epoch >> 8) & 0xff)
	b[3] = byte(epoch & 0xff)
	b[4] = byte((fracVal >> 16) & 0xff)
	b[5] = byte((fracVal >> 8) & 0xff)
	b[6] = byte(fracVal & 0xff)
	b[7] = 0x80 // flags: good
	return b
}

func DecodeUtcTime(b []byte) time.Time {
	if len(b) != 8 {
		return time.Time{}
	}
	epoch := int32(b[0])<<24 | int32(b[1])<<16 | int32(b[2])<<8 | int32(b[3])
	fracVal := uint32(b[4])<<16 | uint32(b[5])<<8 | uint32(b[6])
	sec := int64(epoch)
	fracMs := int64(float64(fracVal) * 0.061) // approximate ms from fraction
	return time.Unix(sec, fracMs*1e6).UTC()
}
