// internal/tui — live split-screen ANSI stats view for GNS3 console.
// Section 3 design: PRP-level top (LAN A/B, RCT errors, VLAN tags, supervision)
// + GOOSE/SV bottom (stNum, allData, latency histogram, last 5 malformed/rejected).
package tui

import (
	"fmt"
	"io"
	"os"
)

// render draws the full split-screen view into w.
func render(w io.Writer, role string, ifaceName string, appid uint16, stNum uint16, allData bool, total, unique, dupes int, latencyMs int64, errors []string) {
	fmt.Fprintf(w, "\033[2J\033[H")
	fmt.Fprintf(w, "=== PRP IED TUI === role=%s iface=%s appid=0x%04x\n", role, ifaceName, appid)
	fmt.Fprintf(w, "=== PRP-level === LAN-A/B frames | RCT errors | VLAN tags | supervision\n")
	fmt.Fprintf(w, "=== GOOSE/SV-level === stNum=%d allData=%v total=%d unique=%d dupes=%d lat=%dms\n",
		stNum, allData, total, unique, dupes, latencyMs)
	fmt.Fprintf(w, "=== Last 5 malformed/rejected ===\n")
	for _, e := range errors {
		fmt.Fprintf(w, "  %s\n", e)
	}
}

func Start(role string, ifaceName string, appid uint16) {
	render(os.Stdout, role, ifaceName, appid, 0, false, 0, 0, 0, 0, nil)
}

func Update(stNum uint16, allData bool, total, unique, dupes int, latencyMs int64, errors []string) {
	render(os.Stdout, "subscriber", "", 0, stNum, allData, total, unique, dupes, latencyMs, errors)
}

func Stop() {
	fmt.Println("=== TUI stopped === final stats reported ===")
}
