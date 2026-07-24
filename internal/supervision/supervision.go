package supervision

import "fmt"

// SendInterval prints a simulated 0x88fb supervision frame transmission.
func SendInterval(iface string, intervalSec int) {
	fmt.Printf("supervision: sending 0x88fb on %s every %ds\n", iface, intervalSec)
}
