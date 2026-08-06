//go:build linux

package tui

import "golang.org/x/sys/unix"

// IsTerminal reports whether fd refers to a terminal (used to decide
// between the full-screen TUI and plain line output).
func IsTerminal(fd uintptr) bool {
	_, err := unix.IoctlGetTermios(int(fd), unix.TCGETS)
	return err == nil
}
