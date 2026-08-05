// trafficgen — GOOSE / Sampled Values (IEC 61850) traffic generator for
// the PRP simulator. Runs on Linux (AF_PACKET raw socket) and Windows
// (Npcap wpcap.dll).
//
// It can run in these modes:
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
//	    --count 100 --rate 100 --vid 0 --pcp 4 --burst
//
//	# On Windows, point --iface at a physical NIC (by Npcap name, 1-based
//	# index, or a substring of the NIC's friendly description):
//	trafficgen-windows-amd64.exe --mode send --iface "Ethernet" --loop \
//	    --appid 0x1001 --rate 5 --vid 0 --pcp 4 --burst
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"prp-gns3/internal/iec"
	"prp-gns3/internal/tui"
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

	// Frame header: APPID(2), Length(2), Reserved(4), then real APDU.
	payloadHeaderLen = 8
)

var dstMAC = []byte{0x01, 0x0c, 0xcd, 0x01, 0x00, 0x01}
var srcMAC = []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}

type stats struct {
	total    int
	unique   int
	dupes    int
	latency  []time.Duration
	lastKey  string
	lastSeen map[string]bool
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

func (s *stats) noteFrame(key string, sentAt time.Time) {
	s.total++
	if sentAt.IsZero() {
		if s.lastSeen[key] {
			s.dupes++
		} else {
			s.unique++
			if s.lastSeen == nil {
				s.lastSeen = make(map[string]bool)
			}
			s.lastSeen[key] = true
			s.lastKey = key
		}
		return
	}
	lat := time.Since(sentAt)
	if lat > 0 {
		s.latency = append(s.latency, lat)
	}
	if s.lastSeen[key] {
		s.dupes++
		return
	}
	if s.lastSeen == nil {
		s.lastSeen = make(map[string]bool)
	}
	s.lastSeen[key] = true
	s.unique++
	s.lastKey = key
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
func buildFrame(appid uint16, stNum uint16, sqNum uint16, vid, pcp int) []byte {
	et := uint16(etGOOSE)
	apdu := iec.BuildGOOSEAPDU(stNum, sqNum, time.Now(), false, 1, "LLN0$GO$gcb0", "LLN0$dataset", "LLN0$GO$gcb0")
	return assembleFrame(appid, apdu, vid, pcp, et)
}

// buildFrameEt is buildFrame with a configurable EtherType (GOOSE 0x88B8
// or SV 0x88BA) and destination MAC, so the same tool generates Sampled
// Values streams too.
func buildFrameEt(appid uint16, stNum uint16, sqNum uint16, vid, pcp int, et uint16, dst []byte) []byte {
	var apdu []byte
	if et == etGOOSE {
		apdu = iec.BuildGOOSEAPDU(stNum, sqNum, time.Now(), false, 1, "LLN0$GO$gcb0", "LLN0$dataset", "LLN0$GO$gcb0")
	} else {
		apdu = iec.BuildSVAPDU(stNum, 1, "MU01")
	}
	return assembleFrame(appid, apdu, vid, pcp, et)
}

func assembleFrame(appid uint16, apdu []byte, vid, pcp int, et uint16) []byte {
	// Ethernet header + GOOSE/SV header (8 bytes) + APDU
	header := make([]byte, payloadHeaderLen)
	binary.BigEndian.PutUint16(header[0:2], appid)
	binary.BigEndian.PutUint16(header[2:4], uint16(len(apdu)+8)) // Length = header + APDU; must be >=8
	binary.BigEndian.PutUint16(header[4:6], 0) // Reserved 1
	binary.BigEndian.PutUint16(header[6:8], 0) // Reserved 2
	tagged := vid >= 0
	var frame []byte
	if tagged {
		frame = make([]byte, 14+4+len(header)+len(apdu))
	} else {
		frame = make([]byte, 14+len(header)+len(apdu))
	}
	copy(frame[0:6], dstMAC)
	copy(frame[6:12], srcMAC)
	if tagged {
		frame[12], frame[13] = 0x81, 0x00
		tci := uint16(pcp&0x07)<<13 | uint16(vid&0x0FFF)
		binary.BigEndian.PutUint16(frame[14:16], tci)
		binary.BigEndian.PutUint16(frame[16:18], et)
		off := 18
		copy(frame[off:off+len(header)], header)
		copy(frame[off+len(header):], apdu)
	} else {
		binary.BigEndian.PutUint16(frame[12:14], et)
		copy(frame[14:14+len(header)], header)
		copy(frame[14+len(header):], apdu)
	}
	return frame
}

// parseFrame extracts (appid, seq, txns, vid, pcp) from a received frame.
// Returns ok=false when the frame is not one of ours (wrong ethertype/appid).
func parseFrame(frame []byte, wantAppid uint16) (appid uint16, stNum uint16, sqNum uint16, smpCnt uint16, sentAt time.Time, allData bool, vid, pcp int, ok bool) {
	if len(frame) < 14+payloadHeaderLen {
		return 0, 0, 0, 0, time.Time{}, false, -1, 0, false
	}
	et := binary.BigEndian.Uint16(frame[12:14])
	off := 14
	vid, pcp = -1, 0
	if et == 0x8100 || et == 0x88a8 {
		if len(frame) < 18+payloadHeaderLen {
			return 0, 0, 0, 0, time.Time{}, false, -1, 0, false
		}
		tci := binary.BigEndian.Uint16(frame[14:16])
		vid = int(tci & 0x0FFF)
		pcp = int((tci >> 13) & 0x07)
		et = binary.BigEndian.Uint16(frame[16:18])
		off = 18
	}
	if et != etGOOSE && et != etSV {
		return 0, 0, 0, 0, time.Time{}, false, -1, 0, false
	}
	if len(frame) < off+payloadHeaderLen {
		return 0, 0, 0, 0, time.Time{}, false, -1, 0, false
	}
	appid = binary.BigEndian.Uint16(frame[off : off+2])
	if wantAppid != 0 && appid != wantAppid {
		return 0, 0, 0, 0, time.Time{}, false, -1, 0, false
	}
	apdu := frame[off+payloadHeaderLen:]
	if et == etGOOSE {
		stNum, sqNum, sentAt, allData, okParse := iec.ParseGOOSEAPDU(apdu)
		return appid, stNum, sqNum, 0, sentAt, allData, vid, pcp, okParse
	} else {
		smpCnt, okParse := iec.ParseSVAPDU(apdu)
		return appid, 0, 0, smpCnt, time.Time{}, false, vid, pcp, okParse
	}
}

// parseMAC parses "aa:bb:cc:dd:ee:ff" into 6 bytes.
func parseMAC(s string) ([]byte, error) {
	if len(s) != 17 {
		return nil, fmt.Errorf("bad MAC %q (want aa:bb:cc:dd:ee:ff)", s)
	}
	out := make([]byte, 6)
	for i := 0; i < 6; i++ {
		var b int
		if _, err := fmt.Sscanf(s[i*3:i*3+2], "%x", &b); err != nil {
			return nil, fmt.Errorf("bad MAC %q", s)
		}
		out[i] = byte(b)
	}
	return out, nil
}

func main() {
	mode := flag.String("mode", "ping", "send | recv | ping")
	iface := flag.String("iface", "eth0", "interface name (Linux: eth0; Windows: Npcap name, 1-based index, or NIC description substring)")
	appid := flag.Uint("appid", 0x1001, "GOOSE appid (hex)")
	count := flag.Int("count", 100, "frames to send (send/ping)")
	rate := flag.Float64("rate", 100, "frames per second (send/ping)")
	duration := flag.Duration("duration", 15*time.Second, "listen duration (recv)")
	vid := flag.Int("vid", -1, "VLAN ID to tag frames with (-1 = untagged); IEC 61850 legacy default is 0 (null VLAN) with priority tagging")
	pcp := flag.Int("pcp", 4, "VLAN priority (PCP 0-7) to tag frames with; IEC 61850 baseline default is 4")
	burst := flag.Bool("burst", false, "GOOSE-style retransmit burst spacing (send)")
	loop := flag.Bool("loop", false, "run continuously until Ctrl+C (send/recv)")
	et := flag.String("et", "goose", "ethertype to send: goose (0x88B8) | sv (0x88BA)")
	mcast := flag.String("mcast", "", "destination MAC for send (default: 01:0c:cd:01:00:01 for goose, 01:0c:cd:04:00:00 for sv)")
	src := flag.String("src-mac", "02:00:00:00:00:01", "source MAC for send")
	list := flag.Bool("list-devices", false, "list available interfaces and exit")
	flag.Parse()

	if *list {
		listDevices()
		os.Exit(0)
	}

	var etV uint16
	switch strings.ToLower(*et) {
	case "goose":
		etV = etGOOSE
		if *mcast == "" {
			*mcast = "01:0c:cd:01:00:01"
		}
	case "sv":
		etV = etSV
		if *mcast == "" {
			*mcast = "01:0c:cd:04:00:00"
		}
	default:
		fmt.Fprintf(os.Stderr, "bad --et %q (want goose or sv)\n", *et)
		os.Exit(1)
	}
	dst, err := parseMAC(*mcast)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad --mcast: %v\n", err)
		os.Exit(1)
	}
	srcBytes, err := parseMAC(*src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad --src-mac: %v\n", err)
		os.Exit(1)
	}
	srcMAC = srcBytes
	dstMAC = dst

	appidV := uint16(*appid)

	switch *mode {
	case "send":
		send(*iface, appidV, *count, *rate, *vid, *pcp, *burst, *loop, etV)
	case "recv":
		recv(*iface, appidV, *duration, *loop)
	default: // ping
		pingMode(*iface, appidV, *count, *rate, *vid, *pcp)
	}
}

// stopChan returns a channel closed on SIGINT/SIGTERM.
func stopChan() <-chan struct{} {
	ch := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		close(ch)
	}()
	return ch
}

