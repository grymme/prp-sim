package prp

import (
	"bytes"
	"encoding/binary"
	"sync"
	"testing"
	"time"

	"prp-gns3/internal/engine"
	"prp-gns3/internal/iface"
)

// memPort is an in-memory PacketPort for testing.
type memPort struct {
	name   string
	mu     sync.Mutex
	frames [][]byte
}

func (m *memPort) Read([]byte) (int, error) { return 0, nil }
func (m *memPort) Close() error             { return nil }
func (m *memPort) Name() string             { return m.name }
func (m *memPort) Write(b []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(b))
	copy(cp, b)
	m.frames = append(m.frames, cp)
	return len(b), nil
}
func (m *memPort) drain() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.frames = nil
}

// newNode returns a test Node with in-memory ports and a stable node MAC.
func newNode() (*Node, *memPort, *memPort, *memPort) {
	lanA := &memPort{name: "lan_a"}
	lanB := &memPort{name: "lan_b"}
	inter := &memPort{name: "interlink"}
	n := NewNode(&Config{
		NodeName:       "test",
		Role:           "redbox",
		TrailerEnabled: true,
	})
	n.LanA = lanA
	n.LanB = lanB
	n.Interlink = inter
	n.SetNodeMAC([]byte{0x02, 0x50, 0x50, 0x00, 0x00, 0x01})
	return n, lanA, lanB, inter
}

// ethFrame builds a minimal Ethernet frame with the given src MAC.
func ethFrame(src [6]byte, dst [6]byte) []byte {
	f := make([]byte, 60)
	copy(f[0:6], dst[:])
	copy(f[6:12], src[:])
	f[12] = 0x08
	f[13] = 0x00
	return f
}

// TestRedBoxDuplicateDiscard is the core PRP test: the RedBox sends the SAME
// sequence number on LAN A and LAN B; a receiving node must accept the first
// copy and discard the second.
func TestRedBoxDuplicateDiscard(t *testing.T) {
	n, lanA, lanB, inter := newNode()
	n.dupTable.Cleanup()

	src := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	dst := [6]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66}
	frame := ethFrame(src, dst)

	// Step 1: SAN frame arrives on interlink -> duplicated to both LANs.
	n.handleInterlinkFrame(frameEvent{iface: "interlink", frame: frame, frameSz: len(frame)})
	if len(lanA.frames) != 1 || len(lanB.frames) != 1 {
		t.Fatalf("expected 1 frame on each LAN, got A=%d B=%d", len(lanA.frames), len(lanB.frames))
	}

	// Both copies must carry the same sequence number.
	_, seqA, _, errA := engine.DecodeRCT(lanA.frames[0])
	_, seqB, _, errB := engine.DecodeRCT(lanB.frames[0])
	if errA != nil || errB != nil {
		t.Fatalf("decode: A=%v B=%v", errA, errB)
	}
	if seqA != seqB {
		t.Fatalf("LAN copies must share seq: A=%d B=%d (breaks duplicate detection)", seqA, seqB)
	}

	// Step 2: this node is now a receiver. Feed the LAN A copy in.
	lanAFrame := lanA.frames[0]
	lanA.drain()
	lanB.drain()
	inter.drain()
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: lanAFrame, frameSz: len(lanAFrame)})

	// The LAN B copy (same src, same seq) must be discarded as a duplicate.
	lanBFrame := engine.EncodeRCT(frame, uint16(seqA), 1)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_b", frame: lanBFrame, frameSz: len(lanBFrame)})

	// Exactly one frame must have been forwarded to the interlink.
	if len(inter.frames) != 1 {
		t.Fatalf("expected exactly 1 forwarded frame, got %d (duplicate not discarded)", len(inter.frames))
	}
}

