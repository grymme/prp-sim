//go:build windows

package main

// Windows packet socket backed by Npcap's wpcap.dll (pure Go syscall
// binding, no cgo — cross-compiles with GOOS=windows from any host).
//
// Requirements on the target machine:
//   - Npcap installed (https://npcap.com) with "WinPcap API-compatible
//     Mode" enabled during install (the default).
//   - Administrative rights, OR Npcap installed with "Allow non-admin
//     users to capture and send packets" checked.
//
// Why Npcap and not raw sockets? Windows has no AF_PACKET; user-space
// code cannot send raw Ethernet frames without a driver. Npcap is the
// same driver Wireshark/scapy use and lets us inject L2 frames out of a
// physical NIC (GOOSE/SV are L2 multicast, no IP involved).

import (
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"
)

const pcapErrBufSize = 256

// npcapDLL resolves wpcap.dll. Npcap installs it under
// C:\Windows\System32\Npcap and adds that directory to PATH, so plain
// LoadLibrary("wpcap.dll") finds it; try the explicit path first as a
// fallback in case PATH is not updated.
func npcapDLL() (*syscall.DLL, error) {
	if dll, err := syscall.LoadDLL("wpcap.dll"); err == nil {
		return dll, nil
	}
	return syscall.LoadDLL("Npcap\\wpcap.dll")
}

type wpcap struct {
	dll      *syscall.DLL
	openLive *syscall.Proc
	sendPkt  *syscall.Proc
	nextEx   *syscall.Proc
	findAll  *syscall.Proc
	freeAll  *syscall.Proc
	close    *syscall.Proc
	geterr   *syscall.Proc
}

var wp = loadWpcap()

func loadWpcap() *wpcap {
	dll, err := npcapDLL()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wpcap.dll not found — install Npcap from https://npcap.com (enable \"WinPcap API-compatible Mode\")\n")
		os.Exit(1)
	}
	proc := func(name string) *syscall.Proc {
		p, err := dll.FindProc(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "wpcap.dll: missing export %s — install a recent Npcap (1.60+)\n", name)
			os.Exit(1)
		}
		return p
	}
	return &wpcap{
		dll:      dll,
		openLive: proc("pcap_open_live"),
		sendPkt:  proc("pcap_sendpacket"),
		nextEx:   proc("pcap_next_ex"),
		findAll:  proc("pcap_findalldevs"),
		freeAll:  proc("pcap_freealldevs"),
		close:    proc("pcap_close"),
		geterr:   proc("pcap_geterr"),
	}
}

// windowsSocket wraps an open pcap_t.
type windowsSocket struct {
	handle uintptr
	closed bool
}

// device is one entry of pcap_findalldevs.
type device struct {
	name        string
	description string
}

// listDevices enumerates Npcap interfaces and prints them.
func listDevices() {
	for _, d := range findDevices() {
		fmt.Printf("%s\t%s\n", d.name, d.description)
	}
}

func findDevices() []device {
	var alldevs unsafe.Pointer
	errbuf := make([]byte, pcapErrBufSize)
	devsPtr := uintptr(unsafe.Pointer(&alldevs))
	errPtr := uintptr(unsafe.Pointer(&errbuf[0]))
	r, _, _ := syscall.SyscallN(wp.findAll.Addr(), devsPtr, errPtr)
	if r != 0 {
		fmt.Fprintf(os.Stderr, "pcap_findalldevs: %s\n", cStr(errbuf))
		os.Exit(1)
	}
	defer syscall.SyscallN(wp.freeAll.Addr(), uintptr(alldevs))

	var out []device
	for p := alldevs; p != nil; {
		name := *(*unsafe.Pointer)(unsafe.Add(p, 8))  // char *name
		desc := *(*unsafe.Pointer)(unsafe.Add(p, 16)) // char *description
		d := device{name: ptrStr(name)}
		if desc != nil {
			d.description = ptrStr(desc)
		}
		out = append(out, d)
		p = *(*unsafe.Pointer)(p) // next
	}
	return out
}

