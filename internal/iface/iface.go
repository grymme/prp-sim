package iface

import (
	"fmt"
	"net"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// PacketPort is the interface implemented by RawSocket and TAPSocket.
// It allows injecting in-memory ports into the PRP node for testing.
type PacketPort interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Close() error
	Name() string
}

// RawSocket wraps a Linux raw socket (AF_PACKET) for frame capture/injection.
type RawSocket struct {
	name    string
	ifIndex int
	fd      int
	mtu     int
}

const (
	ethPAll = 0x0003
)

// htons converts 16-bit value from host to network byte order.
func htons(v uint16) uint16 {
	return (v << 8) & 0xFF00 | v>>8
}

// sockaddrLinkLayer wraps the kernel's struct sockaddr_ll (20 bytes) for
// AF_PACKET bind/sendto. unix.RawSockaddrLinklayer has the exact layout.
type sockaddrLinkLayer = unix.RawSockaddrLinklayer

// CreateRawSocket creates a raw socket bound to a specific interface.
func CreateRawSocket(iface string) (*RawSocket, error) {
	ifIndex, err := getInterfaceIndex(iface)
	if err != nil {
		return nil, fmt.Errorf("get interface index for %s: %w", iface, err)
	}

	mtu, err := getInterfaceMTU(iface)
	if err != nil {
		return nil, fmt.Errorf("get interface MTU for %s: %w", iface, err)
	}

	fd, err := unix.Socket(
		unix.AF_PACKET,
		unix.SOCK_RAW,
		int(htons(ethPAll)),
	)
	if err != nil {
		return nil, fmt.Errorf("create raw socket: %w", err)
	}

	// Bind to specific interface
	addr := sockaddrLinkLayer{
		Family:  unix.AF_PACKET,
		Ifindex: int32(ifIndex),
	}

	_, _, errno := unix.Syscall(
		unix.SYS_BIND,
		uintptr(fd),
		uintptr(unsafe.Pointer(&addr)),
		uintptr(unsafe.Sizeof(addr)),
	)
	if errno != 0 {
		unix.Close(fd)
		return nil, fmt.Errorf("bind to interface %s: %v", iface, errno)
	}

	// Increase receive buffer size
	unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, 128*1024)

	// Do not receive our own transmitted frames. Without this, an AF_PACKET
	// raw socket gets a loopback copy of every frame it sends, which would
	// make the RedBox re-ingest its own duplicated traffic and echo it back
	// to the interlink (packet storm).
	if err := unix.SetsockoptInt(fd, unix.SOL_PACKET, unix.PACKET_IGNORE_OUTGOING, 1); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("set PACKET_IGNORE_OUTGOING: %w", err)
	}

	return &RawSocket{
		name:    iface,
		ifIndex: ifIndex,
		fd:      fd,
		mtu:     mtu,
	}, nil
}

// Read reads a frame from the interface.
func (r *RawSocket) Read(buf []byte) (int, error) {
	return unix.Read(r.fd, buf)
}

// Write writes a frame to the interface.
func (r *RawSocket) Write(frame []byte) (int, error) {
	addr := sockaddrLinkLayer{
		Family:  unix.AF_PACKET,
		Ifindex: int32(r.ifIndex),
	}

	_, _, errno := unix.Syscall6(
		unix.SYS_SENDTO,
		uintptr(r.fd),
		uintptr(unsafe.Pointer(&frame[0])),
		uintptr(len(frame)),
		uintptr(0), // flags
		uintptr(unsafe.Pointer(&addr)),
		uintptr(unsafe.Sizeof(addr)),
	)
	if errno != 0 {
		return 0, fmt.Errorf("sendto: %v", errno)
	}

	return len(frame), nil
}

// Close closes the raw socket.
func (r *RawSocket) Close() error {
	return unix.Close(r.fd)
}

func (r *RawSocket) Name() string {
	return r.name
}

func (r *RawSocket) IfIndex() int {
	return r.ifIndex
}

func (r *RawSocket) MTU() int {
	return r.mtu
}

func (r *RawSocket) Fd() int {
	return r.fd
}

func getInterfaceIndex(name string) (int, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return 0, err
	}
	return iface.Index, nil
}

func getInterfaceMTU(name string) (int, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return 0, err
	}
	return iface.MTU, nil
}

// TAPSocket wraps a TAP interface file descriptor.
type TAPSocket struct {
	name string
	fd   int
	file *os.File
}