// send transmits count frames (or until Ctrl+C with --loop), optionally
// with GOOSE-style retransmit bursts (each logical event retransmitted at
// T+2,T+4,T+8...ms with a NEW seq number, exactly like a real IED).
func send(ifaceName string, appid uint16, count int, rate float64, vid, pcp int, burst, loop bool, et uint16) {
	sock, err := openSocket(ifaceName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", ifaceName, err)
		os.Exit(1)
	}
	defer sock.Close()

	fmt.Printf("send: %d frames @ %.0f Hz, vid=%d pcp=%d, burst=%v, et=0x%04x\n", count, rate, vid, pcp, burst, et)
	tui.Start("publisher", ifaceName, appid)
	defer tui.Stop()
	start := time.Now()
	stNum := uint16(1)
	sqNum := uint16(1)
	if loop {
		count = int(^uint(0) >> 1) // huge; run until signal
	}
	stop := stopChan()
	interval := time.Duration(float64(time.Second) / rate)
	sent := 0
	for i := 0; i < count; i++ {
		select {
		case <-stop:
			fmt.Printf("send: stopped by signal after %d frames in %s\n", sent, time.Since(start).Round(time.Millisecond))
			return
		default:
		}
		sent++
		stNum++ // new event: state increments
		sqNum = 1 // reset retransmission counter
		frame := buildFrameEt(appid, stNum, sqNum, vid, pcp, et, dstMAC)
		if err := sock.Send(frame); err != nil {
			fmt.Fprintf(os.Stderr, "send: %v\n", err)
			os.Exit(1)
		}
		tui.Update(stNum, false, sent, sent, 0, 0, nil)
		if burst {
			// Retransmit burst: 2,4,8,16..ms — each with its own seq.
			d := 2 * time.Millisecond
			for j := 0; j < 3; j++ {
				select {
				case <-stop:
					fmt.Printf("send: stopped by signal after %d frames in %s\n", sent, time.Since(start).Round(time.Millisecond))
					return
				default:
				}
				time.Sleep(d)
				sqNum++
				sent++
				frame := buildFrameEt(appid, stNum, sqNum, vid, pcp, et, dstMAC)
				if err := sock.Send(frame); err != nil {
					fmt.Fprintf(os.Stderr, "send: %v\n", err)
					os.Exit(1)
				}
				d *= 2
			}
			// After a burst, keep the overall event rate: sleep the
			// remainder of the interval.
			if !loop {
				time.Sleep(interval)
			}
		} else {
			time.Sleep(interval)
		}
	}
	fmt.Printf("send: done in %s (%d frames)\n", time.Since(start).Round(time.Millisecond), sent)
}

