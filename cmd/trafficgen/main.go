// trafficgen — GOOSE / Sampled Values (IEC 61850) traffic generator for
// e2e-testing the PRP simulator over raw sockets.
//
// It can run in three modes:
//
//	send    send GOOSE-like frames on one interface with a configurable
//	        rate, count and VLAN tag. Every frame carries a payload with
//	        {appid, seq, tx-nanos} for the receiver to deduplicate and
//	        timestamp.
//	recv    listen on one interface for the configured GOOSE appid,
//	        report unique frames seen, duplicate frames, and round-trip
//	        latency percentiles.
//	ping    (default, when no --mode) send-then-receive echo: emits
//	        'GOOSE-like echo' frames and reports loss + max latency.
//
// The frames are NOT PRP-encapsulated — they are plain SAN-side traffic
// that the RedBoxes under test duplicate (LAN side) and deduplicate.
//
// Usage examples (inside the test compose topology):
//
//	# Receiver on SAN-B, listening for appid 0x1001 for 15s:
//	docker run --rm --privileged --network tests_san-net-b \
//	    prp-sim:test trafficgen --mode recv --iface eth0 --appid 0x1001 --duration 15s
//
//	# Sender on SAN-A: 100 frames at 100 Hz with GOOSE-style retransmit
//	# burst (2,4,8,16... ms spacing) and a PCP=4 VLAN tag:
//	docker run --rm --privileged --network tests_san-net-a \
//	    prp-sim:test trafficgen --mode send --iface eth0 --appid 0x1001 \
//	    --count 100 --rate 100 --vlan 4
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	// GOOSE EtherType (IEC 61850-8-1).
	etGOOSE = 0x88B8
	// Sampled Values EtherType (IEC 61850-9-2).
	etSV = 0x88BA

	// GOOSE multicast group base (01-0C-CD-01-00-00 .. 01-0C-CD-01-01-FF).
	mcastGOOSE = 0x010ccd010000
	// SV multicast group (01-0C-CD-04-00-00).
	mcastSV = 0x010ccd040000

	// payload layout: appid(2) seq(4) txns(8) — 14 bytes of GOOSE-ish
	// payload after the ethernet header (+optional VLAN).
	payloadLen = 14
)

var dstMAC = []byte{0x01, 0x0c, 0xcd, 0x01, 0x00, 0x01}
var srcMAC = []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}

type stats struct {
	total    int
	unique   int
	dupes    int
	latency  []time.Duration
	lastSeq  uint32
	lastSeen map[uint32]bool
	// tags records the distinct (vid, pcp) pairs observed on the wire.
	tags map[int]map[int]int
}

func (s *stats) noteTagged(vid, pcp int) {
	if vid < 0 {
		return
	}
	if s.tags == nil {
		s.tags = make(map[int]map[int]int)
	}
	if s.tags[vid] == nil {
		s.tags[vid] = make(map[int]int)
	}
	s.tags[vid][pcp]++
}

func (s *stats) noteFrame(seq uint32, sentAt time.Time) {
	s.total++
	if sentAt.IsZero() {
		// Unknown tx time (foreign frame) — only count uniqueness.
		if s.lastSeen[seq] {
			s.dupes++
		} else {
			s.unique++
			if s.lastSeen == nil {
				s.lastSeen = make(map[uint32]bool)
			}
			s.lastSeen[seq] = true
		}
		return
	}
	lat := time.Since(sentAt)
	if lat > 0 {
		s.latency = append(s.latency, lat)
	}
	if s.lastSeen[seq] {
		s.dupes++
		return
	}
	if s.lastSeen == nil {
		s.lastSeen = make(map[uint32]bool)
	}
	s.lastSeen[seq] = true
	s.unique++
}

func (s *stats) report(label string) {
	fmt.Printf("%s: total=%d unique=%d dupes=%d\n", label, s.total, s.unique, s.dupes)
	if len(s.latency) > 0 {
		sort.Slice(s.latency, func(i, j int) bool { return s.latency[i] < s.latency[j] })
		p := func(q float64) time.Duration {
			i := int(float64(len(s.latency)-1) * q)
			return s.latency[i]
		}
		fmt.Printf("%s: latency min=%s p50=%s p95=%s max=%s (n=%d)\n",
			label, s.latency[0], p(0.50), p(0.95), s.latency[len(s.latency)-1], len(s.latency))
	}
	if len(s.tags) > 0 {
		var parts []string
		for vid, pcps := range s.tags {
			for pcp, n := range pcps {
				parts = append(parts, fmt.Sprintf("vid=%d pcp=%d (n=%d)", vid, pcp, n))
			}
		}
		sort.Strings(parts)
		fmt.Printf("%s: observed tags: %s\n", label, strings.Join(parts, ", "))
	}
}

