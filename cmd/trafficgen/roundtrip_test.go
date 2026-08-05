package main

import "encoding/binary"

import (
	"fmt"
	"testing"
	"time"
)

func TestRoundTripTimestamp(t *testing.T) {
	before := time.Now().Add(-50 * time.Millisecond)
	f := buildFrameEt(0x1001, 7, 3, 0, 4, etGOOSE, dstMAC)
	_, _, _, _, sentAt, _, _, _, ok := parseFrame(f, 0x1001)
	if !ok {
		t.Fatal("parse failed")
	}
	// find APDU offset
	off := 14
	if binary.BigEndian.Uint16(f[12:14]) == 0x8100 {
		off = 18
	}
	apdu := f[off+8:]
	fmt.Printf("apdu hex: %x\n", apdu)
	t.Logf("sentAt=%v zero=%v before=%v", sentAt, sentAt.IsZero(), before)
	if sentAt.IsZero() {
		t.Fatal("sentAt is zero - latency can't be computed")
	}
	if sentAt.Before(before) || sentAt.After(time.Now()) {
		t.Fatalf("sentAt out of range: %v (before=%v now=%v)", sentAt, before, time.Now())
	}
}
