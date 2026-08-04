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

var table = NewTable(256)

func NewTable(maxSize int) *Table {
	return &Table{
		entries: make(map[string]Entry),
		maxSize: maxSize,
	}
}

func key(srcMAC string, seq int) string {
	return fmt.Sprintf("%s|%d", srcMAC, seq)
}

func Insert(srcMAC string, seq, lanID int) {
	InsertWithExpiry(srcMAC, seq, lanID, 640)
}

func InsertWithExpiry(srcMAC string, seq, lanID int, expiryMs int) {
	table.mu.Lock()
	defer table.mu.Unlock()
	table.entries[key(srcMAC, seq)] = Entry{
		SrcMAC:   srcMAC,
		SeqNo:    seq,
		LANID:    lanID,
		LastSeen: time.Now(),
		ExpiryMs: expiryMs,
	}
}

// Find checks if a frame has been seen (not expired).
func Find(srcMAC string, seq int) bool {
	table.mu.RLock()
	defer table.mu.RUnlock()
	entry, ok := table.entries[key(srcMAC, seq)]
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
func Cleanup() int {
	now := time.Now()
	removed := 0
	table.mu.Lock()
	defer table.mu.Unlock()
	for k, v := range table.entries {
		if now.Sub(v.LastSeen) > time.Duration(v.ExpiryMs)*time.Millisecond {
			delete(table.entries, k)
			removed++
		}
	}
	return removed
}

// Size returns the current number of entries in the table.
func Size() int {
	table.mu.RLock()
	defer table.mu.RUnlock()
	return len(table.entries)
}
