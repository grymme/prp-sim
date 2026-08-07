// internal/prp/hsr.go — HSR ring handling for the RedBox.
//
// Ring semantics (IEC 62439-3, mirrored by the kernel net/hsr):
//
//   - A frame injected onto the ring carries an HSR tag with path 0 and a
//     sequence number assigned per source MAC. It is sent on BOTH ring
//     ports; the two copies travel the ring in opposite directions.
//   - Every node forwards a frame received on one ring port out the other,
//     setting path to 1. A frame with path 1 is not forwarded again (it
//     has already made one hop around the ring).
//   - Each node keeps (src MAC, seq) in the node table: the first copy to
//     arrive is accepted, the second is discarded as a duplicate, and the
//     originator discards its own frame after a full lap.
//   - hsr-prp coupling: the PathId is (NetId<<1)|LanId. Frames received
//     from the ring whose PathId.NetId matches our NetId but whose LanId
//     differs are dropped (IEC 62439-3:2021/COR1 §5.2.2.3.1) — they came
//     from the other PRP LAN and must not be reinjected onto ours.
package prp

import (
	"log"
	"time"

	"prp-gns3/internal/engine"
)

// tracef logs a per-frame decision line when DEBUG_FRAMES=1.
func (n *Node) tracef(format string, args ...any) {
	if n.Config.Debug {
		log.Printf("prp: "+format, args...)
	}
}

// noteDrop increments the drop-reason counter for the given reason.
func (n *Node) noteDrop(reason string) {
	if n.dropCounters == nil {
		n.dropCounters = make(map[string]int)
	}
	n.dropCounters[reason]++
}

// countFrame bumps the per-port traffic counters.
func (n *Node) countFrame(port, dir string) {
	n.counters.mu.Lock()
	defer n.counters.mu.Unlock()
	switch port + "/" + dir {
	case "lan_a/in":
		n.counters.lanAIn++
	case "lan_b/in":
		n.counters.lanBIn++
	case "lan_a/out":
		n.counters.lanAOut++
	case "lan_b/out":
		n.counters.lanBOut++
	case "interlink/in":
		n.counters.interIn++
	case "interlink/out":
		n.counters.interOut++
	}
}

// Status is a snapshot of the node's live counters for the console TUI.
type Status struct {
	Role         string
	LanID        string // "A"/"B" for hsr-prp, "" otherwise
	NetID        int
	LanAIn       uint64
	LanBIn       uint64
	LanAOut      uint64
	LanBOut      uint64
	InterIn      uint64
	InterOut     uint64
	SupSent      uint64
	DupTableSize int
	Drops        map[string]int
}

// Snapshot returns a consistent view of the node's counters.
func (n *Node) Snapshot() Status {
	n.counters.mu.Lock()
	st := Status{
		Role:         n.Config.Role,
		LanID:        n.Config.LanID,
		NetID:        n.Config.NetID,
		LanAIn:       n.counters.lanAIn,
		LanBIn:       n.counters.lanBIn,
		LanAOut:      n.counters.lanAOut,
		LanBOut:      n.counters.lanBOut,
		InterIn:      n.counters.interIn,
		InterOut:     n.counters.interOut,
		SupSent:      n.counters.supSent,
		DupTableSize: n.dupTable.Size(),
	}
	n.counters.mu.Unlock()
	st.Drops = make(map[string]int, len(n.dropCounters))
	for k, v := range n.dropCounters {
		st.Drops[k] = v
	}
	return st
}

