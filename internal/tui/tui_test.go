package tui

import (
	"bytes"
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	var buf bytes.Buffer
	render(&buf, "publisher", "eth0", 0x1001, 42, true, 100, 99, 1, 5, nil)
	out := buf.String()
	if !strings.Contains(out, "PRP IED TUI") {
		t.Fatalf("missing header: %q", out)
	}
	if !strings.Contains(out, "stNum=42") {
		t.Fatalf("missing stNum: %q", out)
	}
	if !strings.Contains(out, "unique=99") {
		t.Fatalf("missing unique: %q", out)
	}
}
