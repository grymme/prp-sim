//go:build !windows

package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// linuxSocket wraps an AF_PACKET raw socket bound to one interface.
type linuxSocket struct {
	fd      int
	ifIndex int
	timeout time.Duration
}

// openSocket binds an AF_PACKET raw socket (ETH_P_ALL) to the interface.
func openSocket(iface string) (pktSocket, error) {
	ifIndex, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, int(htons(0x0003)))
	if err != nil {
		return nil, err
	}
	addr := &unix.RawSockaddrLinklayer{Family: unix.AF_PACKET, Ifindex: int32(ifIndex.Index)}
	if _, _, errno := unix.Syscall(unix.SYS_BIND, uintptr(fd),
		uintptr(unsafe.Pointer(addr)), uintptr(unix.SizeofSockaddrLinklayer)); errno != 0 {
		unix.Close(fd)
		return nil, errno
	}
	unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, 512*1024)
	// VLAN RX offload strips 802.1Q tags and returns them as ancillary
	// data; without PACKET_AUXDATA the receiver would never see the tag.
	// (Same mechanism as prpd's RawSocket.)
	unix.SetsockoptInt(fd, unix.SOL_PACKET, unix.PACKET_AUXDATA, 1)
	return &linuxSocket{fd: fd, ifIndex: ifIndex.Index}, nil
}

func (s *linuxSocket) Send(frame []byte) error {
	_, _, errno := unix.Syscall6(unix.SYS_SENDTO, uintptr(s.fd),
		uintptr(unsafe.Pointer(&frame[0])), uintptr(len(frame)), 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func (s *linuxSocket) Recv(buf []byte) (int, error) {
	oob := make([]byte, 128)
	nr, oobn, _, _, err := unix.Recvmsg(s.fd, buf, oob, 0)
	if err != nil {
		if err == unix.EAGAIN || err == unix.EINTR {
			return 0, nil
		}
		return 0, err
	}
	// Reconstruct the VLAN tag stripped by RX offload so parseFrame
	// sees vid/pcp as they appear on the wire.
	return restoreVLANTag(buf, nr, oob[:oobn]), nil
}

func (s *linuxSocket) SetRecvTimeout(d time.Duration) error {
	s.timeout = d
	tv := unix.Timeval{Sec: int64(d / time.Second), Usec: int64((d % time.Second) / time.Microsecond)}
	return unix.SetsockoptTimeval(s.fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
}

func (s *linuxSocket) Close() error {
	return unix.Close(s.fd)
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

// restoreVLANTag re-inserts the 4-byte 802.1Q header into buf when the
// frame's VLAN tag was stripped by RX offload and returned as
// PACKET_AUXDATA ancillary data (oob). Returns the new frame length.
func restoreVLANTag(buf []byte, n int, oob []byte) int {
	if len(oob) == 0 {
		return n
	}
	msgs, perr := unix.ParseSocketControlMessage(oob)
	if perr != nil {
		return n
	}
	for _, m := range msgs {
		if m.Header.Level != unix.SOL_PACKET || m.Header.Type != unix.PACKET_AUXDATA {
			continue
		}
		if len(m.Data) < 20 {
			continue
		}
		status := binary.LittleEndian.Uint32(m.Data[0:4])
		if status&unix.TP_STATUS_VLAN_VALID == 0 {
			continue
		}
		tci := binary.LittleEndian.Uint16(m.Data[16:18])
		tpid := binary.LittleEndian.Uint16(m.Data[18:20])
		if tpid == 0 {
			tpid = 0x8100
		}
		if n < 12 || len(buf) < n+4 {
			return n
		}
		copy(buf[16:n+4], buf[12:n])
		buf[12] = byte(tpid >> 8)
		buf[13] = byte(tpid)
		buf[14] = byte(tci >> 8)
		buf[15] = byte(tci)
		return n + 4
	}
	return n
}

// listDevices prints available network interfaces (Linux: net.Interfaces).
func listDevices() {
	ifaces, err := net.Interfaces()
	if err != nil {
		fmt.Fprintf(os.Stderr, "list interfaces: %v\n", err)
		os.Exit(1)
	}
	for _, i := range ifaces {
		fmt.Printf("%s\t%s\n", i.Name, i.HardwareAddr)
	}
}
