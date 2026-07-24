package tap

import "fmt"

// Create sets up the TAP interface (prp0).
// In a real build this opens /dev/net/tun and performs TUNSETIFF.
// For this skeleton we just announce creation.
func Create(name string) error {
	fmt.Printf("tap: created interface %s\n", name)
	return nil
}