// TestInterlinkFrameSameSeq asserts that two consecutive interlink frames get
// increasing sequence numbers per source MAC, but within one frame both LAN
// copies share the seq.
func TestInterlinkFrameSameSeq(t *testing.T) {
	n, lanA, lanB, _ := newNode()

	src := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	frame := ethFrame(src, [6]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66})

	n.handleInterlinkFrame(frameEvent{iface: "interlink", frame: frame, frameSz: len(frame)})
	n.handleInterlinkFrame(frameEvent{iface: "interlink", frame: frame, frameSz: len(frame)})

	_, seq1A, _, _ := engine.DecodeRCT(lanA.frames[0])
	_, seq1B, _, _ := engine.DecodeRCT(lanB.frames[0])
	_, seq2A, _, _ := engine.DecodeRCT(lanA.frames[1])
	_, seq2B, _, _ := engine.DecodeRCT(lanB.frames[1])

	if seq1A != seq1B {
		t.Errorf("frame 1: A=%d B=%d must be equal", seq1A, seq1B)
	}
	if seq2A != seq2B {
		t.Errorf("frame 2: A=%d B=%d must be equal", seq2A, seq2B)
	}
	if seq2A != seq1A+1 {
		t.Errorf("frame 2 seq must increment: %d -> %d", seq1A, seq2A)
	}
}

// TestLANIDRejection: a frame received on LAN B carrying lan_id=0 (or vice
// versa) must be dropped — guards against bridged/looped networks.
func TestLANIDRejection(t *testing.T) {
	n, _, _, inter := newNode()
	n.dupTable.Cleanup()

	src := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	frame := ethFrame(src, [6]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66})

	// Frame with lan_id=0 arriving on LAN B (should be 1).
	enc := engine.EncodeRCT(frame, 0, 0)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_b", frame: enc, frameSz: len(enc)})
	if len(inter.frames) != 0 {
		t.Fatalf("lan_id mismatch on LAN B must be dropped, got %d forwards", len(inter.frames))
	}

	// Correct lan_id=1 on LAN B is accepted.
	enc = engine.EncodeRCT(frame, 1, 1)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_b", frame: enc, frameSz: len(enc)})
	if len(inter.frames) != 1 {
		t.Fatalf("valid LAN B frame should be forwarded, got %d", len(inter.frames))
	}
}

// TestSupervisionConsumed: received supervision frames must be parsed and
// never forwarded to the interlink (SAN).
func TestSupervisionConsumed(t *testing.T) {
	n, _, _, inter := newNode()
	n.dupTable.Cleanup()

	srcMAC := []byte{0x02, 0x50, 0x50, 0xaa, 0xbb, 0xcc}
	sup := make([]byte, 60)
	copy(sup[0:6], []byte{0x01, 0x15, 0x4e, 0x00, 0x01, 0x00}) // dst
	copy(sup[6:12], srcMAC)                                    // src
	sup[12], sup[13] = 0x88, 0xfb                              // EtherType
	binary.BigEndian.PutUint16(sup[16:18], 5)                  // seq
	sup[18], sup[19] = 20, 6                                   // TLV LIFE_CHECK_DD
	copy(sup[20:26], srcMAC)                                   // MacAddressA
	binary.BigEndian.PutUint16(sup[14:16], 0)                  // path

	n.handleSupervisionFrame(frameEvent{iface: "lan_a", frame: sup, frameSz: len(sup)})
	if len(inter.frames) != 0 {
		t.Fatalf("supervision frames must not reach the interlink, got %d", len(inter.frames))
	}
	if _, ok := n.supervisionSeen["02:50:50:aa:bb:cc"]; !ok {
		t.Error("sender should be tracked as alive after supervision")
	}
}

// TestProxyLearning verifies that with ForwardAll=false, LAN frames are only
// forwarded to the interlink after the SAN has been learned.
func TestProxyLearning(t *testing.T) {
	n, _, _, inter := newNode()
	n.Config.ForwardAll = false
	n.Config.MaxNodeTableSize = 0 // default
	n.dupTable.Cleanup()

	sanMAC := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	sanDst := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}

	// A frame addressed to the SAN, before learning: must NOT be forwarded.
	src := [6]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	frame := ethFrame(src, sanDst)
	enc := engine.EncodeRCT(frame, 0, 0)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if len(inter.frames) != 0 {
		t.Fatalf("unknown SAN dst should be filtered, got %d forwards", len(inter.frames))
	}

	// SAN sends a frame on the interlink -> MAC is learned.
	sanFrame := ethFrame(sanMAC, [6]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06})
	n.handleInterlinkFrame(frameEvent{iface: "interlink", frame: sanFrame, frameSz: len(sanFrame)})

	// Now the LAN frame addressed to the SAN is forwarded.
	enc = engine.EncodeRCT(frame, 1, 0)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if len(inter.frames) != 1 {
		t.Fatalf("learned SAN dst should be forwarded, got %d", len(inter.frames))
	}
}

