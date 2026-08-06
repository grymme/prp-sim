package prp

import (
	"testing"
	"time"

	"prp-gns3/internal/engine"
	"prp-gns3/internal/supervision"
)

// hsrNode builds an HSR-capable test node with in-memory ports.
func hsrNode(role string) (*Node, *memPort, *memPort, *memPort) {
	lanA := &memPort{name: "ring_a"}
	lanB := &memPort{name: "ring_b"}
	inter := &memPort{name: "interlink"}
	n := NewNode(&Config{
		NodeName:       "hsr-test",
		Role:           role,
		LanID:          "A",
		NetID:          1,
		TrailerEnabled: true,
		ForwardAll:     true, // mirror the config default (interlink.forward_all)
	})
	n.LanA = lanA
	n.LanB = lanB
	n.Interlink = inter
	n.SetNodeMAC([]byte{0x02, 0x50, 0x50, 0x00, 0x00, 0x01})
	return n, lanA, lanB, inter
}

// TestHSRPathTransition: a path-0 frame on one ring port is forwarded to
// the other with path 1; a path-1 frame is not re-forwarded.
func TestHSRPathTransition(t *testing.T) {
	n, _, ringB, inter := hsrNode("hsr-san")
	n.dupTable.Cleanup()

	src := [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x10}
	frame := ethFrame(src, [6]byte{0x01, 0x0c, 0xcd, 0x01, 0x00, 0x01})
	frame[12] = 0x88
	frame[13] = 0xb8
	hsrBody := engine.EncodeHSR(frame[14:], 0, 42, 0x88b8)
	hsr := append(append([]byte(nil), frame[:12]...), hsrBody...)

	n.handleIncomingHSRFrame(frameEvent{iface: "ring_a", frame: hsr, frameSz: len(hsr)})

	if len(ringB.frames) != 1 {
		t.Fatalf("expected 1 forward to ring B, got %d", len(ringB.frames))
	}
	path, _, _, _, err := engine.DecodeHSR(ringB.frames[0])
	if err != nil {
		t.Fatalf("decode forwarded frame: %v", err)
	}
	if path != 1 {
		t.Errorf("forwarded path = %d, want 1", path)
	}
	// Duplicate of the same (src, seq) must NOT be forwarded again.
	ringB.drain()
	n.handleIncomingHSRFrame(frameEvent{iface: "ring_a", frame: hsr, frameSz: len(hsr)})
	if len(ringB.frames) != 0 {
		t.Errorf("duplicate frame forwarded again (%d frames)", len(ringB.frames))
	}
	// path-1 frame must not be re-forwarded.
	hsr1, _ := engine.RewriteHSRPath(hsr, 1)
	ringB.drain()
	n.handleIncomingHSRFrame(frameEvent{iface: "ring_a", frame: hsr1, frameSz: len(hsr1)})
	if len(ringB.frames) != 0 {
		t.Errorf("path-1 frame forwarded (%d frames)", len(ringB.frames))
	}
	_ = inter
}

// TestHSROwnFrameDiscard: a frame with our own source MAC (returned after
// a full lap) must be discarded, not forwarded or delivered.
func TestHSROwnFrameDiscard(t *testing.T) {
	n, _, ringB, inter := hsrNode("hsr-san")
	n.dupTable.Cleanup()

	frame := ethFrame([6]byte{0x02, 0x50, 0x50, 0x00, 0x00, 0x01}, [6]byte{0x01, 0x0c, 0xcd, 0x01, 0x00, 0x01})
	frame[12] = 0x88
	frame[13] = 0xb8
	hsrBody := engine.EncodeHSR(frame[14:], 0, 5, 0x88b8)
	hsr := append(append([]byte(nil), frame[:12]...), hsrBody...)

	n.handleIncomingHSRFrame(frameEvent{iface: "ring_a", frame: hsr, frameSz: len(hsr)})
	if len(ringB.frames) != 0 || len(inter.frames) != 0 {
		t.Errorf("own frame forwarded/delivered: ringB=%d inter=%d", len(ringB.frames), len(inter.frames))
	}
}

