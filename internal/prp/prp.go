package prp

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"prp-gns3/internal/engine"
	"prp-gns3/internal/iface"
	"prp-gns3/internal/nodetable"
	"prp-gns3/internal/supervision"
)

// containsInt reports whether v is present in xs.
func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// Node represents a PRP network node.
type Node struct {
	Config            *Config
	LanA              iface.PacketPort
	LanB              iface.PacketPort
	Interlink         iface.PacketPort
	mac               []byte
	supSeq            uint16
	seqMgr            *engine.SequenceManager
	frameBuffer       chan frameEvent
	stopChan          chan struct{}
	stopLock          sync.Mutex
	cleanupTimer      *time.Timer
	supervisionSeen   map[string]time.Time
	supervisionSeqs   map[string]uint16
	supervisionForget time.Duration
	dupTable          *nodetable.Table
	proxyTable        map[string]time.Time
	proxyTableMu      sync.Mutex
	proxyTableForget  time.Duration
	// dropCounters counts discarded frames by reason (dup/own/path/
	// filter/malformed) for the TUI and debugging.
	dropCounters map[string]int
	// counters tracks per-port received/forwarded frame counts.
	counters struct {
		mu       sync.Mutex
		lanAIn   uint64
		lanBIn   uint64
		lanAOut  uint64
		lanBOut  uint64
		interIn  uint64
		interOut uint64
		supSent  uint64
	}
	// now returns the current time; injectable for deterministic tests.
	now func() time.Time
}

// StopChan returns the channel that is closed when the node is stopped.
// StopChan returns the channel that is closed when the node is stopped.
func (n *Node) StopChan() <-chan struct{} {
	return n.stopChan
}

// Config holds the runtime configuration for a PRP node.
type Config struct {
	NodeName           string
	Role               string
	LanAInterface      string
	LanBInterface      string
	InterlinkInterface string
	// LanID ("A"/"B") and NetID (1-6) describe the coupled PRP LAN for
	// the hsr-prp role; empty/0 otherwise.
	LanID          string
	NetID          int
	PRPID          int
	TrailerEnabled bool
	Debug          bool

	// ForwardAll: when true (default), the RedBox forwards all LAN frames
	// to the interlink; when false, only frames destined to SANs learned
	// behind the interlink (plus multicast/broadcast).
	ForwardAll bool
	// VLANFilter: if non-empty, only these VLAN IDs are forwarded from
	// the interlink to the LANs.
	VLANFilter []int
	// MulticastFirstOctet: when non-empty, multicast frames whose
	// destination first octet does not match are not forwarded from the
	// LANs to the interlink.
	MulticastFirstOctet string
	// EntryForgetMs: duplicate-detection entry lifetime.
	EntryForgetMs int
	// MaxNodeTableSize: duplicate-detection table cap.
	MaxNodeTableSize int
	// ProxyNodeForgetMs: how long SAN MACs learned behind the interlink
	// stay valid when ForwardAll is false.
	ProxyNodeForgetMs int
	// NodeForgetMs: how long a peer node is considered alive after its
	// last supervision frame.
	NodeForgetMs int
}

type frameEvent struct {
	iface   string // "lan_a", "lan_b", "interlink"
	frame   []byte
	frameSz int
}

// SetClock replaces the node's time source (used by tests to drive
// supervision/entry expiry deterministically). Also wires it into the
// duplicate-detection table.
func (n *Node) SetClock(now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	n.now = now
	n.dupTable.SetClock(now)
}

// nowf returns the node's current time source (wall clock by default).
func (n *Node) nowf() time.Time {
	if n.now == nil {
		return time.Now()
	}
	return n.now()
}