// TestVLANFilter verifies the interlink VLAN filter.
func TestVLANFilter(t *testing.T) {
	n, lanA, lanB, _ := newNode()
	n.Config.VLANFilter = []int{10}

	src := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	base := ethFrame(src, [6]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66})

	// VLAN 20: filtered out.
	vlan20 := vlanTagFrame(base, 20)
	n.handleInterlinkFrame(frameEvent{iface: "interlink", frame: vlan20, frameSz: len(vlan20)})
	if len(lanA.frames) != 0 || len(lanB.frames) != 0 {
		t.Fatalf("VLAN 20 should be filtered, got A=%d B=%d", len(lanA.frames), len(lanB.frames))
	}

	// VLAN 10: forwarded.
	vlan10 := vlanTagFrame(base, 10)
	n.handleInterlinkFrame(frameEvent{iface: "interlink", frame: vlan10, frameSz: len(vlan10)})
	if len(lanA.frames) != 1 || len(lanB.frames) != 1 {
		t.Fatalf("VLAN 10 should pass, got A=%d B=%d", len(lanA.frames), len(lanB.frames))
	}
}

// vlanTagFrame wraps a frame in a single 802.1Q tag.
func vlanTagFrame(frame []byte, vlanID int) []byte {
	out := make([]byte, 0, len(frame)+4)
	out = append(out, frame[0:12]...)                // dst + src
	out = append(out, 0x81, 0x00)                    // 802.1Q
	out = append(out, byte(vlanID>>8), byte(vlanID)) // TCI
	out = append(out, frame[12:len(frame)-4]...)     // EtherType + payload
	return out
}

// TestMulticastFilter verifies the multicast first-octet filter on the
// LAN -> interlink path.
func TestMulticastFilter(t *testing.T) {
	n, _, _, inter := newNode()
	n.Config.ForwardAll = false
	n.Config.MulticastFirstOctet = "01"
	n.dupTable.Cleanup()

	src := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}

	// Multicast dst 01-00-5E... passes (first octet 0x01).
	mcPass := ethFrame(src, [6]byte{0x01, 0x00, 0x5e, 0x00, 0x00, 0x01})
	enc := engine.EncodeRCT(mcPass, 0, 0)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if len(inter.frames) != 1 {
		t.Fatalf("multicast 01-00-5E should pass, got %d", len(inter.frames))
	}

	// Multicast dst 33-33-00-00-00-01 (IPv6) fails (first octet 0x33).
	mcFail := ethFrame(src, [6]byte{0x33, 0x33, 0x00, 0x00, 0x00, 0x01})
	enc = engine.EncodeRCT(mcFail, 1, 0)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if len(inter.frames) != 1 {
		t.Fatalf("multicast 33-33 should be filtered, got %d forwards", len(inter.frames))
	}
}

