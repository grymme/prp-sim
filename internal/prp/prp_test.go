package prp

import (
	"testing"
	"time"

	"prp-gns3/internal/engine"
	"prp-gns3/internal/nodetable"
)

// TestRedBoxTrafficFlow simulates the complete RedBox traffic flow:
// 1. Frame arrives on interlink (from SAN) → gets RCT, duplicated to both LANs
// 2. Frame arrives on LAN A → dup check, strip RCT, forward to interlink
// 3. Frame arrives on LAN B → dup check (should find duplicate from step 2)
func TestRedBoxFullTrafficFlow(t *testing.T) {
	// Clean up any previous test state
	nodetable.Cleanup()

	// Create a test frame (Ethernet frame)
	srcMAC := "aa:bb:cc:dd:ee:ff"
	testFrame := make([]byte, 64)
	// Set source and destination MACs
	copy(testFrame[0:6], []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66}) // dst
	copy(testFrame[6:12], []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}) // src
	// Set EtherType (IPv4)
	testFrame[12] = 0x08
	testFrame[13] = 0x00

	// Step 1: Simulate frame arriving from interlink (SAN side)
	// In RedBox mode, this frame should get RCT added and be sent to both LANs
	encoded := engine.EncodeRCT(testFrame, srcMAC, 1)
	if len(encoded) != len(testFrame)+4 {
		t.Fatalf("RCT not added properly: %d vs %d", len(encoded), len(testFrame)+4)
	}

	// Verify the encoded frame can be decoded
	lanID, seq, payload, err := engine.DecodeRCT(encoded)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if lanID != 1 {
		t.Errorf("expected lanID=1, got %d", lanID)
	}
	if seq != 0 {
		t.Errorf("expected seq=0, got %d", seq)
	}
	if len(payload) != len(testFrame) {
		t.Errorf("payload length mismatch: %d vs %d", len(payload), len(testFrame))
	}

	// Step 2: Simulate the same frame arriving on LAN A from another node
	// The receiving node should detect the RCT and check for duplicates
	// For this test, we verify the frame is properly identified as PRP
	if !engine.IsPRPFrame(encoded) {
		t.Error("encoded frame should be detected as PRP frame")
	}

	// Step 3: Verify duplicate detection
	// Insert the (srcMAC, seq) pair into the node table
	nodetable.InsertWithExpiry(srcMAC, seq, lanID, 640)

	// Should be found (not expired)
	if !nodetable.Find(srcMAC, seq) {
		t.Error("expected to find recently inserted entry")
	}

	// Step 4: Verify expired entries are not found
	// Insert with very short expiry
	nodetable.InsertWithExpiry("aa:bb:cc:dd:ee:00", 999, 1, 10) // 10ms expiry
	time.Sleep(50 * time.Millisecond)

	if nodetable.Find("aa:bb:cc:dd:ee:00", 999) {
		t.Error("expected expired entry to not be found")
	}

	// Step 5: Verify cleanup removes expired entries
	removed := nodetable.Cleanup()
	if removed == 0 {
		t.Error("expected cleanup to remove at least the expired entry")
	}
}

// TestSequenceNumberPerMAC verifies that sequence numbers are tracked per source MAC.
func TestSequenceNumberPerMAC(t *testing.T) {
	mac1 := "00:11:22:33:44:55"
	mac2 := "66:77:88:99:aa:bb"

	engine.ResetSequence(mac1)
	engine.ResetSequence(mac2)

	frame1 := make([]byte, 60)
	frame2 := make([]byte, 60)

	// Interleave frames from two different source MACs
	enc1a := engine.EncodeRCT(frame1, mac1, 1) // mac1, seq=0
	enc2a := engine.EncodeRCT(frame2, mac2, 1) // mac2, seq=0
	enc1b := engine.EncodeRCT(frame1, mac1, 1) // mac1, seq=1
	enc2b := engine.EncodeRCT(frame2, mac2, 1) // mac2, seq=1

	_, seq1a, _, _ := engine.DecodeRCT(enc1a)
	_, seq2a, _, _ := engine.DecodeRCT(enc2a)
	_, seq1b, _, _ := engine.DecodeRCT(enc1b)
	_, seq2b, _, _ := engine.DecodeRCT(enc2b)

	if seq1a != 0 || seq1b != 1 {
		t.Errorf("mac1: expected seq 0,1 got %d,%d", seq1a, seq1b)
	}
	if seq2a != 0 || seq2b != 1 {
		t.Errorf("mac2: expected seq 0,1 got %d,%d", seq2a, seq2b)
	}

	engine.ResetSequence(mac1)
	engine.ResetSequence(mac2)
}

// TestFrameDetection verifies frame classification works correctly.
func TestFrameDetection(t *testing.T) {
	// Supervision frame (0x88fb)
	supFrame := make([]byte, 64)
	supFrame[12] = 0x88
	supFrame[13] = 0xfb
	if !engine.IsSupervisionFrame(supFrame) {
		t.Error("supervision frame should be detected")
	}

	// Regular frame
	regFrame := make([]byte, 64)
	regFrame[12] = 0x08
	regFrame[13] = 0x00
	if engine.IsSupervisionFrame(regFrame) {
		t.Error("regular frame should not be detected as supervision")
	}

	// PRP frame (has RCT)
	prpFrame := make([]byte, 60)
	prpFrame = engine.EncodeRCT(prpFrame, "test-mac", 1)
	if !engine.IsPRPFrame(prpFrame) {
		t.Error("frame with RCT should be detected as PRP")
	}

	// Non-PRP frame (no RCT)
	nonPrpFrame := make([]byte, 60)
	if engine.IsPRPFrame(nonPrpFrame) {
		t.Error("frame without RCT should not be detected as PRP")
	}
}

// TestMACAddressParsing verifies source MAC extraction.
func TestMACAddressParsing(t *testing.T) {
	frame := []byte{
		0x00, 0x11, 0x22, 0x33, 0x44, 0x55, // dst
		0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, // src
		0x08, 0x00, // EtherType
	}

	dstMAC := engine.GetDstMAC(frame)
	srcMAC := engine.GetSrcMAC(frame)

	if dstMAC != "00:11:22:33:44:55" {
		t.Errorf("expected dst MAC 00:11:22:33:44:55, got %s", dstMAC)
	}
	if srcMAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("expected src MAC aa:bb:cc:dd:ee:ff, got %s", srcMAC)
	}
}
