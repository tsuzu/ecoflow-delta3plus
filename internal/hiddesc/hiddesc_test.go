package hiddesc

import (
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"testing"
)

func loadFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/river3plus.rd.hex.txt")
	if err != nil {
		t.Fatal(err)
	}
	var out []byte
	for _, tok := range strings.Fields(string(raw)) {
		v, err := strconv.ParseUint(tok, 16, 8)
		if err != nil {
			t.Fatalf("bad hex token %q: %v", tok, err)
		}
		out = append(out, byte(v))
	}
	return out
}

func findField(d *Descriptor, kind Kind, reportID uint8, page uint16, usage uint16) *Field {
	for i, f := range d.Fields {
		if f.Kind == kind && f.ReportID == reportID && f.UsagePage == page && f.Usage == usage {
			return &d.Fields[i]
		}
	}
	return nil
}

func TestRepairCountsMissingEndCollections(t *testing.T) {
	raw := loadFixture(t)
	if len(raw) != 376 {
		t.Fatalf("fixture length = %d, want 376", len(raw))
	}
	_, added := Repair(raw)
	if added != 3 {
		t.Fatalf("Repair added %d End Collection bytes, want 3", added)
	}
	// An already balanced descriptor is untouched.
	balanced, _ := Repair(append(append([]byte(nil), raw...), 0xc0, 0xc0, 0xc0))
	if n := len(balanced); n != 379 {
		t.Fatalf("balanced descriptor length = %d, want 379", n)
	}
}

func TestParseEcoFlowDescriptor(t *testing.T) {
	d, err := Parse(loadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if d.RepairedCollections != 3 {
		t.Fatalf("repaired = %d, want 3", d.RepairedCollections)
	}

	// RemainingCapacity: Feature report 12, Battery System page (0x85)
	// usage 0x66, 8 bits at offset 0.
	f := findField(d, Feature, 12, 0x85, 0x66)
	if f == nil {
		t.Fatal("RemainingCapacity field not found")
	}
	if f.BitOffset != 0 || f.BitSize != 8 {
		t.Fatalf("RemainingCapacity offset/size = %d/%d, want 0/8", f.BitOffset, f.BitSize)
	}

	// RunTimeToEmpty: Feature report 13, usage 0x68, 16 bits.
	f = findField(d, Feature, 13, 0x85, 0x68)
	if f == nil {
		t.Fatal("RunTimeToEmpty field not found")
	}
	if f.BitSize != 16 {
		t.Fatalf("RunTimeToEmpty size = %d, want 16", f.BitSize)
	}

	// PresentStatus: Feature report 7, 1-bit flags. Charging is first,
	// ACPresent third (after Discharging).
	if f = findField(d, Feature, 7, 0x85, 0x44); f == nil || f.BitOffset != 0 || f.BitSize != 1 {
		t.Fatalf("Charging field = %+v, want offset 0 size 1", f)
	}
	if f = findField(d, Feature, 7, 0x85, 0xd0); f == nil || f.BitOffset != 2 {
		t.Fatalf("ACPresent field = %+v, want offset 2", f)
	}

	// ConfigActivePower: Feature report 1, Power Device page usage 0x44, 16 bits.
	if f = findField(d, Feature, 1, 0x84, 0x44); f == nil || f.BitSize != 16 {
		t.Fatalf("ConfigActivePower field = %+v, want size 16", f)
	}

	if got := d.ReportLength(12, Feature); got != 2 {
		t.Fatalf("report 12 length = %d, want 2 (ID + 1 byte)", got)
	}
	if got := d.ReportLength(7, Feature); got != 3 {
		t.Fatalf("report 7 length = %d, want 3 (ID + 12 bits)", got)
	}
}

func TestParseDelta3PlusDescriptor(t *testing.T) {
	// Real descriptor captured from a DELTA 3 Plus on 2026-11-12.
	// Unlike the River 3 Plus fixture, this unit's firmware ships a
	// well-formed descriptor: 401 bytes, no repair needed.
	rawHex, err := os.ReadFile("testdata/delta3plus.rd.hex")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(strings.TrimSpace(string(rawHex)))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 401 {
		t.Fatalf("descriptor length = %d, want 401", len(raw))
	}
	d, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if d.RepairedCollections != 0 {
		t.Fatalf("repaired = %d, want 0 (well-formed descriptor)", d.RepairedCollections)
	}
	// The layout differs from the River: RemainingTimeLimit is
	// report 8 and Voltage is report 11 here — the parser must
	// follow the descriptor, not fixed report IDs.
	if f := findField(d, Feature, 8, 0x85, 0x2a); f == nil || f.BitSize != 16 {
		t.Fatalf("RemainingTimeLimit (report 8) = %+v, want size 16", f)
	}
	if f := findField(d, Feature, 11, 0x84, 0x30); f == nil || f.BitSize != 16 {
		t.Fatalf("Voltage (report 11) = %+v, want size 16", f)
	}
	if f := findField(d, Feature, 12, 0x85, 0x66); f == nil || f.BitSize != 8 {
		t.Fatalf("RemainingCapacity (report 12) = %+v, want size 8", f)
	}
	// Delta PresentStatus adds CommunicationLost (12) and Overload (13).
	if f := findField(d, Feature, 7, 0x84, 0x65); f == nil || f.BitOffset != 13 {
		t.Fatalf("Overload field = %+v, want offset 13", f)
	}
}

func TestExtract(t *testing.T) {
	// RemainingCapacity-style 8-bit field.
	f := Field{BitOffset: 0, BitSize: 8}
	if v := Extract([]byte{76}, f); v != 76 {
		t.Fatalf("extract = %d, want 76", v)
	}
	// 16-bit LE field (runtime).
	f = Field{BitOffset: 0, BitSize: 16}
	if v := Extract([]byte{0x2a, 0x00}, f); v != 42 {
		t.Fatalf("extract = %d, want 42", v)
	}
	// Sentinel 0x3317 arrives as bytes 17 33.
	if v := Extract([]byte{0x17, 0x33}, f); v != 0x3317 {
		t.Fatalf("extract = %#x, want 0x3317", v)
	}
	// 1-bit flag at offset 2.
	f = Field{BitOffset: 2, BitSize: 1}
	if v := Extract([]byte{0b101}, f); v != 1 {
		t.Fatalf("extract = %d, want 1", v)
	}
	// Signed field: logical min < 0 sign-extends.
	f = Field{BitOffset: 0, BitSize: 16, LogicalMin: -32768}
	if v := Extract([]byte{0x00, 0x80}, f); v != -32768 {
		t.Fatalf("extract = %d, want -32768", v)
	}
}