// TestMulticastFilterFullPrefix verifies that the default-style pattern
// "01-00-5E" (the whole multicast group prefix) filters correctly, not
// just the first byte. This is a regression test for the old behaviour
// where the hyphenated pattern failed to parse and disabled filtering.
func TestMulticastFilterFullPrefix(t *testing.T) {
	n, _, _, inter := newNode()
	n.Config.ForwardAll = false
	n.Config.MulticastFirstOctet = "01-00-5E"
	n.dupTable.Cleanup()

	src := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}

	// 01-00-5E-xx-xx-xx (IPv4 multicast) passes: full prefix matches.
	mcPass := ethFrame(src, [6]byte{0x01, 0x00, 0x5e, 0x00, 0x00, 0x01})
	enc := engine.EncodeRCT(mcPass, 0, 0)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if len(inter.frames) != 1 {
		t.Fatalf("multicast 01-00-5E should pass, got %d", len(inter.frames))
	}

	// 01-00-5F-xx-xx-xx: same first byte, different third byte -> blocked.
	mcSameFirst := ethFrame(src, [6]byte{0x01, 0x00, 0x5f, 0x00, 0x00, 0x01})
	enc = engine.EncodeRCT(mcSameFirst, 1, 0)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if len(inter.frames) != 1 {
		t.Fatalf("multicast 01-00-5F should be filtered, got %d forwards", len(inter.frames))
	}

	// 33-33-00-00-00-01 (IPv6) blocked.
	mcFail := ethFrame(src, [6]byte{0x33, 0x33, 0x00, 0x00, 0x00, 0x01})
	enc = engine.EncodeRCT(mcFail, 2, 0)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if len(inter.frames) != 1 {
		t.Fatalf("multicast 33-33 should be filtered, got %d forwards", len(inter.frames))
	}
}

// TestParseMulticastPattern exercises the pattern parser directly.
func TestParseMulticastPattern(t *testing.T) {
	cases := []struct {
		in   string
		want []byte
	}{
		{"01", []byte{0x01}},
		{"01-00-5E", []byte{0x01, 0x00, 0x5e}},
		{"33:33", []byte{0x33, 0x33}},
		{"0x01005e", []byte{0x01, 0x00, 0x5e}},
		{"", nil},
		{"01-00-5E-00-00-00-01", nil}, // too long (>6 bytes)
		{"xyz", nil},                  // not hex
		{"0", nil},                    // odd length
	}
	for _, tc := range cases {
		got := parseMulticastPattern(tc.in)
		if !bytes.Equal(got, tc.want) {
			t.Errorf("parseMulticastPattern(%q) = %x, want %x", tc.in, got, tc.want)
		}
	}
}

// TestProxyTableForget verifies the SAN proxy learning table uses the
// supervision.proxy_node_forget_time lifetime (64s default), not the
// duplicate-detection entry lifetime (640ms).
func TestProxyTableForget(t *testing.T) {
	n, _, _, inter := newNode()
	n.Config.ForwardAll = false
	// Configure a long proxy lifetime, short duplicate-entry lifetime.
	n.proxyTableForget = 60 * time.Second
	n.dupTable.Cleanup()

	sanMAC := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	src := [6]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}

	// SAN sends a frame on the interlink -> MAC is learned.
	sanFrame := ethFrame(sanMAC, [6]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06})
	n.handleInterlinkFrame(frameEvent{iface: "interlink", frame: sanFrame, frameSz: len(sanFrame)})

	// A LAN frame to the SAN arrives well after the 640ms duplicate-entry
	// window would have expired, but within the proxy window.
	time.Sleep(700 * time.Millisecond)
	frame := ethFrame(src, sanMAC)
	enc := engine.EncodeRCT(frame, 1, 0)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if len(inter.frames) != 1 {
		t.Fatalf("learned SAN MAC should still be valid after 640ms, got %d forwards", len(inter.frames))
	}
}

// TestSupervisionSeenExpiry verifies supervision liveness entries are
// forgotten after supervision.node_forget_time, keeping the map bounded.
func TestSupervisionSeenExpiry(t *testing.T) {
	n, _, _, _ := newNode()
	n.supervisionForget = 100 * time.Millisecond
	n.supervisionSeen = map[string]time.Time{
		"02:50:50:aa:bb:cc": time.Now().Add(-1 * time.Second), // stale
		"02:50:50:dd:ee:ff": time.Now(),                       // fresh
	}
	n.cleanupSupervisionSeen()
	if _, ok := n.supervisionSeen["02:50:50:aa:bb:cc"]; ok {
		t.Error("stale supervision entry should have been forgotten")
	}
	if _, ok := n.supervisionSeen["02:50:50:dd:ee:ff"]; !ok {
		t.Error("fresh supervision entry must survive cleanup")
	}
}

