package main

import "testing"

func TestParseFrameTagged(t *testing.T) {
	f := buildFrame(0x3001, 1, 1, 0, 4)
	ok := false
	_, _, _, _, _, _, _, _, ok = parseFrame(f, 0x3001)
	if !ok {
		t.Fatalf("parse failed")
	}
}
func TestParseFrameUntagged(t *testing.T) {
	f := buildFrame(0x3001, 1, 1, -1, 0)
	_, _, _, _, _, _, _, _, ok := parseFrame(f, 0x3001)
	if !ok {
		t.Fatalf("parse failed")
	}
}
