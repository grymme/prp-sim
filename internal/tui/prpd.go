// internal/tui/prpd.go — full-screen ANSI TUI for the prpd RedBox daemon.
//
// Layout (top to bottom):
//
//	Header   — node name, role (prp-san/hsr-san/hsr-prp), PRP ID,
//	           NetId/LanId for hsr-prp, uptime
//	Stats    — per-port counters (ring/LAN A and B, interlink in/out),
//	           supervision sent, node-table size
//	Settings — effective config: trailer, supervision, filters, debug
//	Debug    — drop-reason counters + last N log lines (ring buffer)
//
// Refresh is a fixed 10 s ticker; each draw is one whole-frame write.
// Pure ANSI escape codes, no external dependencies.
package tui

import (
	"fmt"
	"io"
	"strings"
	"time"

	"prp-gns3/internal/version"
)

// PrpdStats is the snapshot data the daemon passes to the renderer.
// (cmd/prpd adapts prp.Status into this; tui stays dependency-free.)
type PrpdStats struct {
	Role         string
	Name         string
	PRPID        int
	NetID        int
	LanID        string
	LanAIn       uint64
	LanAOut      uint64
	LanBIn       uint64
	LanBOut      uint64
	InterIn      uint64
	InterOut     uint64
	SupSent      uint64
	DupTableSize int
	Drops        map[string]int
}

// PrpdSettings is a snapshot of the effective configuration for display.
type PrpdSettings struct {
	Interfaces     string // "eth0 (A), eth1 (B), eth2 (interlink)"
	TrailerEnabled bool
	Supervision    string // "on (2s)" or "off"
	NodeForget     string // e.g. "64s"
	ProxyForget    string // e.g. "64s"
	EntryForget    string // e.g. "640ms"
	ForwardAll     bool
	VLANFilter     string // "none" or "10,20"
	Multicast      string // "none (allow all)" or "01-00-5E"
	DebugFrames    bool
}

// prpdTUI manages the full-screen session.
type prpdTUI struct {
	out     io.Writer
	started time.Time
	lastLog int
}

// StartPrpd enters alternate screen mode and clears it.
func StartPrpd(out io.Writer) *prpdTUI {
	t := &prpdTUI{out: out, started: time.Now()}
	fmt.Fprint(out, "\x1b[?1049h") // alternate screen
	fmt.Fprint(out, "\x1b[?25l")   // hide cursor
	fmt.Fprint(out, "\x1b[2J\x1b[H")
	return t
}

// Draw renders the full frame (header, stats, settings, debug log).
func (t *prpdTUI) Draw(st PrpdStats, set PrpdSettings, logs []string) {
	var b strings.Builder
	b.WriteString("\x1b[2J\x1b[H") // clear screen + home (short frames must not leave stale lines)
	line := func(s string) { b.WriteString(s); b.WriteString("\r\n") }

	uptime := time.Since(t.started).Round(time.Second)
	role := st.Role
	if st.Role == "hsr-prp" {
		role = fmt.Sprintf("%s N%d L%s", st.Role, st.NetID, st.LanID)
	}
	// Row 1 — header: version, name, role, PRP ID, uptime.
	line(fmt.Sprintf("%s %s %s prp=%d up=%s",
		version.Version, st.Name, role, st.PRPID, uptime))
	// Row 2 — stats, one line per port pair.
	line(fmt.Sprintf("A in=%d out=%d | B in=%d out=%d | I in=%d out=%d | sup=%d nt=%d",
		st.LanAIn, st.LanAOut, st.LanBIn, st.LanBOut,
		st.InterIn, st.InterOut, st.SupSent, st.DupTableSize))
	// Row 3 — settings, one line.
	line(fmt.Sprintf("%s trailer=%v fwd=%v sup=%s nf=%s pf=%s ef=%s vlan=%s mcast=%s dbg=%v",
		set.Interfaces, set.TrailerEnabled, set.ForwardAll, set.Supervision,
		set.NodeForget, set.ProxyForget, set.EntryForget,
		set.VLANFilter, set.Multicast, set.DebugFrames))
	// Row 4 — drops.
	line(fmt.Sprintf("drops: %s", formatDrops(st.Drops)))
	// Rows 5+ — last log lines.
	for _, l := range logs {
		line("  " + truncate(l, 76))
	}
	// Clear anything below the last drawn line (frames shrink when the
	// log list does), so stale text from the previous frame never shows.
	b.WriteString("\x1b[0J\r\n")

	b.WriteString("\x1b[?25h")
	t.out.Write([]byte(b.String()))
}

// StopPrpd restores the main screen and the cursor, and prints a final
// status line so something is left on the console after exit.
func (t *prpdTUI) StopPrpd() {
	fmt.Fprint(t.out, "\x1b[?25h")   // show cursor
	fmt.Fprint(t.out, "\x1b[?1049l") // leave alternate screen
}

// truncate cuts s to at most n runes, appending "..." when cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}

// formatDrops renders the drop-reason counters as "dup=1 own=2 ...".
func formatDrops(drops map[string]int) string {
	if len(drops) == 0 {
		return "-"
	}
	keys := []string{"dup", "own", "path", "filter", "malformed"}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if v, ok := drops[k]; ok && v > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", k, v))
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}