// CreateTAP creates a TAP (Layer 2) interface.
func CreateTAP(name string, mac string) (*TAPSocket, error) {
	if err := ensureTunModule(); err != nil {
		return nil, fmt.Errorf("ensure tun module: %w", err)
	}

	file, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/net/tun: %w", err)
	}

	// TUNSETIFF copies a full struct ifreq (40 bytes) from userspace and
	// reads ifr_flags at offset 16. Passing a raw 16-byte name buffer with
	// flags at offset 14 lets the kernel read uninitialized stack memory,
	// so the first attempt fails with EINVAL and later ones may randomly
	// succeed. Use unix.IoctlIfreq which manages the full layout.
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("ifreq(%s): %w", name, err)
	}
	ifr.SetUint16(uint16(unix.IFF_TAP | unix.IFF_NO_PI))

	if err := unix.IoctlIfreq(int(file.Fd()), unix.TUNSETIFF, ifr); err != nil {
		file.Close()
		return nil, fmt.Errorf("TUNSETIFF: %w", err)
	}

	// Read back the actual interface name.
	actualName := ifr.Name()

	if mac != "" && mac != "auto" {
		if err := setInterfaceMAC(actualName, mac); err != nil {
			file.Close()
			return nil, fmt.Errorf("set MAC: %w", err)
		}
	}

	if err := setInterfaceUp(actualName); err != nil {
		file.Close()
		return nil, fmt.Errorf("set interface up: %w", err)
	}

	// TAP interface up and running
	return &TAPSocket{
		name: actualName,
		fd:   int(file.Fd()),
		file: file,
	}, nil
}

func (t *TAPSocket) Name() string {
	return t.name
}

func (t *TAPSocket) Fd() int {
	return t.fd
}

func (t *TAPSocket) File() *os.File {
	return t.file
}

func (t *TAPSocket) Read(buf []byte) (int, error) {
	return t.file.Read(buf)
}

func (t *TAPSocket) Write(frame []byte) (int, error) {
	return t.file.Write(frame)
}

func (t *TAPSocket) Close() error {
	return t.file.Close()
}

func ensureTunModule() error {
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("/dev/net/tun not found - load tun kernel module")
		}
		return err
	}
	return nil
}

// SetMTU changes the MTU of a network interface (used to raise the PRP LAN
// port MTU so a full-size SAN frame plus the 6-byte RCT fits on the wire).
func SetMTU(name string, mtu int) error {
	fd, err := ioctlSocket()
	if err != nil {
		return err
	}
	defer unix.Close(fd)

	var ifreq struct {
		Name [unix.IFNAMSIZ]byte
		MTU  int32
		_    [20]byte
	}
	copy(ifreq.Name[:], name)
	ifreq.MTU = int32(mtu)

	_, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		uintptr(fd),
		unix.SIOCSIFMTU,
		uintptr(unsafe.Pointer(&ifreq)),
	)
	if errno != 0 {
		return fmt.Errorf("SIOCSIFMTU(%d): %v", mtu, errno)
	}
	return nil
}

// ioctlSocket returns a socket fd that network-interface ioctls can be
// issued against. Interface ioctls (SIOCGIFFLAGS etc.) are only accepted on
// socket fds — issuing them against the TAP char-device fd fails with EINVAL.
func ioctlSocket() (int, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	return fd, nil
}

// setInterfaceMAC sets the hardware address of a network interface.
func setInterfaceMAC(name, mac string) error {
	hwAddr, err := net.ParseMAC(mac)
	if err != nil {
		return err
	}

	fd, err := ioctlSocket()
	if err != nil {
		return err
	}
	defer unix.Close(fd)

	// Kernel copies sizeof(struct ifreq) = 40 bytes; ifr_hwaddr (a
	// sockaddr, 16 bytes) starts at offset 16.
	var ifreq [40]byte
	copy(ifreq[0:16], name)
	if len(hwAddr) != 6 {
		return fmt.Errorf("invalid MAC %q", mac)
	}
	ifreq[16] = unix.AF_UNSPEC // sockaddr family
	copy(ifreq[22:28], hwAddr)

	_, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		uintptr(fd),
		unix.SIOCSIFHWADDR,
		uintptr(unsafe.Pointer(&ifreq[0])),
	)
	if errno != 0 {
		return fmt.Errorf("SIOCSIFHWADDR: %v", errno)
	}
	return nil
}

// setInterfaceUp brings a network interface up.
func setInterfaceUp(name string) error {
	fd, err := ioctlSocket()
	if err != nil {
		return err
	}
	defer unix.Close(fd)

	// Use unix.IoctlIfreq: it manages the full 40-byte struct ifreq so the
	// flags (ifr_flags, offset 16) are read from a correctly laid-out
	// buffer. A hand-rolled short struct lets the kernel copy 40 bytes
	// over a smaller buffer (stack corruption / garbage flags).
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return err
	}

	// Get current flags.
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return fmt.Errorf("SIOCGIFFLAGS: %w", err)
	}
	cur := ifr.Uint16()

	// Set IFF_UP (IFF_RUNNING is a read-only state flag).
	ifr.SetUint16(cur | unix.IFF_UP)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr); err != nil {
		return fmt.Errorf("SIOCSIFFLAGS: %w", err)
	}
	return nil
}