// NewNode creates a new PRP node from the given configuration.
func NewNode(cfg *Config) *Node {
	if cfg == nil {
		cfg = &Config{}
	}
	maxSize := cfg.MaxNodeTableSize
	if maxSize <= 0 {
		maxSize = 256
	}
	forgetMs := cfg.EntryForgetMs
	if forgetMs <= 0 {
		forgetMs = 640
	}
	proxyForgetMs := cfg.ProxyNodeForgetMs
	if proxyForgetMs <= 0 {
		proxyForgetMs = 64000 // supervision.proxy_node_forget_time default (64s)
	}
	supForgetMs := cfg.NodeForgetMs
	if supForgetMs <= 0 {
		supForgetMs = 64000 // supervision.node_forget_time default (64s)
	}
	return &Node{
		Config:            cfg,
		seqMgr:            engine.NewSequenceManagerWithStart(randSeqStart()),
		frameBuffer:       make(chan frameEvent, 10000),
		stopChan:          make(chan struct{}),
		dupTable:          nodetable.NewTable(maxSize),
		proxyTable:        make(map[string]time.Time),
		proxyTableForget:  time.Duration(proxyForgetMs) * time.Millisecond,
		supervisionForget: time.Duration(supForgetMs) * time.Millisecond,
		supervisionSeqs:   make(map[string]uint16),
		now:               time.Now,
	}
}

// randSeqStart returns a random 16-bit sequence-number start. PRP sequence
// numbers are only meaningful relative to one another within the duplicate-
// detection window; a random start prevents a restarted node from reusing
// seq 0..k inside a peer's entry_forget window (which would wrongly
// discard its first frames after restart as duplicates).
func randSeqStart() uint16 {
	var b [2]byte
	if _, err := rand.Read(b[:]); err == nil {
		return binary.BigEndian.Uint16(b[:])
	}
	return uint16(time.Now().UnixNano() & 0xffff)
}

// Start initializes interfaces and starts the main processing loops.
func (n *Node) Start() error {
	// Bind raw socket to LAN A first: its hardware address is the node
	// identity (used as the RedBox identity and for the unicast
	// destination filter).
	lanA, err := iface.CreateRawSocket(n.Config.LanAInterface)
	if err != nil {
		return fmt.Errorf("bind LAN A (%s): %w", n.Config.LanAInterface, err)
	}
	n.LanA = lanA
	log.Printf("prp: bound to %s (index %d, MTU %d)", lanA.Name(), lanA.IfIndex(), lanA.MTU())

	// The node MAC is the MAC of the LAN A interface (real hardware MAC,
	// unique per node — never derived from the PRP ID, which is shared by
	// all nodes in the same PRP network).
	n.mac = n.nodeMAC()

	// Raise the MTU of both PRP LAN ports so a full-size SAN frame
	// (1514 B) plus the 6-byte RCT trailer fits on the wire. Standard
	// MTU 1500 only allows 1500+18 = 1518 B; we need 1520.
	for _, l := range []struct {
		port string
		name string
	}{{"LAN A", n.Config.LanAInterface}, {"LAN B", n.Config.LanBInterface}} {
		if err := iface.SetMTU(l.name, 1506); err != nil {
			n.closePorts()
			return fmt.Errorf("set MTU on %s (%s): %w", l.port, l.name, err)
		}
	}
	log.Printf("prp: set MTU 1506 on %s and %s (RCT overhead)", n.Config.LanAInterface, n.Config.LanBInterface)

	lanB, err := iface.CreateRawSocket(n.Config.LanBInterface)
	if err != nil {
		n.closePorts()
		return fmt.Errorf("bind LAN B (%s): %w", n.Config.LanBInterface, err)
	}
	n.LanB = lanB
	log.Printf("prp: bound to %s (index %d, MTU %d)", lanB.Name(), lanB.IfIndex(), lanB.MTU())

	// Bind the interlink: a SAN port (prp-san, hsr-san), the coupled PRP
	// LAN (hsr-prp), or the second HSR ring (hsr-hsr).
	if n.Config.InterlinkInterface != "" {
		interlink, err := iface.CreateRawSocket(n.Config.InterlinkInterface)
		if err != nil {
			n.closePorts()
			return fmt.Errorf("bind interlink (%s): %w", n.Config.InterlinkInterface, err)
		}
		n.Interlink = interlink
		log.Printf("prp: bound to interlink %s (index %d, MTU %d)", interlink.Name(), interlink.IfIndex(), interlink.MTU())

		// Raise the interlink MTU too so a full 1514-byte SAN frame
		// (e.g. VLAN-tagged) forwarded from the LANs fits after the RCT
		// is stripped.
		if err := iface.SetMTU(n.Config.InterlinkInterface, 1506); err != nil {
			n.closePorts()
			return fmt.Errorf("set MTU on interlink (%s): %w", n.Config.InterlinkInterface, err)
		}
		log.Printf("prp: set MTU 1506 on interlink %s", n.Config.InterlinkInterface)
	}

	// Start cleanup timer for node table
	n.cleanupTimer = time.AfterFunc(1*time.Second, n.periodicCleanup)

	// Start processing loops
	go n.readLoop(n.LanA, "lan_a")
	go n.readLoop(n.LanB, "lan_b")

	if n.Interlink != nil {
		go n.readLoop(n.Interlink, "interlink")
	}

	go n.processFrames()

	log.Printf("prp: node %s started in %s mode", n.Config.NodeName, n.Config.Role)
	return nil
}