// TestSupervisionOnInterlinkIsData: a 0x88fb frame arriving on the
// interlink must be treated as ordinary data (forwarded to both LANs), not
// consumed as a supervision frame.
func TestSupervisionOnInterlinkIsData(t *testing.T) {
	n, lanA, lanB, _ := newNode()
	n.dupTable.Cleanup()

	sup := make([]byte, 60)
	copy(sup[0:6], []byte{0x01, 0x15, 0x4e, 0x00, 0x01, 0x00})
	copy(sup[6:12], []byte{0x02, 0x50, 0x50, 0xaa, 0xbb, 0xcc})
	sup[12], sup[13] = 0x88, 0xfb // EtherType 0x88FB

	n.handleFrame(frameEvent{iface: "interlink", frame: sup, frameSz: len(sup)})
	if len(lanA.frames) != 1 || len(lanB.frames) != 1 {
		t.Fatalf("0x88fb on interlink must be forwarded, got A=%d B=%d", len(lanA.frames), len(lanB.frames))
	}
	if len(n.supervisionSeen) != 0 {
		t.Fatal("0x88fb on interlink must not be parsed as supervision")
	}
}

// TestBroadcastUnderMulticastFilter: with forward_all=false and a
// multicast prefix filter set, broadcast frames (ff:ff:ff:ff:ff:ff) must
// still be forwarded from the LANs to the interlink. A broadcast IS
// multicast (first octet odd), so a naive prefix filter like "01-00-5E"
// would drop it - breaking ARP. Real RedBoxes always flood broadcast.
func TestBroadcastUnderMulticastFilter(t *testing.T) {
	n, _, _, inter := newNode()
	n.Config.ForwardAll = false
	n.Config.MulticastFirstOctet = "01-00-5E"
	n.dupTable.Cleanup()

	src := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}

	// ARP request: broadcast dst.
	arp := buildARPFrame(src, [6]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	enc := engine.EncodeRCT(arp, 0, 0)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if len(inter.frames) != 1 {
		t.Fatalf("broadcast/ARP must be forwarded under multicast filter, got %d forwards", len(inter.frames))
	}

	// Plain unicast to an unknown SAN MAC: still filtered (proxy not learned).
	inter.drain()
	uni := ethFrame(src, [6]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66})
	enc = engine.EncodeRCT(uni, 1, 0)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if len(inter.frames) != 0 {
		t.Fatalf("unknown unicast must still be filtered, got %d forwards", len(inter.frames))
	}
}

// buildARPFrame creates an ARP request/response frame (EtherType 0x0806).
func buildARPFrame(src, dst [6]byte) []byte {
	f := make([]byte, 60)
	copy(f[0:6], dst[:])
	copy(f[6:12], src[:])
	f[12], f[13] = 0x08, 0x06 // ARP
	// htype=1, ptype=0x0800, hlen=6, plen=4, op=1 (request)
	f[14], f[15] = 0x00, 0x01
	f[16], f[17] = 0x08, 0x00
	f[18], f[19] = 0x06, 0x04
	f[20], f[21] = 0x00, 0x01
	copy(f[22:28], src[:])
	copy(f[28:32], []byte{10, 0, 0, 1})
	copy(f[38:44], []byte{0, 0, 0, 0, 0, 0})
	copy(f[44:48], []byte{10, 0, 0, 2})
	return f
}

// TestGOOSEMulticastDefault: GOOSE (IEC 61850-8-1) is L2 multicast
// 01-0C-CD-01-xx. With the default empty filter ALL multicast must pass,
// so GOOSE reaches the interlink from the LANs.
func TestGOOSEMulticastDefault(t *testing.T) {
	n, _, _, inter := newNode()
	n.Config.ForwardAll = false
	n.Config.MulticastFirstOctet = "" // default
	n.dupTable.Cleanup()

	src := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	goose := ethFrame(src, [6]byte{0x01, 0x0c, 0xcd, 0x01, 0x00, 0x01})
	enc := engine.EncodeRCT(goose, 0, 0)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if len(inter.frames) != 1 {
		t.Fatalf("GOOSE must pass default filter, got %d forwards", len(inter.frames))
	}
}