// recv listens for the appid and reports uniqueness/dupes/latency.
// With --loop it keeps listening until Ctrl+C.
func recv(ifaceName string, appid uint16, dur time.Duration, loop bool) {
	sock, err := openSocket(ifaceName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", ifaceName, err)
		os.Exit(1)
	}
	defer sock.Close()
	sock.SetRecvTimeout(1 * time.Second)

	fmt.Printf("recv: listening on %s for appid 0x%04x", ifaceName, appid)
	if loop {
		fmt.Printf(" (until Ctrl+C)")
	} else {
		fmt.Printf(" for %s", dur)
	}
	fmt.Println()
	tui.Start("subscriber", ifaceName, appid)
	defer tui.Stop()
	s := &stats{}
	buf := make([]byte, 2048)
	stop := stopChan()
	deadline := time.Now().Add(dur)
	lastReport := time.Now()
	for {
		if !loop && time.Now().After(deadline) {
			break
		}
		select {
		case <-stop:
			s.report("recv")
			os.Exit(0)
		default:
		}
		nr, err := sock.Recv(buf)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read: %v\n", err)
			os.Exit(1)
		}
		if nr == 0 {
			// Poll: report periodically in loop mode.
			if loop && time.Since(lastReport) > 10*time.Second {
				fmt.Printf("recv: partial %s\n", s.partial())
				lastReport = time.Now()
			}
			continue
		}
		appidVal, stNum, sqNum, smpCntVal, txns, allData, vid, pcp, ok := parseFrame(buf[:nr], appid)
		if !ok {
			continue
		}
		_ = allData; _ = vid; _ = pcp; _ = smpCntVal; _ = appidVal
		key := fmt.Sprintf("%d|%d", stNum, sqNum)
		s.noteFrame(key, txns)
		tui.Update(stNum, allData, s.total, s.unique, s.dupes, 0, nil)
		s.noteTagged(vid, pcp)
	}
	s.report("recv")
	os.Exit(0)
}