// closePorts closes every bound port (used on startup failure so no
// interfaces are left half-configured).
func (n *Node) closePorts() {
	if n.LanA != nil {
		n.LanA.Close()
	}
	if n.LanB != nil {
		n.LanB.Close()
	}
	if n.Interlink != nil {
		n.Interlink.Close()
	}
}

// Stop shuts down the node and releases all resources.
func (n *Node) Stop() {
	n.stopLock.Lock()
	defer n.stopLock.Unlock()

	select {
	case <-n.stopChan:
		// Already closed
		return
	default:
		close(n.stopChan)
	}

	if n.cleanupTimer != nil {
		n.cleanupTimer.Stop()
	}

	if n.LanA != nil {
		n.LanA.Close()
	}
	if n.LanB != nil {
		n.LanB.Close()
	}
	if n.Interlink != nil {
		n.Interlink.Close()
	}

	log.Printf("prp: node %s stopped", n.Config.NodeName)
}

// periodicCleanup removes expired entries from the node table and drops
// peers that have not sent a supervision frame within node_forget_time.
func (n *Node) periodicCleanup() {
	if n.dupTable.Cleanup() > 0 && n.Config.Debug {
		// Only log when debugging; during normal traffic entries expire
		// every second and this would flood the log panel.
		log.Printf("prp: node table cleaned up, size now %d", n.dupTable.Size())
	}
	n.cleanupSupervisionSeen()
	n.cleanupTimer.Reset(1 * time.Second)
}

// cleanupSupervisionSeen forgets peers whose last supervision frame is
// older than supervision.node_forget_time. Keeps the liveness map bounded.
func (n *Node) cleanupSupervisionSeen() {
	if len(n.supervisionSeen) == 0 {
		return
	}
	cutoff := n.nowf().Add(-n.supervisionForget)
	for mac, last := range n.supervisionSeen {
		if last.Before(cutoff) {
			delete(n.supervisionSeen, mac)
		}
	}
}

// readLoop reads frames from a raw socket interface.
func (n *Node) readLoop(rs iface.PacketPort, ifaceName string) {
	buf := make([]byte, 2048)

	for {
		select {
		case <-n.stopChan:
			return
		default:
		}

		nr, err := rs.Read(buf)
		if err != nil {
			log.Printf("prp: read error on %s: %v", ifaceName, err)
			time.Sleep(10 * time.Millisecond)
			continue
		}

		// Copy the frame to avoid buffer reuse
		frame := make([]byte, nr)
		copy(frame, buf[:nr])

		select {
		case n.frameBuffer <- frameEvent{
			iface:   ifaceName,
			frame:   frame,
			frameSz: nr,
		}:
		default:
			// Buffer full, drop frame
			log.Printf("prp: frame buffer full, dropping frame on %s", ifaceName)
		}
	}
}

// processFrames is the main event loop for frame processing.
func (n *Node) processFrames() {
	for {
		select {
		case <-n.stopChan:
			return
		case event := <-n.frameBuffer:
			n.handleFrame(event)
		}
	}
}