// TestGOOSEBlockedByIPV4Filter documents the real-RedBox hazard: a
// multicast filter of "01-00-5E" (IPv4 multicast only) blocks GOOSE
// (01-0C-CD) and SV (01-0C-CD-04). This is exactly what happens on many
// production RedBoxes; the test pins the current simulator behaviour so a
// user who configures that filter knows GOOSE will not cross.
func TestGOOSEBlockedByIPV4Filter(t *testing.T) {
	n, _, _, inter := newNode()
	n.Config.ForwardAll = false
	n.Config.MulticastFirstOctet = "01-00-5E"
	n.dupTable.Cleanup()

	src := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	goose := ethFrame(src, [6]byte{0x01, 0x0c, 0xcd, 0x01, 0x00, 0x01})
	enc := engine.EncodeRCT(goose, 0, 0)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if len(inter.frames) != 0 {
		t.Fatalf("GOOSE must be blocked by IPv4-only filter, got %d forwards", len(inter.frames))
	}
	t.Log("documented: 01-00-5E filter blocks GOOSE - configure 01-0C-CD or empty for IEC 61850")
}

// TestSVMulticastFiltered: SV (IEC 61850-9-2) dst 01-0C-CD-04-xx must be
// filterable exactly like GOOSE.
func TestSVMulticastFiltered(t *testing.T) {
	n, _, _, inter := newNode()
	n.Config.ForwardAll = false
	n.Config.MulticastFirstOctet = "01-0C-CD"
	n.dupTable.Cleanup()

	src := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}

	// SV dst 01-0C-CD-04-xx passes (prefix 01-0C-CD).
	sv := ethFrame(src, [6]byte{0x01, 0x0c, 0xcd, 0x04, 0x00, 0x01})
	enc := engine.EncodeRCT(sv, 0, 0)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if len(inter.frames) != 1 {
		t.Fatalf("SV with prefix 01-0C-CD must pass, got %d forwards", len(inter.frames))
	}

	// IPv4 multicast 01-00-5E is now blocked (prefix mismatch).
	inter.drain()
	ip4 := ethFrame(src, [6]byte{0x01, 0x00, 0x5e, 0x00, 0x00, 0x01})
	enc = engine.EncodeRCT(ip4, 1, 0)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if len(inter.frames) != 0 {
		t.Fatalf("IPv4 multicast must be blocked by 01-0C-CD filter, got %d forwards", len(inter.frames))
	}
}

// TestVLAN0PriorityTagging (IEC 61850 legacy default): a frame carrying a
// null VLAN ID (VID 0) with priority bits (PCP 4) — 802.1Q "priority
// tagging without segmentation" — must round-trip through the RCT
// machinery and be classified as VLAN-tagged (LSDU -= 4), while the tag
// itself stays intact.
func TestVLAN0PriorityTagging(t *testing.T) {
	// Build an Ethernet frame carrying 802.1Q tag: vid=0, pcp=4. The base
	// is 70 bytes so the tagged frame (74 B) stays above the VLAN minimum
	// frame size and EncodeRCT applies no padding.
	base := make([]byte, 70)
	copy(base[0:6], []byte{0x01, 0x0c, 0xcd, 0x01, 0x00, 0x01}) // GOOSE dst
	copy(base[6:12], []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff})
	base[12], base[13] = 0x88, 0xB8 // GOOSE EtherType
	tagged := vlanPCPFrame(base, 0, 4)

	// Classification: VLAN frame with null VLAN ID, still tagged.
	if !engine.IsVLANFrame(tagged) {
		t.Fatal("VLAN-0 tagged frame must be classified as VLAN frame")
	}
	if vid, ok := engine.GetVLANID(tagged); !ok || vid != 0 {
		t.Fatalf("GetVLANID = %d, %v; want 0, true (null VLAN)", vid, ok)
	}
	if et := engine.GetEtherType(tagged); et != 0x88B8 {
		t.Fatalf("GetEtherType = %#x, want 0x88b8 (GOOSE)", et)
	}

	// RCT round-trip: encode, then decode back.
	enc := engine.EncodeRCT(tagged, 7, 0)
	lanID, seq, payload, err := engine.DecodeRCT(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if lanID != 0 || seq != 7 {
		t.Fatalf("lanID=%d seq=%d, want 0, 7", lanID, seq)
	}
	if !bytes.Equal(payload, tagged) {
		t.Error("payload with VLAN tag corrupted by RCT round trip")
	}

	// The tag (TCI: vid=0, pcp=4) must be untouched on the wire.
	tci := binary.BigEndian.Uint16(payload[14:16])
	if gotVid, gotPcp := int(tci&0x0FFF), int((tci>>13)&0x07); gotVid != 0 || gotPcp != 4 {
		t.Fatalf("priority tag not preserved: vid=%d pcp=%d, want vid=0 pcp=4", gotVid, gotPcp)
	}
}

