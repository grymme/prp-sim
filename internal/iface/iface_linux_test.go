package iface

import (
	"net"
	"os"
	"testing"
)

// TestCreateTAPLinux verifies the TAP interface can actually be created and
// brought up. This requires root (CAP_NET_ADMIN) and /dev/net/tun; it is
// skipped otherwise. Regression test for the bug where SIOCSIFFLAGS was
// issued against the interface index (EBADF) instead of the TAP fd.
func TestCreateTAPLinux(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Skip("no /dev/net/tun")
	}

	tap, err := CreateTAP("prptest0", "02:50:50:00:00:42")
	if err != nil {
		t.Fatalf("CreateTAP failed: %v", err)
	}
	defer tap.Close()

	// The interface must exist and be up.
	iface, err := net.InterfaceByName(tap.Name())
	if err != nil {
		t.Fatalf("interface %s not found: %v", tap.Name(), err)
	}
	if iface.Flags&net.FlagUp == 0 {
		t.Errorf("interface %s is not up", tap.Name())
	}

	// The configured MAC must have been applied.
	if want, got := "02:50:50:00:00:42", iface.HardwareAddr.String(); got != want {
		t.Errorf("MAC: want %s, got %s", want, got)
	}
}

// TestCreateTAPAutoMAC checks the default "auto" path (no explicit MAC).
func TestCreateTAPAutoMAC(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Skip("no /dev/net/tun")
	}

	tap, err := CreateTAP("prptest1", "auto")
	if err != nil {
		t.Fatalf("CreateTAP(auto) failed: %v", err)
	}
	defer tap.Close()

	if _, err := net.InterfaceByName(tap.Name()); err != nil {
		t.Fatalf("interface %s not found: %v", tap.Name(), err)
	}
}

// TestSetInterfaceIP verifies static IPv4 assignment via the
// SIOCSIFADDR/SIOCSIFNETMASK ioctls (requires root, skipped otherwise).
func TestSetInterfaceIP(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Skip("no /dev/net/tun")
	}

	tap, err := CreateTAP("prptest2", "auto")
	if err != nil {
		t.Fatalf("CreateTAP failed: %v", err)
	}
	defer tap.Close()

	if err := SetInterfaceUp(tap.Name()); err != nil {
		t.Fatalf("SetInterfaceUp: %v", err)
	}
	if err := SetInterfaceIP(tap.Name(), "192.0.2.7/24"); err != nil {
		t.Fatalf("SetInterfaceIP: %v", err)
	}

	addrs, err := net.InterfaceByName(tap.Name())
	if err != nil {
		t.Fatalf("interface lookup: %v", err)
	}
	ifList, err := addrs.Addrs()
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
		t.Errorf("address 192.0.2.7/24 not present on %s: %v", tap.Name(), ifList)
	}
}

// TestSetInterfaceIPRejectsIPv6: only IPv4 CIDRs are supported.
func TestSetInterfaceIPRejectsIPv6(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Skip("no /dev/net/tun")
	}

	tap, err := CreateTAP("prptest3", "auto")
	if err != nil {
		t.Fatalf("CreateTAP failed: %v", err)
	}
	defer tap.Close()

	if err := SetInterfaceIP(tap.Name(), "fe80::1/64"); err == nil {
		t.Fatal("expected error for IPv6 CIDR")
	}
}