// buildFrame assembles a GOOSE-like Ethernet frame with optional VLAN
// tag. Payload: appid(2) seq(4) txns(8).
//
// vid=-1 means untagged. vid>=0 adds an 802.1Q tag carrying the given
// VLAN ID and PCP. The IEC 61850 legacy default — VLAN ID 0 with priority
// tagging (PCP 4) — is vid=0, pcp=4: the tag carries QoS priority bits
// without assigning the frame to any named VLAN, and transit switches use
// the PCP for hardware queueing.
func buildFrame(appid uint16, seq uint32, vid, pcp int) []byte {
	payload := make([]byte, payloadLen)
	binary.BigEndian.PutUint16(payload[0:2], appid)
	binary.BigEndian.PutUint32(payload[2:6], seq)
	binary.BigEndian.PutUint64(payload[6:14], uint64(time.Now().UnixNano()))

	tagged := vid >= 0
	var frame []byte
	if tagged {
		frame = make([]byte, 14+4+payloadLen)
	} else {
		frame = make([]byte, 14+payloadLen)
	}
	copy(frame[0:6], dstMAC)
	copy(frame[6:12], srcMAC)
	if tagged {
		frame[12], frame[13] = 0x81, 0x00 // 802.1Q
		tci := uint16(pcp&0x07)<<13 | uint16(vid&0x0FFF)
		binary.BigEndian.PutUint16(frame[14:16], tci)
		binary.BigEndian.PutUint16(frame[16:18], etGOOSE)
		copy(frame[18:18+payloadLen], payload)
	} else {
		binary.BigEndian.PutUint16(frame[12:14], etGOOSE)
		copy(frame[14:14+payloadLen], payload)
	}
	return frame
}

// parseFrame extracts (appid, seq, txns, vid, pcp) from a received frame.
// Returns ok=false when the frame is not one of ours (wrong ethertype/appid).
func parseFrame(frame []byte, wantAppid uint16) (appid uint16, seq uint32, sentAt time.Time, vid, pcp int, ok bool) {
	if len(frame) < 14+payloadLen {
		return 0, 0, time.Time{}, -1, 0, false
	}
	et := binary.BigEndian.Uint16(frame[12:14])
	off := 14
	vid, pcp = -1, 0
	if et == 0x8100 || et == 0x88a8 { // VLAN tag
		if len(frame) < 18+payloadLen {
			return 0, 0, time.Time{}, -1, 0, false
		}
		tci := binary.BigEndian.Uint16(frame[14:16])
		vid = int(tci & 0x0FFF)
		pcp = int((tci >> 13) & 0x07)
		et = binary.BigEndian.Uint16(frame[16:18])
		off = 18
	}
	if et != etGOOSE && et != etSV {
		return 0, 0, time.Time{}, -1, 0, false
	}
	appid = binary.BigEndian.Uint16(frame[off : off+2])
	if wantAppid != 0 && appid != wantAppid {
		return 0, 0, time.Time{}, -1, 0, false
	}
	seq = binary.BigEndian.Uint32(frame[off+2 : off+6])
	txns := binary.BigEndian.Uint64(frame[off+6 : off+14])
	return appid, seq, time.Unix(0, int64(txns)), vid, pcp, true
}

func openSocket(iface string) (int, int, error) {
	ifIndex, err := net.InterfaceByName(iface)
	if err != nil {
		return 0, 0, err
	}
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, int(htons(0x0003)))
	if err != nil {
		return 0, 0, err
	}
	addr := &unix.RawSockaddrLinklayer{Family: unix.AF_PACKET, Ifindex: int32(ifIndex.Index)}
	if _, _, errno := unix.Syscall(unix.SYS_BIND, uintptr(fd),
		uintptr(unsafe.Pointer(addr)), uintptr(unix.SizeofSockaddrLinklayer)); errno != 0 {
		unix.Close(fd)
		return 0, 0, errno
	}
	unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, 512*1024)
	// VLAN RX offload strips 802.1Q tags and returns them as ancillary
	// data; without PACKET_AUXDATA the receiver would never see the tag.
	// (Same mechanism as prpd's RawSocket.)
	unix.SetsockoptInt(fd, unix.SOL_PACKET, unix.PACKET_AUXDATA, 1)
	return fd, ifIndex.Index, nil
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

