package prp

import (
	"encoding/binary"
	"fmt"
	"log"
	"sync"
	"time"

	"prp-gns3/internal/engine"
	"prp-gns3/internal/iface"
	"prp-gns3/internal/nodetable"
)

// Node represents a PRP network node.
type Node struct {
	Config       *Config
	LanA         *iface.RawSocket
	LanB         *iface.RawSocket
	Interlink    *iface.RawSocket
	Tap          *iface.TAPSocket
	seqCounters  map[string]uint16
	frameBuffer  chan frameEvent
	stopChan     chan struct{}
	stopLock     sync.Mutex
	cleanupTimer *time.Timer
}

// StopChan returns the channel that is closed when the node is stopped.
func (n *Node) StopChan() <-chan struct{} {
	return n.stopChan
}

// Config holds the runtime configuration for a PRP node.
type Config struct {
	NodeName          string
	Role              string
	LanAInterface     string
	LanBInterface     string
	InterlinkInterface string
	TapName           string
	TapMAC            string
	PRPID             int
	TrailerEnabled    bool
	Debug             bool
}

type frameEvent struct {
	iface   string // "lan_a", "lan_b", "tap", "interlink"
	frame   []byte
	frameSz int
}

// NewNode creates a new PRP node from the given configuration.
func NewNode(cfg *Config) *Node {
	return &Node{
		Config:      cfg,
		seqCounters: make(map[string]uint16),
		frameBuffer: make(chan frameEvent, 10000),
		stopChan:    make(chan struct{}),
	}
}

