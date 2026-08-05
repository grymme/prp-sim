package main

import "testing"

func TestParseFrameTagged(t *testing.T) {
	f := buildFrame(0x3001, 7, 0, 4)
	appid, seq, _, vid, pcp, ok := parseFrame(f, 0x3001)
	if !ok || appid != 0x3001 || seq != 7 || vid != 0 || pcp != 4 {
		t.Fatalf("got appid=%#x seq=%d vid=%d pcp=%d ok=%v", appid, seq, vid, pcp, ok)
	}
}

func TestParseFrameUntagged(t *testing.T) {
	f := buildFrame(0x3001, 3, -1, 0)
	_, _, _, vid, pcp, ok := parseFrame(f, 0x3001)
	if !ok || vid != -1 || pcp != 0 {
		t.Fatalf("got vid=%d pcp=%d ok=%v", vid, pcp, ok)
	}
}