// resolveDevice maps the user-supplied --iface to an Npcap device name.
// Accepts: an exact Npcap name ("\\Device\\NPF_{GUID}"), a 1-based index
// ("1", "2", ...), or a substring of the friendly description.
func resolveDevice(iface string) string {
	devs := findDevices()
	if len(devs) == 0 {
		fmt.Fprintln(os.Stderr, "no Npcap interfaces found")
		os.Exit(1)
	}
	// Exact Npcap name.
	for _, d := range devs {
		if d.name == iface {
			return d.name
		}
	}
	// 1-based index.
	var idx int
	if _, err := fmt.Sscanf(iface, "%d", &idx); err == nil {
		if idx >= 1 && idx <= len(devs) {
			return devs[idx-1].name
		}
		fmt.Fprintf(os.Stderr, "interface index %d out of range (1..%d)\n", idx, len(devs))
		os.Exit(1)
	}
	// Substring match against the friendly description (case-insensitive).
	lower := lowerASCII(iface)
	var matches []device
	for _, d := range devs {
		if containsFold(d.description, lower) {
			matches = append(matches, d)
		}
	}
	if len(matches) == 1 {
		return matches[0].name
	}
	if len(matches) > 1 {
		fmt.Fprintf(os.Stderr, "--iface %q is ambiguous, matches:\n", iface)
		for i, d := range matches {
			fmt.Fprintf(os.Stderr, "  %d: %s (%s)\n", i+1, d.name, d.description)
		}
		fmt.Fprintln(os.Stderr, "use the Npcap name or a 1-based index")
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "no Npcap interface matches %q. Run with --list-devices to see available interfaces.\n", iface)
	os.Exit(1)
	return ""
}

// openSocket opens an Npcap capture handle on the interface with a 1s
// timeout (so Recv can poll for shutdown).
func openSocket(iface string) (pktSocket, error) {
	dev := resolveDevice(iface)
	errbuf := make([]byte, pcapErrBufSize)
	devP := append([]byte(dev), 0)
	devPtr := uintptr(unsafe.Pointer(&devP[0]))
	errPtr := uintptr(unsafe.Pointer(&errbuf[0]))
	r, _, _ := syscall.SyscallN(wp.openLive.Addr(),
		devPtr,
		uintptr(2048),   // snaplen
		uintptr(1),      // promiscuous
		uintptr(1000),   // read timeout (ms)
		errPtr,
	)
	if r == 0 {
		return nil, fmt.Errorf("pcap_open_live(%s): %s (run as administrator, or enable Npcap's non-admin capture option)", dev, cStr(errbuf))
	}
	return &windowsSocket{handle: r}, nil
}

func (s *windowsSocket) Send(frame []byte) error {
	if s.closed {
		return fmt.Errorf("socket closed")
	}
	framePtr := uintptr(unsafe.Pointer(&frame[0]))
	r, _, _ := syscall.SyscallN(wp.sendPkt.Addr(), s.handle, framePtr, uintptr(len(frame)))
	if r != 0 {
		return fmt.Errorf("pcap_sendpacket: %s", wp.errstr(s.handle))
	}
	return nil
}

func (s *windowsSocket) Recv(buf []byte) (int, error) {
	if s.closed {
		return 0, fmt.Errorf("socket closed")
	}
	var hdr unsafe.Pointer
	var data unsafe.Pointer
	hdrPtr := uintptr(unsafe.Pointer(&hdr))
	dataPtr := uintptr(unsafe.Pointer(&data))
	r, _, _ := syscall.SyscallN(wp.nextEx.Addr(), s.handle, hdrPtr, dataPtr)
	switch r {
	case 1:
		caplen := *(*uint32)(unsafe.Add(hdr, 8)) // pcap_pkthdr: ts(8) + caplen(4)
		if int(caplen) > len(buf) {
			caplen = uint32(len(buf))
		}
		copy(buf, unsafe.Slice((*byte)(data), int(caplen)))
		return int(caplen), nil
	case 0:
		return 0, nil // read timeout
	default:
		return 0, fmt.Errorf("pcap_next_ex: %s", wp.errstr(s.handle))
	}
}

func (s *windowsSocket) SetRecvTimeout(d time.Duration) error {
	// The open timeout is fixed at 1s; the recv loop polls in 1s steps.
	return nil
}

func (s *windowsSocket) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	syscall.SyscallN(wp.close.Addr(), s.handle)
	return nil
}

// errstr returns pcap_geterr(p) as a string.
func (w *wpcap) errstr(handle uintptr) string {
	r, _, _ := syscall.SyscallN(w.geterr.Addr(), handle)
	if r == 0 {
		return "unknown pcap error"
	}
	// Reinterpret the returned pointer value bit-for-bit (avoids a
	// uintptr->unsafe.Pointer conversion that the unsafeptr analyzer
	// flags).
	return ptrStr(*(*unsafe.Pointer)(unsafe.Pointer(&r)))
}

// cStr converts a Go []byte to a C string (NUL-terminated).
func cStr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// ptrStr reads a NUL-terminated char* at a C pointer.
func ptrStr(p unsafe.Pointer) string {
	if p == nil {
		return ""
	}
	var b []byte
	for {
		c := *(*byte)(unsafe.Add(p, len(b)))
		if c == 0 {
			break
		}
		b = append(b, c)
	}
	return string(b)
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func containsFold(s, lowerSub string) bool {
	return len(lowerSub) > 0 && len(lowerSub) <= len(s) && len(lowerASCII(s)) >= len(lowerSub) && contains(lowerASCII(s), lowerSub)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
