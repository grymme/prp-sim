package iface

import (
	"net"
	"os"
	"testing"
)

// TestSetInterfaceIP verifies static IPv4 assignment via the
// SIOCSIFADDR/SIOCSIFNETMASK ioctls (requires root, skipped otherwise).
func TestSetInterfaceIP(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}
	if _, err := net.InterfaceByName("lo"); err != nil {
		t.Skip("no lo interface")
	}

	if err := SetInterfaceIP("lo", "192.0.2.7/24"); err != nil {
		t.Fatalf("SetInterfaceIP: %v", err)
	}

	ifc, err := net.InterfaceByName("lo")
	if err != nil {
		t.Fatalf("interface lookup: %v", err)
	}
	ifList, err := ifc.Addrs()
	if err != nil {
		t.Fatalf("list addresses: %v", err)
	}
	var found bool
	for _, a := range ifList {
		if a.String() == "192.0.2.7/24" {
			found = true
		}
	}
	if !found {
		t.Errorf("address 192.0.2.7/24 not present on lo: %v", ifList)
	}
}

// TestSetInterfaceIPRejectsIPv6: only IPv4 CIDRs are supported.
func TestSetInterfaceIPRejectsIPv6(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}
	if err := SetInterfaceIP("lo", "fe80::1/64"); err == nil {
		t.Fatal("expected error for IPv6 CIDR")
	}
}
