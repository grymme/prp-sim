package nodetable

import (
	"fmt"
	"sync"
	"time"
)

// Entry represents a tracked frame for duplicate detection.
type Entry struct {
	SrcMAC   string
	SeqNo    int
	LANID    int
	LastSeen time.Time
	ExpiryMs int
}

type Table struct {
	mu      sync.RWMutex
	entries map[string]Entry
	maxSize int
}

func NewTable(maxSize int) *Table {
	return &Table{
		entries: make(map[string]Entry),
		maxSize: maxSize,
	}
}

func key(srcMAC string, seq int) string {
	return fmt.Sprintf("%s|%d", srcMAC, seq)
}

// InsertWithExpiry stores a (srcMAC, seq) entry. The entry expires after
// expiryMs. When the table is at maxSize, the oldest entry is evicted.
func (t *Table) InsertWithExpiry(srcMAC string, seq, lanID int, expiryMs int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entries[key(srcMAC, seq)] = Entry{
		SrcMAC:   srcMAC,
		SeqNo:    seq,
		LANID:    lanID,
		LastSeen: time.Now(),
		ExpiryMs: expiryMs,
	}
	if t.maxSize > 0 && len(t.entries) > t.maxSize {
		// Evict the oldest entry.
		var oldestKey string
		var oldest time.Time
		for k, e := range t.entries {
			if oldestKey == "" || e.LastSeen.Before(oldest) {
				oldestKey = k
				oldest = e.LastSeen
			}
		}
		delete(t.entries, oldestKey)
	}
}

// Find checks if a frame has been seen (not expired).
func (t *Table) Find(srcMAC string, seq int) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	entry, ok := t.entries[key(srcMAC, seq)]
	if !ok {
		return false
	}
	// Check if expired
	if time.Since(entry.LastSeen) > time.Duration(entry.ExpiryMs)*time.Millisecond {
		return false
	}
	return true
}

// Cleanup removes stale entries. Returns count of removed entries.
func (t *Table) Cleanup() int {
	now := time.Now()
	removed := 0
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, v := range t.entries {
		if now.Sub(v.LastSeen) > time.Duration(v.ExpiryMs)*time.Millisecond {
			delete(t.entries, k)
			removed++
		}
	}
	return removed
}

// Size returns the current number of entries in the table.
func (t *Table) Size() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.entries)
}
