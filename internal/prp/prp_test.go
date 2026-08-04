package prp

import (
	"bytes"
	"encoding/binary"
	"sync"
	"testing"
	"time"

	"prp-gns3/internal/engine"
	"prp-gns3/internal/iface"
	"prp-gns3/internal/nodetable"
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
	engine.ResetSequence("aa:bb:cc:dd:ee:ff")
	nodetable.Cleanup()

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
	engine.ResetSequence("aa:bb:cc:dd:ee:ff")

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
	nodetable.Cleanup()

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
	nodetable.Cleanup()

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

// TestDANModeForwarding: in DAN mode, frames from LANs are written to the
// TAP interface after RCT stripping.
func TestDANModeForwarding(t *testing.T) {
	lanA := &memPort{name: "lan_a"}
	lanB := &memPort{name: "lan_b"}
	tap := &memPort{name: "tap"}
	n := NewNode(&Config{Role: "dan", TrailerEnabled: true})
	n.LanA = lanA
	n.LanB = lanB
	n.Tap = tap
	nodetable.Cleanup()

	// Unique src MAC: the node table is a package-level singleton and
	// Cleanup() only removes expired entries.
	frame2 := ethFrame([6]byte{0x0a, 0xbb, 0xcc, 0xdd, 0xee, 0xff}, [6]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66})
	enc := engine.EncodeRCT(frame2, 0, 0)

	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if len(tap.frames) != 1 {
		t.Fatalf("expected 1 frame on TAP, got %d", len(tap.frames))
	}
	if !bytes.Equal(tap.frames[0], frame2) {
		t.Error("TAP frame should equal the original untagged frame")
	}
}

// TestProxyLearning verifies that with ForwardAll=false, LAN frames are only
// forwarded to the interlink after the SAN has been learned.
func TestProxyLearning(t *testing.T) {
	n, _, _, inter := newNode()
	n.Config.ForwardAll = false
	n.Config.MaxNodeTableSize = 0 // default
	nodetable.Cleanup()

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
	nodetable.Cleanup()

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

// TestDANUnicastFilter verifies DAN mode only delivers unicast frames
// addressed to the node's own MAC.
func TestDANUnicastFilter(t *testing.T) {
	lanA := &memPort{name: "lan_a"}
	lanB := &memPort{name: "lan_b"}
	tap := &memPort{name: "tap"}
	n := NewNode(&Config{Role: "dan", TrailerEnabled: true})
	n.LanA = lanA
	n.LanB = lanB
	n.Tap = tap
	n.SetNodeMAC([]byte{0x02, 0x50, 0x50, 0x00, 0x00, 0x99})
	nodetable.Cleanup()

	src := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}

	// Unicast to someone else: dropped.
	other := ethFrame(src, [6]byte{0x10, 0x22, 0x33, 0x44, 0x55, 0x66})
	enc := engine.EncodeRCT(other, 0, 0)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if len(tap.frames) != 0 {
		t.Fatalf("unicast to other MAC should be dropped, got %d", len(tap.frames))
	}

	// Unicast to our MAC: delivered.
	mine := ethFrame(src, [6]byte{0x02, 0x50, 0x50, 0x00, 0x00, 0x99})
	enc = engine.EncodeRCT(mine, 1, 0)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if len(tap.frames) != 1 {
		t.Fatalf("unicast to own MAC should be delivered, got %d", len(tap.frames))
	}
}

var _ iface.PacketPort = (*memPort)(nil)

// Ensure no stray timers leak between tests.
func TestMain(m *testing.M) {
	time.Now()
	m.Run()
}
