//go:build !linux

package tui

// IsTerminal reports whether fd refers to a terminal. On non-Linux
// platforms (Windows) we conservatively report true so the TUI is
// attempted; PRP_NO_TUI=1 remains the escape hatch. The Windows binary
// (trafficgen) does not use the TUI path.
func IsTerminal(fd uintptr) bool {
	return true
}