// partial returns a compact one-line live summary for loop mode.
func (s *stats) partial() string {
	return fmt.Sprintf("total=%d unique=%d dupes=%d", s.total, s.unique, s.dupes)
}

// pingMode sends count frames then listens for them to come back (they
// must cross the PRP network to the peer and back via the SAN nets).
func pingMode(ifaceName string, appid uint16, count int, rate float64, vid, pcp int) {
	sock, err := openSocket(ifaceName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", ifaceName, err)
		os.Exit(1)
	}
	defer sock.Close()
	sock.SetRecvTimeout(500 * time.Millisecond)

	fmt.Printf("ping: %d frames @ %.0f Hz, vid=%d pcp=%d\n", count, rate, vid, pcp)
	s := &stats{}
	stop := stopChan()
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 2048)
		for {
			select {
			case <-done:
				return
			default:
			}
			nr, err := sock.Recv(buf)
			if err != nil {
				continue
			}
			if nr == 0 {
				continue
			}
			_, _, _, _, _, _, _, _, ok := parseFrame(buf[:nr], appid)
			if !ok {
				continue
			}
			s.noteFrame("ping", time.Now())
		}
	}()

	interval := time.Duration(float64(time.Second) / rate)
	for i := 0; i < count; i++ {
		select {
		case <-stop:
			break
		default:
		}
		frame := buildFrame(appid, uint16(i+1), 1, vid, pcp)
		if err := sock.Send(frame); err != nil {
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
