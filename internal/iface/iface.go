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
	ethPAll  = 0x0003
	ethPTxRx = 0x884c // PRP ethertype for sending
)

// htons converts 16-bit value from host to network byte order.
func htons(v uint16) uint16 {
	return (v << 8) & 0xFF00 | v>>8
}

// sockaddrLinkLayer mirrors the kernel struct for AF_PACKET binding.
type sockaddrLinkLayer struct {
	Family  uint16
	_       uint16 // pad
	Ifindex int32
	Hat     uint16
	_       [20]byte
}

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

	var ifr [unix.IFNAMSIZ]byte
	copy(ifr[:], name)

	flags := uint16(unix.IFF_TAP | unix.IFF_NO_PI | unix.IFF_RUNNING)
	*(*uint16)(unsafe.Pointer(&ifr[unix.IFNAMSIZ-2])) = flags

	_, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		file.Fd(),
		unix.TUNSETIFF,
		uintptr(unsafe.Pointer(&ifr[0])),
	)
	if errno != 0 {
		file.Close()
		return nil, fmt.Errorf("TUNSETIFF: %v", errno)
	}

	// Read back actual interface name
	actualName := string(ifr[:])
	for i := 0; i < len(actualName); i++ {
		if actualName[i] == 0 {
			actualName = actualName[:i]
			break
		}
	}

	if mac != "" && mac != "auto" {
		if err := setInterfaceMAC(file.Fd(), actualName, mac); err != nil {
			file.Close()
			return nil, fmt.Errorf("set MAC: %w", err)
		}
	}

	if err := setInterfaceUp(file.Fd(), actualName); err != nil {
		file.Close()
		return nil, fmt.Errorf("set interface up: %w", err)
	}

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

// setInterfaceMAC sets the hardware address of a network interface.
// fd must be an open file descriptor that ioctl can be issued on (the TAP
// fd or any socket); using the interface index here would fail with EBADF.
func setInterfaceMAC(fd uintptr, name, mac string) error {
	hwAddr, err := net.ParseMAC(mac)
	if err != nil {
		return err
	}

	var ifreq struct {
		Name   [unix.IFNAMSIZ]byte
		HWAddr [32]byte
		_      [20]byte
	}
	copy(ifreq.Name[:], name)
	copy(ifreq.HWAddr[:], hwAddr)

	_, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		fd,
		unix.SIOCSIFHWADDR,
		uintptr(unsafe.Pointer(&ifreq)),
	)
	if errno != 0 {
		return fmt.Errorf("SIOCSIFHWADDR: %v", errno)
	}

	return nil
}

// setInterfaceUp brings a network interface up.
// fd must be an open file descriptor that ioctl can be issued on (the TAP fd
// or any socket); using the interface index here would fail with EBADF.
func setInterfaceUp(fd uintptr, name string) error {
	var ifreq struct {
		Name  [unix.IFNAMSIZ]byte
		Flags uint16
		_     [20]byte
	}
	copy(ifreq.Name[:], name)

	// Get current flags
	_, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		fd,
		unix.SIOCGIFFLAGS,
		uintptr(unsafe.Pointer(&ifreq)),
	)
	if errno != 0 {
		return fmt.Errorf("SIOCGIFFLAGS: %v", errno)
	}

	// Set IFF_UP and IFF_RUNNING flags
	ifreq.Flags |= unix.IFF_UP | unix.IFF_RUNNING

	_, _, errno = unix.Syscall(
		unix.SYS_IOCTL,
		fd,
		unix.SIOCSIFFLAGS,
		uintptr(unsafe.Pointer(&ifreq)),
	)
	if errno != 0 {
		return fmt.Errorf("SIOCSIFFLAGS: %v", errno)
	}

	return nil
}