func sendFrame(fd int, frame []byte) error {
	_, _, errno := unix.Syscall6(unix.SYS_SENDTO, uintptr(fd),
		uintptr(unsafe.Pointer(&frame[0])), uintptr(len(frame)), 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func main() {
	mode := flag.String("mode", "ping", "send | recv | ping")
	iface := flag.String("iface", "eth0", "interface name")
	appid := flag.Uint("appid", 0x1001, "GOOSE appid (hex)")
	count := flag.Int("count", 100, "frames to send (send/ping)")
	rate := flag.Float64("rate", 100, "frames per second (send/ping)")
	duration := flag.Duration("duration", 15*time.Second, "listen duration (recv)")
	vid := flag.Int("vid", -1, "VLAN ID to tag frames with (-1 = untagged); IEC 61850 legacy default is 0 (null VLAN) with priority tagging")
	pcp := flag.Int("pcp", 4, "VLAN priority (PCP 0-7) to tag frames with; IEC 61850 baseline default is 4")
	burst := flag.Bool("burst", false, "GOOSE-style retransmit burst spacing (send)")
	flag.Parse()

	appidV := uint16(*appid)

	switch *mode {
	case "send":
		send(*iface, appidV, *count, *rate, *vid, *pcp, *burst)
	case "recv":
		recv(*iface, appidV, *duration)
	default: // ping
		pingMode(*iface, appidV, *count, *rate, *vid, *pcp)
	}
}

// send transmits count frames, optionally with GOOSE-style retransmit
// bursts (each logical event retransmitted at T+2,T+4,T+8...ms with a
// NEW seq number, exactly like a real IED).
func send(ifaceName string, appid uint16, count int, rate float64, vid, pcp int, burst bool) {
	fd, _, err := openSocket(ifaceName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", ifaceName, err)
		os.Exit(1)
	}
	defer unix.Close(fd)

	interval := time.Duration(float64(time.Second) / rate)
	fmt.Printf("send: %d frames @ %.0f Hz, vid=%d pcp=%d, burst=%v\n", count, rate, vid, pcp, burst)
	start := time.Now()
	seq := uint32(1)
	for i := 0; i < count; i++ {
		seq++
		frame := buildFrame(appid, seq, vid, pcp)
		if err := sendFrame(fd, frame); err != nil {
			fmt.Fprintf(os.Stderr, "send: %v\n", err)
			os.Exit(1)
		}
		if burst {
			// Retransmit burst: 2,4,8,16..ms — each with its own seq.
			d := 2 * time.Millisecond
			for j := 0; j < 3; j++ {
				time.Sleep(d)
				seq++
				frame := buildFrame(appid, seq, vid, pcp)
				sendFrame(fd, frame)
				d *= 2
			}
		} else {
			time.Sleep(interval)
		}
	}
	fmt.Printf("send: done in %s\n", time.Since(start).Round(time.Millisecond))
}

// recv listens for the appid and reports uniqueness/dupes/latency.
func recv(ifaceName string, appid uint16, dur time.Duration) {
	fd, _, err := openSocket(ifaceName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", ifaceName, err)
		os.Exit(1)
	}
	defer unix.Close(fd)

	fmt.Printf("recv: listening on %s for appid 0x%04x for %s\n", ifaceName, appid, dur)
	s := &stats{}
	buf := make([]byte, 2048)
	oob := make([]byte, 128)
	deadline := time.Now().Add(dur)
	for time.Now().Before(deadline) {
		nr, oobn, _, _, err := unix.Recvmsg(fd, buf, oob, 0)
		if err != nil {
			if err == unix.EAGAIN || err == unix.EINTR {
				continue
			}
			fmt.Fprintf(os.Stderr, "read: %v\n", err)
			os.Exit(1)
		}
		// Reconstruct the VLAN tag stripped by RX offload so parseFrame
		// sees vid/pcp as they appear on the wire.
		nr = restoreVLANTag(buf, nr, oob[:oobn])
		_, seq, txns, vid, pcp, ok := parseFrame(buf[:nr], appid)
		if !ok {
			continue
		}
		s.noteFrame(seq, txns)
		s.noteTagged(vid, pcp)
	}
	s.report("recv")
	os.Exit(0)
}

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

// pingMode sends count frames then listens for them to come back (they
// must cross the PRP network to the peer and back via the SAN nets).
func pingMode(ifaceName string, appid uint16, count int, rate float64, vid, pcp int) {
	fd, _, err := openSocket(ifaceName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", ifaceName, err)
		os.Exit(1)
	}
	defer unix.Close(fd)

	fmt.Printf("ping: %d frames @ %.0f Hz, vid=%d pcp=%d\n", count, rate, vid, pcp)
	s := &stats{}
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 2048)
		for {
			select {
			case <-done:
				return
			default:
			}
			nr, err := unix.Read(fd, buf)
			if err != nil {
				continue
			}
			_, seq, txns, _, _, ok := parseFrame(buf[:nr], appid)
			if !ok {
				continue
			}
			s.noteFrame(seq, txns)
		}
	}()

	interval := time.Duration(float64(time.Second) / rate)
	for i := 0; i < count; i++ {
		frame := buildFrame(appid, uint32(i+1), vid, pcp)
		if err := sendFrame(fd, frame); err != nil {
			fmt.Fprintf(os.Stderr, "send: %v\n", err)
			os.Exit(1)
		}
		time.Sleep(interval)
	}
	// Give echoes time to return.
	time.Sleep(5 * time.Second)
	close(done)
	s.report("ping")
}
