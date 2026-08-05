package iec

import (
	"bytes"
	"testing"
)

func TestBERRoundTrip(t *testing.T) {
	original := []byte{0xde, 0xad, 0xbe, 0xef}
	tlv := encodeTLV(0xab, original)
	tag, payload, rest, ok := parseTLV(tlv)
	if !ok {
		t.Fatal("parseTLV failed")
	}
	if tag != 0xab {
		t.Errorf("tag=%02x want ab", tag)
	}
	if !bytes.Equal(payload, original) {
		t.Errorf("payload=%x want %x", payload, original)
	}
	if len(rest) != 0 {
		t.Errorf("rest non-empty: %x", rest)
	}
}

func TestLengthShortAndLong(t *testing.T) {
	short := encodeLength(10)
	if len(short) != 1 || short[0] != 10 {
		t.Errorf("short length %x", short)
	}
	long := encodeLength(300)
	if len(long) < 3 {
		t.Errorf("long length too short: %x (len %d)", long, len(long))
	}
	if long[0] != 0x82 {
		t.Errorf("long form byte %02x want 82", long[0])
	}
}

func TestBoolean(t *testing.T) {
	trueP := Boolean(true)
	falseP := Boolean(false)
	if len(trueP) != 3 || trueP[2] != 0xff {
		t.Errorf("true Boolean payload %x", trueP)
	}
	if len(falseP) != 3 || falseP[2] != 0x00 {
		t.Errorf("false Boolean payload %x", falseP)
	}
}

func TestInteger16(t *testing.T) {
	p := Integer16(0x1234)
	if len(p) != 2 || p[0] != 0x12 || p[1] != 0x34 {
		t.Errorf("Integer16 payload %x", p)
	}
}

func TestString(t *testing.T) {
	s := String("hello")
	if !bytes.Equal(s, []byte("hello")) {
		t.Errorf("String payload %x", s)
	}
}