// handleFrame processes an incoming frame based on its source interface.
func (n *Node) handleFrame(event frameEvent) {
	frame := event.frame

	// Skip frames that are too short to be valid Ethernet
	if len(frame) < 14 {
		return
	}

	// Get EtherType to classify the frame
	etherType := engine.GetEtherType(frame)

	// Handle supervision frames (0x88fb). A 0x88fb frame arriving on the
	// PRP LANs or (hsr-hsr) on the interlink is supervision traffic and
	// is consumed locally. A 0x88fb frame arriving on the interlink of a
	// prp-san/hsr-san node is ordinary data (e.g. SAN traffic) and must
	// be forwarded like any other frame.
	supervision := etherType == 0x88fb && (event.iface == "lan_a" || event.iface == "lan_b" ||
		(n.IsHSRHSR() && event.iface == "interlink"))
	if supervision {
		n.handleSupervisionFrame(event)
		// hsr-hsr (QuadBox): forward ring supervision onto the other
		// ring via the interlink (and vice versa) so both rings learn
		// each other's nodes.
		if n.IsHSRHSR() {
			if event.iface == "lan_a" || event.iface == "lan_b" {
				if n.Interlink != nil {
					if _, err := n.Interlink.Write(event.frame); err == nil {
						n.countFrame("interlink", "out")
					}
				}
			} else {
				if _, err := n.LanA.Write(event.frame); err == nil {
					n.countFrame("lan_a", "out")
				}
				if _, err := n.LanB.Write(event.frame); err == nil {
					n.countFrame("lan_b", "out")
				}
			}
		}
		return
	}

	switch event.iface {
	case "lan_a", "lan_b":
		if n.IsHSRNode() {
			// HSR ring port: tag-based ring forwarding.
			n.handleIncomingHSRFrame(event)
		} else {
			// PRP LAN: RCT-based receive path.
			n.handleIncomingPRPFrame(event)
		}
	case "interlink":
		if n.IsHSRNode() {
			// SAN (or coupled PRP LAN) → ring.
			n.handleHSRInterlinkFrame(event)
		} else {
			n.handleInterlinkFrame(event)
		}
	}
}

// handleIncomingPRPFrame processes frames arriving from PRP LANs.
// Flow: LAN → check RCT → dup detection → strip RCT → forward to interlink
func (n *Node) handleIncomingPRPFrame(event frameEvent) {
	n.countFrame(event.iface, "in")
	if n.Config.Debug {
		log.Printf("prp: incoming PRP frame on %s (%d bytes)", event.iface, event.frameSz)
	}

	// Check if this frame has an RCT trailer
	if !engine.IsPRPFrame(event.frame) {
		// No RCT trailer - this is a non-PRP frame on the PRP LAN
		// Log and discard
		n.noteDrop("malformed")
		if n.Config.Debug {
			log.Printf("prp: frame without RCT on %s, discarding", event.iface)
		}
		return
	}

	// Decode the RCT trailer
	lanID, seq, payload, err := engine.DecodeRCT(event.frame)
	if err != nil {
		if n.Config.Debug {
			log.Printf("prp: invalid RCT on %s: %v", event.iface, err)
		}
		return
	}

	// A frame received on LAN A must carry lan_id=0; on LAN B lan_id=1.
	// Mismatches indicate a bridged/looped network and are dropped.
	expected := 0
	if event.iface == "lan_b" {
		expected = 1
	}
	if lanID != expected {
		n.noteDrop("path")
		if n.Config.Debug {
			log.Printf("prp: lan_id %d on %s (expected %d), dropping", lanID, event.iface, expected)
		}
		return
	}

	// Get source MAC for duplicate detection
	srcMAC := engine.GetSrcMAC(event.frame)

	// Never forward frames sourced from this node itself. PACKET_IGNORE_OUTGOING
	// already prevents self-reception, but this guards against bridged LANs.
	if bytes.Equal(n.mac, srcMACBytes(event.frame)) && n.mac != nil {
		if n.Config.Debug {
			log.Printf("prp: frame from own MAC on %s, discarding", event.iface)
		}
		return
	}

	// Check for duplicate
	if n.dupTable.Find(srcMAC, seq) {
		n.noteDrop("dup")
		if n.Config.Debug {
			log.Printf("prp: duplicate frame from %s seq=%d on %s, discarding", srcMAC, seq, event.iface)
		}
		return
	}

	// Accept the frame - add to node table
	n.dupTable.InsertWithExpiry(srcMAC, seq, lanID, n.entryForgetMs())

	// Forward based on role (prp-san is the legacy "redbox" path).
	if !n.IsHSRNode() && n.Interlink != nil {
		if !n.shouldForwardToInterlink(payload) {
			n.noteDrop("filter")
			if n.Config.Debug {
				log.Printf("prp: frame from %s not forwarded to interlink (filtered)", srcMAC)
			}
			return
		}
		_, err := n.Interlink.Write(payload)
		if err != nil {
			log.Printf("prp: failed to write to interlink: %v", err)
		} else {
			n.countFrame("interlink", "out")
		}
		if n.Config.Debug {
			log.Printf("prp: forwarded frame (%d bytes) to interlink", len(payload))
		}
	}
}