// TestHSRSanDelivery: an accepted ring frame is delivered to the SAN with
// the HSR tag stripped and the original EtherType restored.
func TestHSRSanDelivery(t *testing.T) {
	n, _, _, inter := hsrNode("hsr-san")
	n.dupTable.Cleanup()

	src := [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x20}
	dst := [6]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	payload := []byte{0x45, 0x00, 0x00, 0x10}
	hsrBody := engine.EncodeHSR(payload, 0, 7, 0x0800)
	hsr := make([]byte, 0, 12+len(hsrBody))
	hsr = append(append(append(hsr, dst[:]...), src[:]...), hsrBody...)

	n.handleIncomingHSRFrame(frameEvent{iface: "ring_b", frame: hsr, frameSz: len(hsr)})
	if len(inter.frames) != 1 {
		t.Fatalf("expected 1 delivered frame, got %d", len(inter.frames))
	}
	got := inter.frames[0]
	// Original frame: MAC header + 0x0800 EtherType + payload.
	if got[12] != 0x08 || got[13] != 0x00 {
		t.Errorf("EtherType = %02x%02x, want 0x0800", got[12], got[13])
	}
	if string(got[14:18]) != string(payload) {
		t.Errorf("payload = %v, want %v (padding must be stripped)", got[14:], payload)
	}
}

