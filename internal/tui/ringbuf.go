// internal/tui/ringbuf.go — bounded log ring buffer used to feed the
// prpd TUI's debug panel.
//
// Design goals (approved):
//   - Bounded: at most capacity lines are kept; the oldest lines are
//     dropped so a log burst can never grow memory.
//   - Decoupled: producers only append; the renderer snapshots. Rendering
//     never happens per log line, so bursts cannot cause render storms.
//   - Tee: the same writer also forwards every line to an external sink
//     (os.Stderr), so `docker logs` still sees the full output even when
//     the console is showing the TUI.
package tui

import (
	"sync"
)

// RingBuffer is a thread-safe, bounded line buffer implementing io.Writer.
type RingBuffer struct {
	mu       sync.Mutex
	lines    []string
	pending  string // partial line awaiting a newline
	capacity int
	tee      func(p []byte)
}

// NewRingBuffer creates a bounded line buffer. Lines are split on '\n';
// empty lines are skipped. tee, if non-nil, receives every raw write.
func NewRingBuffer(capacity int, tee func(p []byte)) *RingBuffer {
	if capacity <= 0 {
		capacity = 200
	}
	return &RingBuffer{
		lines:    make([]string, 0, capacity),
		capacity: capacity,
		tee:      tee,
	}
}

// Write implements io.Writer: appends complete lines to the buffer.
// A trailing partial line is retained and joined with the next write.
func (b *RingBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	s := b.pending + string(p)
	b.pending = ""
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			if line := s[start:i]; line != "" {
				b.push(line)
			}
			start = i + 1
		}
	}
	if start < len(s) {
		b.pending = s[start:]
	}

	if b.tee != nil {
		b.tee(p)
	}
	return len(p), nil
}

func (b *RingBuffer) push(line string) {
	b.lines = append(b.lines, line)
	if len(b.lines) > b.capacity {
		// Drop oldest.
		copy(b.lines, b.lines[len(b.lines)-b.capacity:])
		b.lines = b.lines[:b.capacity]
	}
}

// Lines returns the n most recent complete lines (oldest first).
func (b *RingBuffer) Lines(n int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n <= 0 || n > len(b.lines) {
		n = len(b.lines)
	}
	out := make([]string, n)
	copy(out, b.lines[len(b.lines)-n:])
	return out
}

// Len returns the number of buffered lines.
func (b *RingBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.lines)
}