// entryForgetMs returns the configured duplicate-detection entry lifetime.
func (n *Node) entryForgetMs() int {
	if n.Config.EntryForgetMs > 0 {
		return n.Config.EntryForgetMs
	}
	return 640
}

// srcMACBytes returns the raw 6-byte source MAC of an Ethernet frame.
func srcMACBytes(frame []byte) []byte {
	if len(frame) < 12 {
		return nil
	}
	return frame[6:12]
}

// shouldForwardToInterlink decides whether a LAN frame is forwarded to the
// SAN. With ForwardAll (default) everything passes. Otherwise only frames
// destined to SANs learned behind the interlink, or multicast/broadcast
// frames matching the configured multicast filter.
func (n *Node) shouldForwardToInterlink(frame []byte) bool {
	if n.Config.ForwardAll {
		return true
	}
	if engine.IsMulticastMAC(frame) {
		return n.multicastAllowed(frame)
	}
	// Unicast: forward only if the destination was learned behind the
	// interlink (proxy table).
	dst := engine.GetDstMAC(frame)
	n.proxyTableMu.Lock()
	seen, ok := n.proxyTable[dst]
	n.proxyTableMu.Unlock()
	if ok && n.nowf().Sub(seen) <= n.proxyTableForget {
		return true
	}
	return false
}

// multicastAllowed applies the configured multicast filter. The pattern is
// a hex-byte prefix of the destination MAC, e.g. "01" (IPv4 multicast),
// "01-00-5E" or "33-33". An empty or unparsable pattern allows all
// multicast.
func (n *Node) multicastAllowed(frame []byte) bool {
	// Broadcast (ff:ff:ff:ff:ff:ff) must ALWAYS pass: it is technically
	// multicast (first octet odd) but a RedBox must flood it regardless
	// of the configured multicast prefix filter, or ARP/ND break.
	if len(frame) >= 6 {
		allFF := true
		for _, b := range frame[0:6] {
			if b != 0xff {
				allFF = false
				break
			}
		}
		if allFF {
			return true
		}
	}
	prefix := parseMulticastPattern(n.Config.MulticastFirstOctet)
	if len(prefix) == 0 {
		return true
	}
	if len(frame) < len(prefix) {
		return false
	}
	for i, b := range prefix {
		if frame[i] != b {
			return false
		}
	}
	return true
}

// parseMulticastPattern converts a multicast filter pattern ("01",
// "01-00-5E", "33:33", "0x01005e") into the destination-MAC byte prefix it
// matches. Returns nil when the pattern is empty or malformed (in which
// case the filter is disabled).
func parseMulticastPattern(pattern string) []byte {
	hex := strings.ToLower(pattern)
	hex = strings.TrimPrefix(hex, "0x")
	hex = strings.ReplaceAll(hex, "-", "")
	hex = strings.ReplaceAll(hex, ":", "")
	if hex == "" || len(hex)%2 != 0 || len(hex) > 12 {
		return nil
	}
	out := make([]byte, 0, len(hex)/2)
	for i := 0; i < len(hex); i += 2 {
		b, err := strconv.ParseUint(hex[i:i+2], 16, 8)
		if err != nil {
			return nil
		}
		out = append(out, byte(b))
	}
	return out
}

