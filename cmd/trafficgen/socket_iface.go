package main

import "time"

// Packet socket abstraction. Linux uses an AF_PACKET raw socket with
// PACKET_AUXDATA (so VLAN tags stripped by RX offload are restored);
// Windows (see socket_windows.go) uses the Npcap wpcap.dll interface.
// The rest of trafficgen only talks to this interface.
type pktSocket interface {
	// Send transmits one Ethernet frame.
	Send(frame []byte) error
	// Recv blocks until a frame arrives (or a timeout set via
	// SetRecvTimeout expires, in which case it returns 0, nil).
	Recv(buf []byte) (int, error)
	// SetRecvTimeout sets the receive timeout.
	SetRecvTimeout(d time.Duration) error
	// Close releases the socket.
	Close() error
}
