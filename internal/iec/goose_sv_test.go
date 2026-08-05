package iec

import (
	"testing"
	"time"
)

func TestGOOSEStructure(t *testing.T) {
	apdu := BuildGOOSEAPDU(1, 1, time.Now(), false, 1, "gcb", "dataset", "goId")
	// Must start with 0x61 (application, constructed, PDU 1)
	if len(apdu) == 0 || apdu[0] != 0x61 {
		t.Errorf("APDU tag %02x, want 61", apdu[0])
	}
	stNum, sqNum, ts, data, ok := ParseGOOSEAPDU(apdu)
	if !ok {
		t.Errorf("ParseGOOSEAPDU failed; apdu len=%d", len(apdu))
	}
	if stNum != 1 {
		t.Errorf("stNum=%d want 1", stNum)
	}
	if sqNum != 1 {
		t.Errorf("sqNum=%d want 1", sqNum)
	}
	if ts.IsZero() {
		t.Errorf("t is zero")
	}
	if data != false {
		t.Errorf("allData=%v should be false", data)
	}
}

func TestGOOSELengthAtLeast8(t *testing.T) {
	apdu := BuildGOOSEAPDU(1, 1, time.Now(), false, 1, "g", "d", "i")
	if len(apdu) < 2 {
		t.Fatal("APDU too short")
	}
	lengthByte := apdu[1]
	var length int
	if lengthByte&0x80 == 0 {
		length = int(lengthByte)
	} else {
		numBytes := int(lengthByte & 0x7f)
		if len(apdu) < 2+numBytes {
			t.Fatal("APDU too short for long length")
		}
		length = 0
		for i := 0; i < numBytes; i++ {
			length = (length << 8) | int(apdu[2+i])
		}
	}
	if length < 8 {
		t.Errorf("Length=%d, want >=8 (Wireshark minimum for GOOSE)", length)
	}
}

func TestSVE2E(t *testing.T) {
	apdu := BuildSVAPDU(100, 1, "svID")
	if len(apdu) == 0 || apdu[0] != 0x60 {
		t.Errorf("SV APDU tag %02x, want 60", apdu[0])
	}
	smpCnt, ok := ParseSVAPDU(apdu)
	if !ok {
		t.Errorf("ParseSVAPDU failed")
	}
	if smpCnt != 100 {
		t.Errorf("smpCnt=%d want 100", smpCnt)
	}
}
