package tui

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestRingBufferBounds(t *testing.T) {
	var tee bytes.Buffer
	b := NewRingBuffer(5, func(p []byte) { tee.Write(p) })
	for i := 0; i < 20; i++ {
		fmt.Fprintf(b, "line %d\n", i)
	}
	if b.Len() != 5 {
		t.Fatalf("Len = %d, want 5 (bounded)", b.Len())
	}
	lines := b.Lines(5)
	if lines[0] != "line 15" || lines[4] != "line 19" {
		t.Errorf("oldest lines dropped: got %v", lines)
	}
	// Tee saw everything.
	if !strings.Contains(tee.String(), "line 0") || !strings.Contains(tee.String(), "line 19") {
		t.Error("tee did not receive all writes")
	}
}

func TestRingBufferPartialLines(t *testing.T) {
	b := NewRingBuffer(10, nil)
	b.Write([]byte("part1"))
	if b.Len() != 0 {
		t.Errorf("partial line must not count as a complete line, got %d", b.Len())
	}
	b.Write([]byte("part2\n"))
	lines := b.Lines(1)
	if len(lines) != 1 || lines[0] != "part1part2" {
		t.Errorf("partial-line join failed: %v", lines)
	}
}

func TestRingBufferConcurrent(t *testing.T) {
	// Run with -race: concurrent producers must not corrupt the buffer.
	b := NewRingBuffer(50, nil)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				fmt.Fprintf(b, "g%d line %d\n", id, i)
			}
		}(g)
	}
	wg.Wait()
	if b.Len() > 50 {
		t.Fatalf("buffer exceeded capacity: %d", b.Len())
	}
}

func TestPrpdTUIHandlesLogBurst(t *testing.T) {
	// The renderer must tolerate many logs without growing the frame
	// beyond the bounded panel.
	b := NewRingBuffer(200, nil)
	for i := 0; i < 500; i++ {
		fmt.Fprintf(b, "burst line %d\n", i)
	}
	var out bytes.Buffer
	view := StartPrpd(&out)
	view.Draw(PrpdStats{
		Role:  "hsr-san",
		Name:  "redbox-1",
		PRPID: 1,
		Drops: map[string]int{"dup": 3},
	}, PrpdSettings{
		Interfaces:     "eth0 (A), eth1 (B), eth2 (interlink)",
		TrailerEnabled: true,
		Supervision:    "on (2s)",
		ForwardAll:     true,
	}, b.Lines(8))
	view.StopPrpd()
	s := out.String()
	// Compact layout: version on the first line, stats/settings on single
	// rows, drops, then the log tail.
	for _, want := range []string{"v0.5.3", "A in=", "trailer=", "drops: dup=3", "burst line 4"} {
		if !strings.Contains(s, want) {
			t.Errorf("frame missing %q", want)
		}
	}
	// The version must appear on the FIRST line of the drawn frame so it
	// is never scrolled off the top (the alternate-screen prefix precedes
	// the first draw).
	if !strings.Contains(s, "\x1b[2J\x1b[Hv0.5.3") {
		t.Errorf("version not on first line: %q", s[:80])
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate short = %q", got)
	}
	got := truncate("abcdefghijklmnop", 10)
	if got != "abcdefg..." {
		t.Errorf("truncate long = %q, want abcdefg...", got)
	}
}

// TestPrpdTUINoStaleLines: redrawing with a shorter frame (fewer log
// lines) must clear the lines from the previous frame — otherwise stale
// text like "STATS port A..." appears spliced into the new frame.
func TestPrpdTUINoStaleLines(t *testing.T) {
	var out bytes.Buffer
	view := StartPrpd(&out)
	st := PrpdStats{Role: "hsr-san", Name: "rb", PRPID: 1}
	set := PrpdSettings{Interfaces: "eth0 (A), eth1 (B), eth2 (i)", TrailerEnabled: true, Supervision: "on (2s)", ForwardAll: true}

	// First frame: 3 log lines.
	view.Draw(st, set, []string{"log 1", "log 2", "log 3"})
	first := out.String()

	// Second frame: no log lines at all.
	out.Reset()
	view.Draw(st, set, nil)
	second := out.String()

	// The second frame must contain a clear-screen sequence and must not
	// contain the stale "log 1" text.
	if !strings.Contains(second, "\x1b[2J") {
		t.Error("second frame missing clear-screen sequence")
	}
	if strings.Contains(second, "log 1") {
		t.Error("stale log line leaked into second frame")
	}
	// The frame must end by clearing to end-of-screen.
	if !strings.Contains(second, "\x1b[0J") {
		t.Error("frame missing clear-to-end-of-screen")
	}
	_ = first
}