// Start initializes interfaces and starts the main processing loops.
func (n *Node) Start() error {
	// Create TAP interface (for DAN mode, but also useful in RedBox)
	if n.Config.TapName != "" {
		tap, err := iface.CreateTAP(n.Config.TapName, n.Config.TapMAC)
		if err != nil {
			return fmt.Errorf("create TAP: %w", err)
		}
		n.Tap = tap
		log.Printf("prp: TAP interface %s created", tap.Name())
	}

	// Bind raw sockets to physical interfaces
	lanA, err := iface.CreateRawSocket(n.Config.LanAInterface)
	if err != nil {
		return fmt.Errorf("bind LAN A (%s): %w", n.Config.LanAInterface, err)
	}
	n.LanA = lanA
	log.Printf("prp: bound to %s (index %d, MTU %d)", lanA.Name(), lanA.IfIndex(), lanA.MTU())

	lanB, err := iface.CreateRawSocket(n.Config.LanBInterface)
	if err != nil {
		lanA.Close()
		return fmt.Errorf("bind LAN B (%s): %w", n.Config.LanBInterface, err)
	}
	n.LanB = lanB
	log.Printf("prp: bound to %s (index %d, MTU %d)", lanB.Name(), lanB.IfIndex(), lanB.MTU())

	// Bind interlink (for RedBox mode)
	if n.Config.Role == "redbox" && n.Config.InterlinkInterface != "" {
		interlink, err := iface.CreateRawSocket(n.Config.InterlinkInterface)
		if err != nil {
			lanA.Close()
			lanB.Close()
			return fmt.Errorf("bind interlink (%s): %w", n.Config.InterlinkInterface, err)
		}
		n.Interlink = interlink
		log.Printf("prp: bound to interlink %s (index %d, MTU %d)", interlink.Name(), interlink.IfIndex(), interlink.MTU())
	}

	// Start cleanup timer for node table
	n.cleanupTimer = time.AfterFunc(1*time.Second, n.periodicCleanup)

	// Start processing loops
	go n.readLoop(n.LanA, "lan_a")
	go n.readLoop(n.LanB, "lan_b")

	if n.Interlink != nil {
		go n.readLoop(n.Interlink, "interlink")
	}

	if n.Tap != nil {
		go n.tapReadLoop()
	}

	go n.processFrames()

	log.Printf("prp: node %s started in %s mode", n.Config.NodeName, n.Config.Role)
	return nil
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

	if n.Tap != nil {
		n.Tap.Close()
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

// periodicCleanup removes expired entries from the node table.
func (n *Node) periodicCleanup() {
	if nodetable.Cleanup() > 0 {
		log.Printf("prp: node table cleaned up, size now %d", nodetable.Size())
	}
	n.cleanupTimer.Reset(1 * time.Second)
}

// readLoop reads frames from a raw socket interface.
func (n *Node) readLoop(rs *iface.RawSocket, ifaceName string) {
	buf := make([]byte, rs.MTU()+512)

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

// tapReadLoop reads frames from the TAP interface.
func (n *Node) tapReadLoop() {
	buf := make([]byte, n.TapMTU()+512)

	for {
		select {
		case <-n.stopChan:
			return
		default:
		}

		nr, err := n.Tap.Read(buf)
		if err != nil {
			log.Printf("prp: TAP read error: %v", err)
			time.Sleep(10 * time.Millisecond)
			continue
		}

		frame := make([]byte, nr)
		copy(frame, buf[:nr])

		select {
		case n.frameBuffer <- frameEvent{
			iface:   "tap",
			frame:   frame,
			frameSz: nr,
		}:
		default:
			log.Printf("prp: frame buffer full, dropping TAP frame")
		}
	}
}

func (n *Node) TapMTU() int {
	if n.Tap != nil {
		return 1500
	}
	return 1500
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

	// Handle supervision frames (0x88fb)
	if etherType == 0x88fb {
		n.handleSupervisionFrame(event)
		return
	}

	switch event.iface {
	case "lan_a", "lan_b":
		// Incoming from PRP LANs - handle receive path
		n.handleIncomingPRPFrame(event)
	case "interlink":
		// Incoming from interlink (SAN in RedBox mode) - handle transmit path
		n.handleInterlinkFrame(event)
	case "tap":
		// Incoming from TAP (DAN mode) - handle as application frame
		n.handleDANFrame(event)
	}
}

// handleIncomingPRPFrame processes frames arriving from PRP LANs.
// Flow: LAN → check RCT → dup detection → strip RCT → forward to interlink/TAP
func (n *Node) handleIncomingPRPFrame(event frameEvent) {
	if n.Config.Debug {
		log.Printf("prp: incoming PRP frame on %s (%d bytes)", event.iface, event.frameSz)
	}

	// Check if this frame has an RCT trailer
	if !engine.IsPRPFrame(event.frame) {
		// No RCT trailer - this is a non-PRP frame on the PRP LAN
		// Log and discard
		if n.Config.Debug {
			log.Printf("prp: frame without RCT on %s, discarding", event.iface)
		}
		return
	}

	// Decode the RCT trailer
	lanID, seq, payload, err := engine.DecodeRCT(event.frame)
	if err != nil {
		log.Printf("prp: failed to decode RCT: %v", err)
		return
	}

	// Get source MAC for duplicate detection
	srcMAC := engine.GetSrcMAC(event.frame)

	// Check for duplicate
	if nodetable.Find(srcMAC, seq) {
		if n.Config.Debug {
			log.Printf("prp: duplicate frame from %s seq=%d on %s, discarding", srcMAC, seq, event.iface)
		}
		return
	}

	// Accept the frame - add to node table
	nodetable.InsertWithExpiry(srcMAC, seq, lanID, 640)

	// Forward based on role
	if n.Config.Role == "redbox" && n.Interlink != nil {
		// RedBox: forward to interlink
		_, err := n.Interlink.Write(payload)
		if err != nil {
			log.Printf("prp: failed to write to interlink: %v", err)
		}
		if n.Config.Debug {
			log.Printf("prp: forwarded frame (%d bytes) to interlink", len(payload))
		}
	} else if n.Tap != nil {
		// DAN: forward to TAP interface
		_, err := n.Tap.Write(payload)
		if err != nil {
			log.Printf("prp: failed to write to TAP: %v", err)
		}
	}
}

// handleInterlinkFrame processes frames arriving from the interlink.
// Flow: Interlink → add RCT → duplicate to both LANs
func (n *Node) handleInterlinkFrame(event frameEvent) {
	if n.Config.Debug {
		log.Printf("prp: interlink frame (%d bytes)", event.frameSz)
	}

	if !n.Config.TrailerEnabled {
		// No RCT - just forward to both LANs as-is
		n.LanA.Write(event.frame)
		n.LanB.Write(event.frame)
		return
	}

	// Add RCT trailer to the frame
	srcMAC := engine.GetSrcMAC(event.frame)
	lanAFrame := engine.EncodeRCT(event.frame, srcMAC, 1)
	lanBFrame := engine.EncodeRCT(event.frame, srcMAC, 2)

	// Send to both LANs
	if _, err := n.LanA.Write(lanAFrame); err != nil {
		log.Printf("prp: failed to write to LAN A: %v", err)
	}
	if _, err := n.LanB.Write(lanBFrame); err != nil {
		log.Printf("prp: failed to write to LAN B: %v", err)
	}

	if n.Config.Debug {
		log.Printf("prp: duplicated frame to both LANs (seq managed per src MAC)")
	}
}

// handleDANFrame processes frames arriving from the TAP interface.
// In DAN mode, these are application frames that need RCT added before sending to LANs.
func (n *Node) handleDANFrame(event frameEvent) {
	if n.Config.Debug {
		log.Printf("prp: DAN frame from TAP (%d bytes)", event.frameSz)
	}

	srcMAC := engine.GetSrcMAC(event.frame)

	if !n.Config.TrailerEnabled {
		// No RCT - just forward to both LANs
		n.LanA.Write(event.frame)
		n.LanB.Write(event.frame)
		return
	}

	// Add RCT trailer
	lanAFrame := engine.EncodeRCT(event.frame, srcMAC, 1)
	lanBFrame := engine.EncodeRCT(event.frame, srcMAC, 2)

	if _, err := n.LanA.Write(lanAFrame); err != nil {
		log.Printf("prp: failed to write to LAN A: %v", err)
	}
	if _, err := n.LanB.Write(lanBFrame); err != nil {
		log.Printf("prp: failed to write to LAN B: %v", err)
	}

	if n.Config.Debug {
		log.Printf("prp: DAN frame duplicated to both LANs")
	}
}

// handleSupervisionFrame processes PRP supervision frames (0x88fb).
func (n *Node) handleSupervisionFrame(event frameEvent) {
	if n.Config.Debug {
		log.Printf("prp: supervision frame received on %s (%d bytes)", event.iface, event.frameSz)
	}

	// For now, just log. A full implementation would parse and process
	// the supervision payload for node discovery and network monitoring.
}

// SendSupervisionFrame sends a supervision frame on both LANs.
func (n *Node) SendSupervisionFrame() {
	supPayload := n.buildSupervisionFrame()

	// Send on LAN A
	frameA := make([]byte, len(supPayload))
	copy(frameA, supPayload)
	n.LanA.Write(frameA)

	// Send on LAN B
	frameB := make([]byte, len(supPayload))
	copy(frameB, supPayload)
	n.LanB.Write(frameB)

	if n.Config.Debug {
		log.Printf("prp: supervision frame sent on both LANs")
	}
}

// buildSupervisionFrame constructs a supervision frame payload.
func (n *Node) buildSupervisionFrame() []byte {
	// Supervision frame structure:
	// Destination MAC: 01-15-72-00-00-00 (PRP multicast)
	// Source MAC: our node MAC
	// EtherType: 0x88fb
	// Payload: PRP-ID, LAN-ID, Node State, Seq, etc.

	dstMAC := []byte{0x01, 0x15, 0x72, 0x00, 0x00, 0x00}
	srcMAC := n.nodeMAC()

	payload := make([]byte, 0, 256)
	payload = append(payload, dstMAC...)
	payload = append(payload, srcMAC...)
	etype := make([]byte, 2)
	binary.BigEndian.PutUint16(etype, 0x88fb)
	payload = append(payload, etype...)

	// Supervision payload
	// PRP-ID (1 byte)
	payload = append(payload, byte(n.Config.PRPID))
	// LAN-ID (1 byte) - 1 for A, 2 for B
	payload = append(payload, 0x01)
	// Node State (1 byte) - 0 = Active
	payload = append(payload, 0x00)
	// Padding
	payload = append(payload, 0x00, 0x00, 0x00)

	// Pad to minimum Ethernet frame size
	minFrameSize := 64
	if len(payload) < minFrameSize {
		payload = append(payload, make([]byte, minFrameSize-len(payload))...)
	}

	return payload
}

func (n *Node) nodeMAC() []byte {
	// Derive a MAC from the PRP ID
	mac := make([]byte, 6)
	mac[0] = 0x02
	mac[1] = 0x50
	mac[2] = 0x50
	mac[3] = byte(n.Config.PRPID >> 8)
	mac[4] = byte(n.Config.PRPID)
	mac[5] = 0x00
	return mac
}