// handleInterlinkFrame processes frames arriving from the interlink.
// Flow: Interlink → add RCT → duplicate to both LANs.
// Both LAN copies MUST carry the same sequence number (PRP duplicate
// detection on the receiver keys on (src MAC, seq)).
func (n *Node) handleInterlinkFrame(event frameEvent) {
	n.countFrame("interlink", "in")
	if n.Config.Debug {
		log.Printf("prp: interlink frame (%d bytes)", event.frameSz)
	}

	// VLAN filter: when configured, only the listed VLAN IDs pass.
	if len(n.Config.VLANFilter) > 0 {
		if vlanID, tagged := engine.GetVLANID(event.frame); !tagged || !containsInt(n.Config.VLANFilter, vlanID) {
			n.noteDrop("filter")
			if n.Config.Debug {
				log.Printf("prp: interlink frame VLAN %d filtered out", vlanID)
			}
			return
		}
	}

	// Learn the SAN source MAC so the RedBox can later decide which
	// LAN frames to forward back to the interlink.
	if len(event.frame) >= 12 {
		src := engine.GetSrcMAC(event.frame)
		n.proxyTableMu.Lock()
		n.proxyTable[src] = n.nowf()
		n.proxyTableMu.Unlock()
	}

	srcMAC := engine.GetSrcMAC(event.frame)

	if !n.Config.TrailerEnabled {
		// No RCT - just forward to both LANs as-is
		n.LanA.Write(event.frame)
		n.LanB.Write(event.frame)
		n.countFrame("lan_a", "out")
		n.countFrame("lan_b", "out")
		return
	}

	// Allocate ONE sequence number for this frame; both copies use it.
	seq := n.seqMgr.Next(srcMAC)

	// LAN A = 0, LAN B = 1 (IEC 62439-3 / kernel numbering).
	lanAFrame := engine.EncodeRCT(event.frame, seq, 0)
	lanBFrame := engine.EncodeRCT(event.frame, seq, 1)

	// Send to both LANs
	if _, err := n.LanA.Write(lanAFrame); err != nil {
		log.Printf("prp: failed to write to LAN A: %v", err)
	} else {
		n.countFrame("lan_a", "out")
	}
	if _, err := n.LanB.Write(lanBFrame); err != nil {
		log.Printf("prp: failed to write to LAN B: %v", err)
	} else {
		n.countFrame("lan_b", "out")
	}

	if n.Config.Debug {
		log.Printf("prp: duplicated frame (seq %d) to both LANs", seq)
	}
}

// handleSupervisionFrame processes received PRP supervision frames (0x88fb).
// Supervision frames are consumed locally — a RedBox must NOT forward them
// to the interlink (SAN) as data traffic.
func (n *Node) handleSupervisionFrame(event frameEvent) {
	sup := supervision.Parse(event.frame)
	if sup == nil {
		if n.Config.Debug {
			log.Printf("prp: unparseable supervision frame on %s", event.iface)
		}
		return
	}
	if n.Config.Debug {
		log.Printf("prp: supervision from %s on %s (seq %d, tlv %d)", sup.SrcMAC, event.iface, sup.Seq, sup.TLVType)
	}
	// Track liveness of the sending node.
	if n.supervisionSeen == nil {
		n.supervisionSeen = make(map[string]time.Time)
	}
	n.supervisionSeen[sup.SrcMAC] = n.nowf()

	// A regression of the supervision sequence number (16-bit, wrap-aware)
	// means the peer restarted: its counters were reset, so the duplicate-
	// detection entries we hold for it describe a previous incarnation.
	// Flush them; otherwise the fresh low sequence numbers the restarted
	// node emits would be wrongly discarded as stale duplicates.
	if last, ok := n.supervisionSeqs[sup.SrcMAC]; ok {
		if diff := int16(sup.Seq - last); diff < 0 {
			if removed := n.dupTable.FlushFor(sup.SrcMAC); removed > 0 {
				log.Printf("prp: peer %s restarted (supervision seq %d -> %d), flushed %d dup entries", sup.SrcMAC, last, sup.Seq, removed)
			} else if n.Config.Debug {
				log.Printf("prp: peer %s supervision seq regressed %d -> %d (restart detected)", sup.SrcMAC, last, sup.Seq)
			}
		}
	}
	// Record for the next frame (needed for cross-restart regression
	// detection on the very first supervision frame after a restart).
	n.supervisionSeqs[sup.SrcMAC] = sup.Seq
}

