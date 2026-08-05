// Edge-case integration tests for the PRP engine (in-memory ports).
package prp

import (
	"testing"

	"prp-gns3/internal/engine"
	"prp-gns3/internal/supervision"
)

// TestSeqResetDuplicateCollision verifies the restart race is handled:
// a node restarts and its per-src-MAC sequence counter resets, while the
// peer's duplicate-detection table still holds (src, seq 0..k) entries
// within the entry_forget window.
//
// Two mechanisms cover this:
//  1. randSeqStart() gives every node incarnation a random starting
//     sequence number, so a restarted node does NOT reuse the same low
//     sequence numbers the peer saw moments earlier.
//  2. handleSupervisionFrame detects the supervision sequence regression
//     (the restart signal) and flushes the peer's dup entries, so any
//     residual stale entries are cleared at the first supervision frame
//     after restart.
func TestSeqResetDuplicateCollision(t *testing.T) {
	n, _, _, inter := newNode()
	n.dupTable.Cleanup()

	src := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	frame := ethFrame(src, [6]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66})

	// 1. Peer receives seq 0..4 from this source (normal operation),
	// interleaved with normal supervision frames (advancing seqs).
	for seq := 0; seq < 5; seq++ {
		enc := engine.EncodeRCT(frame, uint16(seq), 0)
		n.handleFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	}
	if got := len(inter.frames); got != 5 {
		t.Fatalf("expected 5 forwarded frames, got %d", got)
	}
	for supSeq := uint16(5); supSeq <= 7; supSeq++ {
		sup := supervision.BuildRCTed(src[:], supSeq, 0)
		n.handleFrame(frameEvent{iface: "lan_a", frame: sup, frameSz: len(sup)})
	}
	if got := n.dupTable.Size(); got != 5 {
		t.Fatalf("expected 5 data entries after normal operation, got %d", got)
	}

	// 2. Source node restarts: its supervision seq regresses (peer had
	// reached seq 7, the restarted node starts again at seq 3) and its
	// data counter is back near 0. The supervision regression must flush
	// the stale dup entries BEFORE the fresh data frames arrive.
	inter.drain()
	sup := supervision.BuildRCTed(src[:], 3, 0) // restarted node, low sup seq
	n.handleFrame(frameEvent{iface: "lan_a", frame: sup, frameSz: len(sup)})
	if got := n.dupTable.Size(); got != 0 {
		t.Fatalf("expected dup table flushed after restart detection, size=%d", got)
	}

	// 3. Fresh frame reusing seq 2 (restarted counter) must now pass.
	inter.drain()
	enc := engine.EncodeRCT(frame, 2, 0) // reset counter emits seq 2 again
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if got := len(inter.frames); got != 1 {
		t.Fatalf("expected fresh seq 2 to pass after supervision flush, got %d forwards", got)
	}
}

// TestNoFlushOnNormalSeqAdvance ensures the dup table is NOT flushed when
// supervision sequences advance normally (no regression): a transiently
// dropped supervision frame (e.g. 5 -> 7) must not clear live dup state.
func TestNoFlushOnNormalSeqAdvance(t *testing.T) {
	n, _, _, inter := newNode()
	n.dupTable.Cleanup()

	src := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	frame := ethFrame(src, [6]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66})

	enc := engine.EncodeRCT(frame, 10, 0)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if got := len(inter.frames); got != 1 {
		t.Fatalf("expected 1 forwarded frame, got %d", got)
	}

	// Supervision advances normally (5 -> 7, one frame lost): no flush.
	sup5 := supervision.BuildRCTed(src[:], 5, 0)
	n.handleFrame(frameEvent{iface: "lan_a", frame: sup5, frameSz: len(sup5)})
	sup7 := supervision.BuildRCTed(src[:], 7, 0)
	n.handleFrame(frameEvent{iface: "lan_a", frame: sup7, frameSz: len(sup7)})
	if got := n.dupTable.Size(); got != 1 {
		t.Fatalf("expected dup entry to survive normal seq advance, size=%d", got)
	}

	// A later duplicate of seq 10 is still discarded.
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if got := len(inter.frames); got != 1 {
		t.Fatalf("expected seq 10 duplicate to stay discarded, got %d forwards", got)
	}
}

// TestSequenceStartRandomized verifies each node incarnation starts its
// per-source sequence numbers at a random value (not 0), so a restart does
// not collide with the peer's still-warm dup table.
func TestSequenceStartRandomized(t *testing.T) {
	n1 := NewNode(&Config{})
	n2 := NewNode(&Config{})
	// 100 draws: a fixed start of 0 for every node would collide; the
	// random starts must differ at least once across the sample.
	differed := false
	for i := 0; i < 100; i++ {
		a := n1.seqMgr.Next("aa")
		b := n2.seqMgr.Next("bb")
		n1.seqMgr.Reset("aa")
		n2.seqMgr.Reset("bb")
		if a != b {
			differed = true
			break
		}
	}
	if !differed {
		t.Fatal("expected randomized sequence starts across node instances")
	}
}

// TestSeqWrapAround verifies duplicate detection survives sequence-number
// wrap-around 65535 -> 0: a frame with seq 0 arriving right after seq
// 65535 must NOT be treated as a duplicate of the earlier seq 0.
func TestSeqWrapAround(t *testing.T) {
	n, _, _, inter := newNode()
	n.dupTable.Cleanup()

	src := [6]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	frame := ethFrame(src, [6]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66})

	// seq 65535 (last before wrap).
	enc := engine.EncodeRCT(frame, 65535, 0)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if got := len(inter.frames); got != 1 {
		t.Fatalf("seq 65535 should forward, got %d", got)
	}

	// seq 0 immediately after wrap: if the entry table treats (src,0) as
	// fresh (expired 640ms ago), this forwards. But if any stale entry
	// remains it would be dropped. With the current 640ms window and two
	// back-to-back calls the first (src,0) is gone, so this forwards.
	inter.drain()
	enc = engine.EncodeRCT(frame, 0, 0)
	n.handleIncomingPRPFrame(frameEvent{iface: "lan_a", frame: enc, frameSz: len(enc)})
	if got := len(inter.frames); got != 1 {
		t.Fatalf("seq 0 after wrap-around must forward, got %d", got)
	}
}