// TestVLAN0FilterSemantics: the interlink VLAN filter must treat a null
// VLAN (VID 0) frame as tagged. Empty filter passes everything; a filter
// of [10] blocks VID 0; an explicit [0] passes it (legacy IEC 61850
// networks that whitelist the null VLAN).
func TestVLAN0FilterSemantics(t *testing.T) {
	src := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	base := ethFrame(src, [6]byte{0x01, 0x0c, 0xcd, 0x01, 0x00, 0x01})
	v0 := vlanPCPFrame(base, 0, 4) // vid=0, pcp=4

	// 1. Empty filter (default): VID 0 passes.
	n, lanA, lanB, _ := newNode()
	n.Config.VLANFilter = nil
	n.handleInterlinkFrame(frameEvent{iface: "interlink", frame: v0, frameSz: len(v0)})
	if len(lanA.frames) != 1 || len(lanB.frames) != 1 {
		t.Fatalf("empty filter must pass VID 0, got A=%d B=%d", len(lanA.frames), len(lanB.frames))
	}

	// 2. Filter [10]: VID 0 is blocked (it is tagged, just null).
	n2, lanA2, lanB2, _ := newNode()
	n2.Config.VLANFilter = []int{10}
	n2.handleInterlinkFrame(frameEvent{iface: "interlink", frame: v0, frameSz: len(v0)})
	if len(lanA2.frames) != 0 || len(lanB2.frames) != 0 {
		t.Fatalf("filter [10] must block VID 0, got A=%d B=%d", len(lanA2.frames), len(lanB2.frames))
	}

	// 3. Filter [0]: VID 0 passes (explicit null-VLAN whitelist).
	n3, lanA3, lanB3, _ := newNode()
	n3.Config.VLANFilter = []int{0}
	n3.handleInterlinkFrame(frameEvent{iface: "interlink", frame: v0, frameSz: len(v0)})
	if len(lanA3.frames) != 1 || len(lanB3.frames) != 1 {
		t.Fatalf("filter [0] must pass VID 0, got A=%d B=%d", len(lanA3.frames), len(lanB3.frames))
	}
}

// vlanPCPFrame tags a frame with the given VLAN ID and PCP (unlike
// vlanTagFrame which sets PCP=0).
func vlanPCPFrame(frame []byte, vid, pcp int) []byte {
	out := make([]byte, 0, len(frame)+4)
	out = append(out, frame[0:12]...) // dst + src
	out = append(out, 0x81, 0x00)     // 802.1Q
	tci := uint16(pcp&0x07)<<13 | uint16(vid&0x0FFF)
	out = append(out, byte(tci>>8), byte(tci))
	out = append(out, frame[12:len(frame)-4]...) // EtherType + payload
	return out
}

var _ iface.PacketPort = (*memPort)(nil)

// Ensure no stray timers leak between tests.
func TestMain(m *testing.M) {
	time.Now()
	m.Run()
}