// SendSupervisionFrame sends supervision frames appropriate to the node's
// role:
//   - prp-san: PRP supervision with RCT on both LANs.
//   - hsr-san: HSR supervision (no RCT) on both ring ports.
//   - hsr-prp: HSR supervision on the ring ports AND PRP supervision with
//     RCT on the coupled PRP LAN (the two sides speak different
//     supervision dialects; IEC 62439-3 §5.2.2.3.2 requires the RedBox to
//     present each side with its own format).
//   - hsr-hsr: HSR supervision on the ring ports AND on the interlink so
//     both rings learn this node.
func (n *Node) SendSupervisionFrame() {
	srcMAC := n.mac
	if srcMAC == nil {
		srcMAC = n.nodeMAC()
	}
	n.supSeq++

	if !n.IsHSRNode() {
		frameA := supervision.BuildRCTed(srcMAC, n.supSeq, 0)
		frameB := supervision.BuildRCTed(srcMAC, n.supSeq, 1)
		if _, err := n.LanA.Write(frameA); err != nil {
			log.Printf("prp: failed to write supervision to LAN A: %v", err)
		}
		if _, err := n.LanB.Write(frameB); err != nil {
			log.Printf("prp: failed to write supervision to LAN B: %v", err)
		}
		n.counters.mu.Lock()
		n.counters.supSent++
		n.counters.mu.Unlock()
		n.tracef("supervision frame (seq %d) sent on both LANs", n.supSeq)
		return
	}

	// HSR node: supervision on the ring carries the HSR path.
	path := 0
	if n.IsHSRPRPCoupling() {
		path = (n.NetID() << 1) | n.LanID()
	}
	sup := supervision.BuildHSR(srcMAC, n.supSeq, path)
	if _, err := n.LanA.Write(sup); err != nil {
		log.Printf("prp: failed to write supervision to ring A: %v", err)
	}
	if _, err := n.LanB.Write(sup); err != nil {
		log.Printf("prp: failed to write supervision to ring B: %v", err)
	}
	n.counters.mu.Lock()
	n.counters.supSent++
	n.counters.mu.Unlock()
	n.tracef("HSR supervision frame (seq %d, path %d) sent on ring", n.supSeq, path)

	// hsr-hsr (QuadBox): also announce on the interlink so the other
	// ring learns this node.
	if n.IsHSRHSR() && n.Interlink != nil {
		if _, err := n.Interlink.Write(sup); err != nil {
			log.Printf("prp: failed to write supervision to interlink: %v", err)
		}
	}

	// hsr-prp: also announce on the coupled PRP LAN in PRP dialect.
	if n.IsHSRPRPCoupling() && n.Interlink != nil {
		lanFrame := supervision.BuildRCTed(srcMAC, n.supSeq, n.LanID())
		if _, err := n.Interlink.Write(lanFrame); err != nil {
			log.Printf("prp: failed to write supervision to coupled LAN: %v", err)
		}
	}
}

// nodeMAC returns the node's MAC address. It must never be derived from
// the PRP ID — all nodes in a PRP network share the PRP ID, which would
// cause MAC collisions. Precedence: the LAN A interface hardware address,
// then any MAC set programmatically
// (tests), then a synthetic locally-administered MAC derived from the PRP
// ID as a last resort.
func (n *Node) nodeMAC() []byte {
	// Use the LAN A interface hardware address when bound to a real
	// interface.
	if n.Config.LanAInterface != "" && n.mac == nil {
		if iface, err := net.InterfaceByName(n.Config.LanAInterface); err == nil {
			return append([]byte(nil), iface.HardwareAddr...)
		}
	}
	// Fallback for tests / synthetic ports.
	if n.mac != nil {
		return n.mac
	}
	mac := make([]byte, 6)
	mac[0] = 0x02
	mac[1] = 0x50
	mac[2] = 0x50
	mac[3] = byte(n.Config.PRPID >> 8)
	mac[4] = byte(n.Config.PRPID)
	mac[5] = 0x00
	return mac
}

// SetNodeMAC allows tests (and callers without a real LAN A interface) to
// set an explicit node MAC.
func (n *Node) SetNodeMAC(mac []byte) {
	n.mac = append([]byte(nil), mac...)
}