// TestHSRSanInject: frames from the SAN get an HSR tag (path 0) and are
// sent on BOTH ring ports with the same sequence number.
func TestHSRSanInject(t *testing.T) {
	n, ringA, ringB, _ := hsrNode("hsr-san")
	n.dupTable.Cleanup()

	frame := ethFrame([6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x30}, [6]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	frame[12] = 0x08
	frame[13] = 0x00

	n.handleHSRInterlinkFrame(frameEvent{iface: "interlink", frame: frame, frameSz: len(frame)})

	if len(ringA.frames) != 1 || len(ringB.frames) != 1 {
		t.Fatalf("expected 1 frame on each ring port, got A=%d B=%d", len(ringA.frames), len(ringB.frames))
	}
	pathA, seqA, _, _, err := engine.DecodeHSR(ringA.frames[0])
	if err != nil {
		t.Fatalf("decode ring A frame: %v", err)
	}
	pathB, seqB, _, _, err := engine.DecodeHSR(ringB.frames[0])
	if err != nil {
		t.Fatalf("decode ring B frame: %v", err)
	}
	if pathA != 0 || pathB != 0 {
		t.Errorf("paths = %d/%d, want 0/0", pathA, pathB)
	}
	if seqA != seqB {
		t.Errorf("seq differs across ring ports: %d vs %d", seqA, seqB)
	}
}

// TestHSRPRPCouplingReinjection: in hsr-prp mode a frame arriving on the
// ring from the OTHER PRP LAN (same NetId, other LanId) is dropped, while
// a frame from the same LAN (own PathId) is discarded after a lap, and a
// frame with a different NetId is delivered to the coupled LAN.
func TestHSRPRPCouplingReinjection(t *testing.T) {
	// RedBox B: NetId 1, LanId B (1). Frames from LAN A carry path (1,0).
	n, _, _, inter := hsrNode("hsr-prp")
	n.Config.LanID = "B" // LanId B = 1
	n.dupTable.Cleanup()

	mk := func(path int, seq int) []byte {
		src := [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, byte(seq)}
		frame := ethFrame(src, [6]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
		frame[12] = 0x08
		frame[13] = 0x00
		hsrBody := engine.EncodeHSR(frame[14:], path, uint16(seq), 0x0800)
		return append(append([]byte(nil), frame[:12]...), hsrBody...)
	}

	// Same NetId, other LanId (1,0) -> drop (reinjection prevention).
	inter.drain()
	n.handleIncomingHSRFrame(frameEvent{iface: "ring_a", frame: mk(1<<1|0, 1), frameSz: 60})
	if len(inter.frames) != 0 {
		t.Errorf("frame from other LAN delivered (%d frames)", len(inter.frames))
	}
	// Own PathId (1,1) returning -> dropped (node table / own check).
	inter.drain()
	n.handleIncomingHSRFrame(frameEvent{iface: "ring_a", frame: mk(1<<1|1, 2), frameSz: 60})
	if len(inter.frames) != 0 {
		t.Errorf("own PathId frame delivered (%d frames)", len(inter.frames))
	}
	// Different NetId (2,0) -> delivered to coupled LAN with RCT lan B.
	inter.drain()
	n.handleIncomingHSRFrame(frameEvent{iface: "ring_a", frame: mk(2<<1|0, 3), frameSz: 60})
	if len(inter.frames) != 1 {
		t.Fatalf("expected 1 delivered frame, got %d", len(inter.frames))
	}
	lanID, seq, _, err := engine.DecodeRCT(inter.frames[0])
	if err != nil {
		t.Fatalf("decode RCT: %v", err)
	}
	if lanID != 1 {
		t.Errorf("RCT lan_id = %d, want 1 (LAN B)", lanID)
	}
	if seq != 3 {
		t.Errorf("RCT seq = %d, want 3 (preserved end-to-end)", seq)
	}
}

// TestHSRSupervision sends HSR supervision frames on the ring and confirms
// they carry the coupling path.
func TestHSRSupervision(t *testing.T) {
	n, ringA, ringB, _ := hsrNode("hsr-prp")
	n.Config.LanID = "B"
	n.SendSupervisionFrame()
	if len(ringA.frames) != 1 || len(ringB.frames) != 1 {
		t.Fatalf("expected supervision on both ring ports")
	}
	// HSR supervision: path (1<<1)|1 = 3.
	path := int((uint16(ringA.frames[0][14])<<8 | uint16(ringA.frames[0][15])) >> 12)
	if path != 3 {
		t.Errorf("supervision path = %d, want 3", path)
	}
}

// TestHSRFakeClock drives node-table expiry deterministically.
func TestHSRFakeClock(t *testing.T) {
	n, _, ringB, _ := hsrNode("hsr-san")
	now := time.Unix(1000, 0)
	n.SetClock(func() time.Time { return now })
	n.dupTable.Cleanup()

	src := [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x40}
	frame := ethFrame(src, [6]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	frame[12] = 0x08
	frame[13] = 0x00
	hsrBody := engine.EncodeHSR(frame[14:], 0, 1, 0x0800)
	hsr := append(append([]byte(nil), frame[:12]...), hsrBody...)

	n.handleIncomingHSRFrame(frameEvent{iface: "ring_a", frame: hsr, frameSz: len(hsr)})
	// Immediately: duplicate.
	ringB.drain()
	n.handleIncomingHSRFrame(frameEvent{iface: "ring_a", frame: hsr, frameSz: len(hsr)})
	if len(ringB.frames) != 0 {
		t.Error("expected duplicate drop before expiry")
	}
	// Advance past entry_forget_time (640ms): same (src,seq) is new again.
	now = now.Add(700 * time.Millisecond)
	ringB.drain()
	n.handleIncomingHSRFrame(frameEvent{iface: "ring_a", frame: hsr, frameSz: len(hsr)})
	if len(ringB.frames) != 1 {
		t.Errorf("expected forward after expiry, got %d", len(ringB.frames))
	}
}

// hsrhsrNode builds an hsr-hsr (QuadBox) test node.
func hsrhsrNode() (*Node, *memPort, *memPort, *memPort) {
	lanA := &memPort{name: "ring1_a"}
	lanB := &memPort{name: "ring1_b"}
	inter := &memPort{name: "interlink"}
	n := NewNode(&Config{
		NodeName:       "quad",
		Role:           "hsr-hsr",
		TrailerEnabled: true,
		ForwardAll:     true,
	})
	n.LanA = lanA
	n.LanB = lanB
	n.Interlink = inter
	n.SetNodeMAC([]byte{0x02, 0x50, 0x50, 0x00, 0x00, 0x01})
	return n, lanA, lanB, inter
}

// hsrFrame builds an HSR-tagged frame with the given src and path/seq.
func hsrFrame(srcMAC [6]byte, path int, seq int) []byte {
	dst := [6]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	payload := []byte{0x45, 0x00, 0x00, 0x10}
	body := engine.EncodeHSR(payload, path, uint16(seq), 0x0800)
	frame := make([]byte, 0, 12+len(body))
	frame = append(append(append(frame, dst[:]...), srcMAC[:]...), body...)
	return frame
}

// TestHSRHSRRingToInterlink: a path-0 frame on a ring port is forwarded to
// the other ring port (path 1) AND onto the interlink (path 1), so the
// second ring receives it.
func TestHSRHSRRingToInterlink(t *testing.T) {
	n, _, ringB, inter := hsrhsrNode()
	n.dupTable.Cleanup()

	src := [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x50}
	frame := hsrFrame(src, 0, 7)

	n.handleIncomingHSRFrame(frameEvent{iface: "ring1_a", frame: frame, frameSz: len(frame)})

	if len(ringB.frames) != 1 {
		t.Fatalf("expected 1 forward to ring B, got %d", len(ringB.frames))
	}
	if len(inter.frames) != 1 {
		t.Fatalf("expected 1 forward to interlink, got %d", len(inter.frames))
	}
	// Both forwarded copies must carry path 1 and the same seq.
	for name, f := range map[string][]byte{"ringB": ringB.frames[0], "inter": inter.frames[0]} {
		path, seq, _, _, err := engine.DecodeHSR(f)
		if err != nil {
			t.Fatalf("%s decode: %v", name, err)
		}
		if path != 1 {
			t.Errorf("%s path = %d, want 1", name, path)
		}
		if seq != 7 {
			t.Errorf("%s seq = %d, want 7", name, seq)
		}
	}
}

// TestHSRHSRInterlinkToRing: an HSR frame from the interlink (ring 2) is
// reinjected onto BOTH ring ports with path 0, and not echoed back.
func TestHSRHSRInterlinkToRing(t *testing.T) {
	n, ringA, ringB, _ := hsrhsrNode()
	n.dupTable.Cleanup()

	src := [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x51}
	frame := hsrFrame(src, 1, 9) // arrived on interlink with path 1

	n.handleHSRInterlinkFrame(frameEvent{iface: "interlink", frame: frame, frameSz: len(frame)})

	if len(ringA.frames) != 1 || len(ringB.frames) != 1 {
		t.Fatalf("expected 1 frame on each ring port, got A=%d B=%d", len(ringA.frames), len(ringB.frames))
	}
	for name, f := range map[string][]byte{"ringA": ringA.frames[0], "ringB": ringB.frames[0]} {
		path, seq, _, _, err := engine.DecodeHSR(f)
		if err != nil {
			t.Fatalf("%s decode: %v", name, err)
		}
		if path != 0 {
			t.Errorf("%s path = %d, want 0 (fresh traversal on ring 1)", name, path)
		}
		if seq != 9 {
			t.Errorf("%s seq = %d, want 9 (preserved end-to-end)", name, seq)
		}
	}
	// Duplicate of the same (src, seq) must not be reinjected again.
	ringA.drain()
	ringB.drain()
	n.handleHSRInterlinkFrame(frameEvent{iface: "interlink", frame: frame, frameSz: len(frame)})
	if len(ringA.frames) != 0 || len(ringB.frames) != 0 {
		t.Errorf("duplicate reinjected onto ring (A=%d B=%d)", len(ringA.frames), len(ringB.frames))
	}
}

// TestHSRHSROwnFrameInterlink: a frame with our own source MAC arriving on
// the interlink (a full-lap return from the other ring) is discarded.
func TestHSRHSROwnFrameInterlink(t *testing.T) {
	n, ringA, ringB, _ := hsrhsrNode()
	n.dupTable.Cleanup()

	frame := hsrFrame([6]byte{0x02, 0x50, 0x50, 0x00, 0x00, 0x01}, 1, 3)
	n.handleHSRInterlinkFrame(frameEvent{iface: "interlink", frame: frame, frameSz: len(frame)})
	if len(ringA.frames) != 0 || len(ringB.frames) != 0 {
		t.Errorf("own frame reinjected (A=%d B=%d)", len(ringA.frames), len(ringB.frames))
	}
}

// TestHSRHSRSupervisionBridge: HSR supervision on a ring port is consumed
// locally AND forwarded to the interlink; supervision from the interlink is
// forwarded onto both ring ports.
func TestHSRHSRSupervisionBridge(t *testing.T) {
	n, ringA, ringB, inter := hsrhsrNode()
	n.dupTable.Cleanup()

	// Ring supervision -> interlink. The dispatch matches iface names
	// lan_a/lan_b for ring ports (as the read loop uses them).
	sup := supervision.BuildHSR([]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x60}, 1, 0)
	n.handleFrame(frameEvent{iface: "lan_a", frame: sup, frameSz: len(sup)})
	if len(inter.frames) != 1 {
		t.Errorf("ring supervision not forwarded to interlink (%d)", len(inter.frames))
	}
	if _, ok := n.supervisionSeen["02:00:00:00:00:60"]; !ok {
		t.Error("ring supervision not consumed locally")
	}

	// Interlink supervision -> both ring ports.
	inter.drain()
	sup2 := supervision.BuildHSR([]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x61}, 2, 0)
	n.handleFrame(frameEvent{iface: "interlink", frame: sup2, frameSz: len(sup2)})
	if len(ringA.frames) != 1 || len(ringB.frames) != 1 {
		t.Errorf("interlink supervision not forwarded to ring (A=%d B=%d)", len(ringA.frames), len(ringB.frames))
	}
}
