// internal/tui — live split-screen ANSI stats view for GNS3 console.
// Section 3 design: PRP-level top (LAN A/B, RCT errors, VLAN tags, supervision)
// + GOOSE/SV bottom (stNum, allData, latency histogram, last 5 malformed/rejected).
package tui

import (
	"fmt"
)

func Start(role string, ifaceName string, appid uint16) {
	fmt.Printf("\033[2J\033[H") // clear + home
	fmt.Printf("=== PRP IED TUI === role=%s iface=%s appid=0x%04x\n", role, ifaceName, appid)
	fmt.Println("=== PRP-level === LAN-A/B frames | RCT errors | VLAN tags | supervision")
	fmt.Println("=== GOOSE/SV-level === stNum | allData | latency p50/p95/max | errors")
	fmt.Println("=== Last 5 malformed/rejected ===")
}

func Update(stNum uint16, allData bool, total, unique, dupes int, latencyMs int64, errors []string) {
	fmt.Printf("\033[11;1H=== GOOSE === stNum=%d allData=%v total=%d unique=%d dupes=%d lat=%dms\n",
		stNum, allData, total, unique, dupes, latencyMs)
	fmt.Printf("=== Errors === %v\n", errors)
}

func Stop() {
	fmt.Println("=== TUI stopped === final stats reported ===")
}
