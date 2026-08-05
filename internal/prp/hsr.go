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

// handleIncomingHSRFrame processes a frame arriving on a ring port.
// Flow: decode tag → own-frame discard → dup detection → forward to the
// other ring port (path 0→1) → deliver to the interlink unless duplicate.
func (n *Node) handleIncomingHSRFrame(event frameEvent) {
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

	// Forward around the ring: path 0 frames continue (with path set to
	// 1); path 1 frames have already travelled one direction and are not
	// forwarded again.
	if path == 0 {
		fwd, err := engine.RewriteHSRPath(event.frame, 1)
		if err == nil {
			port := n.LanB
			if other == "lan_a" {
				port = n.LanA
			}
			if _, err := port.Write(fwd); err != nil {
				n.tracef("HSR forward to %s failed: %v", other, err)
			} else {
				n.tracef("HSR frame from %s (seq %d) forwarded to %s (path 1)", event.iface, seq, other)
			}
		}
	} else {
		n.tracef("HSR frame on %s has path %d — not forwarded", event.iface, path)
	}

	// Deliver to the interlink. In hsr-prp coupling the interlink is a
	// PRP LAN and the PathId rule applies; in hsr-san it is a plain SAN.
	if n.IsHSRPRPCoupling() {
		if path>>1 != n.NetID() {
			// Different NetId (pure ring or another coupled network):
			// forward onto our PRP LAN.
			n.couplingToPRP(event.frame, payload, seq)
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

	// hsr-san: strip the tag and deliver the original frame to the SAN.
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
	}
	_ = encap
}

// handleHSRInterlinkFrame processes a frame arriving from the interlink:
// add an HSR tag (path 0 for hsr-san, (NetId<<1)|LanId for hsr-prp) and
// send it on both ring ports with a fresh sequence number.
func (n *Node) handleHSRInterlinkFrame(event frameEvent) {
	srcMAC := engine.GetSrcMAC(event.frame)

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

	// Preserve the VLAN tag if present; the HSR tag goes after it. The
	// encapsulated EtherType is the frame's own EtherType (or the inner
	// one for VLAN-tagged frames).
	vlan := engine.IsVLANFrame(event.frame)
	et := engine.GetEtherType(event.frame)

	var body []byte
	if vlan {
		// event.frame: dst(6) src(6) TPID(2) TCI(2) innerEtherType(2) ...
		inner := event.frame[18:]
		hsrBody := engine.EncodeHSR(inner, path, n.seqMgr.Next(srcMAC), et)
		body = append(append(append(append([]byte(nil), event.frame[:12]...),
			event.frame[12:16]...), hsrBody...), []byte{}...)
	} else {
		hsrBody := engine.EncodeHSR(event.frame[14:], path, n.seqMgr.Next(srcMAC), et)
		body = append(append([]byte(nil), event.frame[:12]...), hsrBody...)
	}

	if _, err := n.LanA.Write(body); err != nil {
		n.tracef("ring A write failed: %v", err)
	}
	if _, err := n.LanB.Write(body); err != nil {
		n.tracef("ring B write failed: %v", err)
	}
	n.tracef("interlink frame from %s injected into ring (path %d)", srcMAC, path)
}

// couplingToPRP delivers a ring frame (already decoded: payload = original
// Ethernet frame) onto the coupled PRP LAN with an RCT trailer, preserving
// the HSR sequence number end-to-end.
func (n *Node) couplingToPRP(frame []byte, payload []byte, seq int) {
	// payload is the original Ethernet frame (dst/src/ethertype/...).
	rct := engine.EncodeRCT(payload, uint16(seq), n.LanID())
	if _, err := n.Interlink.Write(rct); err != nil {
		n.tracef("coupling LAN write failed: %v", err)
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