// handleIncomingHSRFrame processes a frame arriving on a ring port.
// Flow: decode tag → own-frame discard → dup detection → forward to the
// other ring port (path 0→1) → deliver to the interlink unless duplicate.
func (n *Node) handleIncomingHSRFrame(event frameEvent) {
	n.countFrame(event.iface, "in")
	path, seq, encap, payload, err := engine.DecodeHSR(event.frame)
	if err != nil {
		n.tracef("HSR frame on %s: %v", event.iface, err)
		n.noteDrop("malformed")
		return
	}
	srcMAC := engine.GetSrcMAC(event.frame)
	other := "lan_b"
	if event.iface == "lan_b" {
		other = "lan_a"
	}

	// Own frame returning after a full lap — discard (multicast frames
	// traverse the entire ring and are removed by the originator).
	if n.mac != nil && bytesEqual(n.mac, srcMACBytes(event.frame)) {
		n.tracef("HSR frame on %s from own MAC (path %d, seq %d) — full lap, discarding", event.iface, path, seq)
		n.noteDrop("own")
		return
	}

	// Duplicate detection: both ring copies and repeated delivery are
	// keyed on (src MAC, seq).
	if n.dupTable.Find(srcMAC, seq) {
		n.tracef("HSR duplicate from %s seq=%d on %s, discarding", srcMAC, seq, event.iface)
		n.noteDrop("dup")
		return
	}
	n.dupTable.InsertWithExpiry(srcMAC, seq, 0, n.entryForgetMs())
	n.learnRingMember(srcMAC)

	// Forward around the ring: every received ring frame is forwarded to
	// the other ring port (mirrors the kernel hsr_forward_skb, which
	// forwards to all other ports; loop protection comes from the node
	// table + originator discard, not from the path bits). The path
	// field identifies the egress lane: 0 when egressing on LAN A, 1 on
	// LAN B (kernel hsr_set_path_id). For HSR-PRP coupling the frame
	// may already carry a coupling PathId in the top bits; only the
	// lane bit is set on forward (PathId bits are preserved).
	fwd, err := engine.RewriteHSRPathLane(event.frame, other == "lan_a")
	if err == nil {
		port := n.LanB
		if other == "lan_a" {
			port = n.LanA
		}
		if _, err := port.Write(fwd); err != nil {
			n.tracef("HSR forward to %s failed: %v", other, err)
		} else {
			n.countFrame(other, "out")
			n.tracef("HSR frame from %s (seq %d) forwarded to %s (lane %d)", event.iface, seq, other, map[bool]int{true: 0, false: 1}[other == "lan_a"])
		}
	}

	// hsr-hsr (QuadBox): also forward the frame onto the interlink so
	// it reaches the other HSR ring — unless it is unicast addressed to
	// a local ring member (kernel hsr_drop_frame: DA in node_db is not
	// forwarded to the interlink).
	if n.IsHSRHSR() && n.Interlink != nil {
		if n.unicastToRingMember(event.frame[:6]) {
			n.tracef("HSR unicast to ring member not forwarded to interlink")
			n.noteDrop("filter")
		} else if _, err := n.Interlink.Write(event.frame); err != nil {
			n.tracef("HSR forward to interlink failed: %v", err)
		} else {
			n.countFrame("interlink", "out")
			n.tracef("HSR frame from %s (seq %d) forwarded to interlink (ring 2)", event.iface, seq)
		}
	}

	// Deliver to the interlink. In hsr-prp coupling the interlink is a
	// PRP LAN and the PathId rule applies; in hsr-san it is a plain SAN.
	if n.IsHSRPRPCoupling() {
		if path>>1 != n.NetID() {
			// Different NetId (pure ring or another coupled network):
			// forward onto our PRP LAN.
			original, err := engine.StripHSR(event.frame)
			if err != nil {
				n.tracef("HSR strip for coupling failed: %v", err)
				n.noteDrop("malformed")
				return
			}
			// In HSR-PRP coupling the interlink is a PRP LAN (different
			// redundancy domain), NOT a SAN or another ring. The
			// hsr_drop_frame filter (unicast to ring member) does NOT
			// apply here — only the PathId reinjection check below.
			n.couplingToPRP(original, seq)
			return
		}
		if path&1 == n.LanID() {
			// Own PathId — our own injection returning after a lap.
			n.tracef("HSR frame with own PathId (net %d, lan %d) returning — discarded", n.NetID(), n.LanID())
			n.noteDrop("own")
			return
		}
		// Same NetId, other LanId (IEC §5.2.2.3.1): drop.
		n.tracef("HSR frame PathId net %d lan %d (ours net %d lan %d) — reinjection prevented", path>>1, path&1, n.NetID(), n.LanID())
		n.noteDrop("path")
		return
	}

	// hsr-hsr (QuadBox): the frame has already been forwarded to the
	// interlink above — there is no SAN on this port. Return.
	if n.IsHSRHSR() {
		return
	}

	// hsr-san: strip the tag and deliver the original frame to the SAN,
	// subject to the kernel filter (unicast to a ring member stays on the
	// ring) and then the configured SAN forwarding policy.
	if n.unicastToRingMember(event.frame[:6]) {
		n.tracef("HSR unicast to ring member not delivered to SAN")
		n.noteDrop("filter")
		return
	}
	if !n.shouldForwardToInterlink(payload) {
		n.tracef("HSR frame from %s not forwarded to SAN (filtered)", srcMAC)
		n.noteDrop("filter")
		return
	}
	original, err := engine.StripHSR(event.frame)
	if err != nil {
		n.tracef("HSR strip failed: %v", err)
		n.noteDrop("malformed")
		return
	}
	if _, err := n.Interlink.Write(original); err != nil {
		n.tracef("SAN write failed: %v", err)
	} else {
		n.countFrame("interlink", "out")
	}
	_ = encap
}

// handleHSRInterlinkFrame processes a frame arriving from the interlink:
// add an HSR tag (path 0 for hsr-san, (NetId<<1)|LanId for hsr-prp) and
// send it on both ring ports with a fresh sequence number.
func (n *Node) handleHSRInterlinkFrame(event frameEvent) {
	n.countFrame("interlink", "in")
	srcMAC := engine.GetSrcMAC(event.frame)

	// hsr-hsr (QuadBox): the interlink carries HSR-tagged frames from
	// the other ring. Decode, dedup and inject into our ring as a fresh
	// path-0 frame on both ring ports (the peer's ring number is a new
	// path domain).
	if n.IsHSRHSR() {
		_, seq, _, _, err := engine.DecodeHSR(event.frame)
		if err != nil {
			n.tracef("HSR interlink frame: %v", err)
			n.noteDrop("malformed")
			return
		}
		// Own frame returning over the interlink — discard.
		if n.mac != nil && bytesEqual(n.mac, srcMACBytes(event.frame)) {
			n.tracef("HSR interlink frame from own MAC (seq %d) — discarding", seq)
			n.noteDrop("own")
			return
		}
		if n.dupTable.Find(srcMAC, seq) {
			n.tracef("HSR interlink duplicate from %s seq=%d — discarding", srcMAC, seq)
			n.noteDrop("dup")
			return
		}
		n.dupTable.InsertWithExpiry(srcMAC, seq, 0, n.entryForgetMs())

		// Reinject into this ring with path 0 (fresh traversal).
		fwd, err := engine.RewriteHSRPath(event.frame, 0)
		if err != nil {
			n.tracef("HSR interlink rewrite failed: %v", err)
			n.noteDrop("malformed")
			return
		}
		if _, err := n.LanA.Write(fwd); err != nil {
			n.tracef("ring A write failed: %v", err)
		} else {
			n.countFrame("lan_a", "out")
		}
		if _, err := n.LanB.Write(fwd); err != nil {
			n.tracef("ring B write failed: %v", err)
		} else {
			n.countFrame("lan_b", "out")
		}
		n.tracef("HSR frame from ring 2 (seq %d) injected into ring 1 (path 0)", seq)
		return
	}

	// Learn the SAN source so we can later decide which ring frames to
	// forward back (proxy table, used with forward_all=false).
	if len(event.frame) >= 12 {
		n.proxyTableMu.Lock()
		n.proxyTable[srcMAC] = n.nowf()
		n.proxyTableMu.Unlock()
	}

	path := 0
	if n.IsHSRPRPCoupling() {
		path = (n.NetID() << 1) | n.LanID()
	}

	// Sequence number: for hsr-prp coupling the frame arrives from the
	// PRP LAN as an RCT-tagged frame whose sequence number must be
	// PRESERVED as the HSR tag's sequence number. The two coupling
	// RedBoxes (LAN A and LAN B) inject the same (src MAC, seq), so the
	// ring's duplicate detection discards the second copy — exactly-once
	// across the coupling. For hsr-san the SAN frame has no seq, so one
	// is allocated per source MAC.
	var seq uint16
	if n.IsHSRPRPCoupling() {
		_, rctSeq, _, err := engine.DecodeRCT(event.frame)
		if err != nil {
			n.tracef("coupling: no RCT on interlink frame: %v", err)
			n.noteDrop("malformed")
			return
		}
		seq = uint16(rctSeq)
	} else {
		seq = n.seqMgr.Next(srcMAC)
	}

	// Register the injected frame in the duplicate table before sending
	// (kernel hsr_register_frame_out) so the originator recognises its
	// own injected frames when they return after a full lap and discards
	// them instead of delivering them to the SAN a second time.
	n.dupTable.InsertWithExpiry(srcMAC, int(seq), 0, n.entryForgetMs())

	// Preserve the VLAN tag if present; the HSR tag goes after it. The
	// encapsulated EtherType is the frame's own EtherType (or the inner
	// one for VLAN-tagged frames).
	vlan := engine.IsVLANFrame(event.frame)
	et := engine.GetEtherType(event.frame)

	var body []byte
	if vlan {
		// event.frame: dst(6) src(6) TPID(2) TCI(2) innerEtherType(2) ...
		inner := event.frame[18:]
		hsrBody := engine.EncodeHSR(inner, path, seq, et)
		body = append(append(append(append([]byte(nil), event.frame[:12]...),
			event.frame[12:16]...), hsrBody...), []byte{}...)
	} else {
		hsrBody := engine.EncodeHSR(event.frame[14:], path, seq, et)
		body = append(append([]byte(nil), event.frame[:12]...), hsrBody...)
	}

	if _, err := n.LanA.Write(body); err != nil {
		n.tracef("ring A write failed: %v", err)
	} else {
		n.countFrame("lan_a", "out")
	}
	if _, err := n.LanB.Write(body); err != nil {
		n.tracef("ring B write failed: %v", err)
	} else {
		n.countFrame("lan_b", "out")
	}
	n.tracef("interlink frame from %s injected into ring (path %d)", srcMAC, path)
}

// couplingToPRP delivers a ring frame (already decoded: payload = original
// Ethernet frame) onto the coupled PRP LAN with an RCT trailer, preserving
// the HSR sequence number end-to-end.
func (n *Node) couplingToPRP(original []byte, seq int) {
	rct := engine.EncodeRCT(original, uint16(seq), n.LanID())
	if _, err := n.Interlink.Write(rct); err != nil {
		n.tracef("coupling LAN write failed: %v", err)
	} else {
		n.countFrame("interlink", "out")
	}
	n.tracef("ring frame (seq %d) delivered to PRP LAN %d", seq, n.LanID())
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// learnRingMember records srcMAC as a local ring member. Mirrors the
// kernel's hsr_get_node, which creates/refreshes a node_db entry for
// EVERY HSR frame received on a ring port — not only supervision — so a
// restarted node is filtered from its first data frame.
func (n *Node) learnRingMember(srcMAC string) {
	n.ringMembersMu.Lock()
	if n.ringMembers == nil {
		n.ringMembers = make(map[string]time.Time)
	}
	n.ringMembers[srcMAC] = n.nowf()
	n.ringMembersMu.Unlock()
}

// unicastToRingMember reports whether dst is a unicast MAC address of a
// local ring member (kernel hsr_drop_frame: "Do not forward to port C
// (Interlink) frames from nodes A and B if DA is in NodeTable").
func (n *Node) unicastToRingMember(dst []byte) bool {
	if len(dst) != 6 || dst[0]&0x01 == 1 {
		return false // multicast/broadcast always floods
	}
	mac := engine.GetDstMAC(dst)
	n.ringMembersMu.Lock()
	defer n.ringMembersMu.Unlock()
	_, ok := n.ringMembers[mac]
	return ok
}
