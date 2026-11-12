package ecoflow

import (
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"testing"
)

type fakeSource struct {
	desc    []byte
	reports map[uint8][]byte
}

func (f *fakeSource) ReportDescriptor() ([]byte, error) { return f.desc, nil }
func (f *fakeSource) Close() error                      { return nil }
func (f *fakeSource) GetFeatureReport(id uint8, length int) ([]byte, error) {
	r, ok := f.reports[id]
	if !ok {
		return nil, errNoReport(id)
	}
	if len(r) > length {
		r = r[:length]
	}
	return r, nil
}

type reportError uint8

func errNoReport(id uint8) error { return reportError(id) }
func (e reportError) Error() string {
	return "report " + strconv.Itoa(int(e)) + " not served"
}

func loadDescriptor(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../hiddesc/testdata/river3plus.rd.hex.txt")
	if err != nil {
		t.Fatal(err)
	}
	var out []byte
	for _, tok := range strings.Fields(string(raw)) {
		v, err := strconv.ParseUint(tok, 16, 8)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, byte(v))
	}
	return out
}

func TestHIDReadDecodesStatus(t *testing.T) {
	src := &fakeSource{
		desc: loadDescriptor(t),
		reports: map[uint8][]byte{
			12: {12, 76},         // RemainingCapacity = 76%
			13: {13, 0x2a, 0x00}, // RunTimeToEmpty = 42 min
			// PresentStatus bits: Charging(0)=1, Discharging(1)=0,
			// ACPresent(2)=1, BatteryPresent(3)=1, rest in byte 2 = 0.
			7: {7, 0b1101, 0},
			1: {1, 0x00, 0x04}, // ConfigActivePower = 1024
		},
	}
	r, err := NewHIDReader(src)
	if err != nil {
		t.Fatal(err)
	}
	st, err := r.Read()
	if err != nil {
		t.Fatal(err)
	}
	if st.ChargePercent == nil || *st.ChargePercent != 76 {
		t.Fatalf("charge = %v, want 76", st.ChargePercent)
	}
	if st.RuntimeMinutes == nil || *st.RuntimeMinutes != 42 {
		t.Fatalf("runtime = %v, want 42", st.RuntimeMinutes)
	}
	if st.RuntimeSentinel {
		t.Fatal("runtime sentinel set, want clear")
	}
	if st.ACPresent == nil || !*st.ACPresent {
		t.Fatalf("ac_present = %v, want true", st.ACPresent)
	}
	if st.Charging == nil || !*st.Charging {
		t.Fatalf("charging = %v, want true", st.Charging)
	}
	if st.Discharging == nil || *st.Discharging {
		t.Fatalf("discharging = %v, want false", st.Discharging)
	}
	if st.ConfigActivePower == nil || *st.ConfigActivePower != 1024 {
		t.Fatalf("config_active_power = %v, want 1024", st.ConfigActivePower)
	}
	if len(st.Warnings) == 0 {
		t.Fatal("expected warnings for unserved reports in the fake")
	}
}

func loadDeltaDescriptor(t *testing.T) []byte {
	t.Helper()
	rawHex, err := os.ReadFile("../hiddesc/testdata/delta3plus.rd.hex")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(strings.TrimSpace(string(rawHex)))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Values below mirror the real DELTA 3 Plus read on 2026-11-12:
// 80% charge, AC present, FullyCharged at 80% (charge limit),
// ConfigActivePower 1024, runtime-low 10 min from report 8.
func TestHIDReadDelta3Plus(t *testing.T) {
	src := &fakeSource{
		desc: loadDeltaDescriptor(t),
		reports: map[uint8][]byte{
			12: {12, 80},
			13: {13, 0x80, 0x0c}, // 3200 min
			// PresentStatus: ACPresent(2), BatteryPresent(3),
			// FullyCharged(8) set.
			7:  {7, 0b1100, 0b1},
			1:  {1, 0x00, 0x04}, // 1024
			8:  {8, 0x0a, 0x00}, // RemainingTimeLimit = 10 min
			17: {17, 30},        // RemainingCapacityLimit
			31: {31, 0xff},      // string report: must be skipped, no warning
		},
	}
	r, err := NewHIDReader(src)
	if err != nil {
		t.Fatal(err)
	}
	st, err := r.Read()
	if err != nil {
		t.Fatal(err)
	}
	if st.ChargePercent == nil || *st.ChargePercent != 80 {
		t.Fatalf("charge = %v, want 80", st.ChargePercent)
	}
	if st.RuntimeMinutes == nil || *st.RuntimeMinutes != 3200 {
		t.Fatalf("runtime = %v, want 3200", st.RuntimeMinutes)
	}
	if st.RemainingTimeLimitMinutes == nil || *st.RemainingTimeLimitMinutes != 10 {
		t.Fatalf("runtime low = %v, want 10", st.RemainingTimeLimitMinutes)
	}
	if st.FullyCharged == nil || !*st.FullyCharged {
		t.Fatalf("fully_charged = %v, want true", st.FullyCharged)
	}
	for _, w := range st.Warnings {
		if strings.Contains(w, "report 31") || strings.Contains(w, "report 32") {
			t.Fatalf("string reports should be skipped silently, got warning %q", w)
		}
	}
	for _, f := range st.Fields {
		if strings.HasSuffix(f.Path, "0x00000000") {
			t.Fatalf("padding field leaked into output: %+v", f)
		}
	}
}

func TestHIDReadRuntimeSentinel(t *testing.T) {
	src := &fakeSource{
		desc: loadDescriptor(t),
		reports: map[uint8][]byte{
			12: {12, 100},
			13: {13, 0x17, 0x33}, // 0x3317 LE = not discharging
		},
	}
	r, err := NewHIDReader(src)
	if err != nil {
		t.Fatal(err)
	}
	st, err := r.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !st.RuntimeSentinel || st.RuntimeMinutes != nil {
		t.Fatalf("sentinel=%v runtime=%v, want sentinel with nil runtime", st.RuntimeSentinel, st.RuntimeMinutes)
	}
}

// Golden CDC frame captured from a River 3 Plus by the r3pcomms
// project (sequence 0, so the XOR obfuscation is a no-op), stored
// verbatim in testdata — except for the CRC: the published capture
// has the serial number redacted in the debug output, which
// invalidates its CRC, so the fixture loader re-signs the frame.
// Segment decoding is still validated against the real capture.
func loadGoldenFrame(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/cdc_golden_frame.hex")
	if err != nil {
		t.Fatal(err)
	}
	frame, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	crc := CRC16ARC(frame[:len(frame)-2])
	frame[len(frame)-2] = byte(crc)
	frame[len(frame)-1] = byte(crc >> 8)
	return frame
}

func TestCDCParseGoldenFrame(t *testing.T) {
	frame := loadGoldenFrame(t)
	m, err := ParseCDCFrame(frame, 0)
	if err != nil {
		t.Fatal(err)
	}
	if m.DesignCapacityMAh == nil || *m.DesignCapacityMAh != 12800 {
		t.Fatalf("design capacity = %v, want 12800", m.DesignCapacityMAh)
	}
	if m.SystemTemperatureC == nil || *m.SystemTemperatureC != 26 {
		t.Fatalf("system temp = %v, want 26", m.SystemTemperatureC)
	}
	if m.BatteryTemperatureC == nil || *m.BatteryTemperatureC != 28 {
		t.Fatalf("battery temp = %v, want 28", m.BatteryTemperatureC)
	}
	approx := func(name string, got *float64, want float64) {
		t.Helper()
		if got == nil || *got < want-0.01 || *got > want+0.01 {
			t.Fatalf("%s = %v, want ~%v", name, got, want)
		}
	}
	approx("output_power", m.OutputPowerW, 258.77)
	approx("input_power", m.InputPowerW, 29.20)
	approx("solar_input", m.SolarInputPowerW, 29.20)
	approx("ac_output", m.ACOutputPowerW, 258.77)
	if m.RatedOutputPowerW == nil || *m.RatedOutputPowerW != 600 {
		t.Fatalf("rated output = %v, want 600", m.RatedOutputPowerW)
	}
	if m.ACOutputFrequencyHz == nil || *m.ACOutputFrequencyHz != 60 {
		t.Fatalf("ac output freq = %v, want 60", m.ACOutputFrequencyHz)
	}
	if m.ChargingRuntimeMinute != nil {
		t.Fatalf("charging runtime = %v, want nil (sentinel)", *m.ChargingRuntimeMinute)
	}
	if m.EMSVersion != "2.0.1.35" {
		t.Fatalf("ems version = %q, want 2.0.1.35", m.EMSVersion)
	}
}

func TestCDCFrameValidation(t *testing.T) {
	frame := loadGoldenFrame(t)
	// Wrong sequence must be rejected (frame carries sequence 0).
	if _, err := ParseCDCFrame(frame, 7); err == nil {
		t.Fatal("expected sequence mismatch error")
	}
	// Corrupting any byte must break the CRC.
	bad := append([]byte(nil), frame...)
	bad[40] ^= 0xff
	if _, err := ParseCDCFrame(bad, 0); err == nil {
		t.Fatal("expected CRC error")
	}
}

func TestBuildCDCRequest(t *testing.T) {
	req := BuildCDCRequest(0)
	if len(req) != 20 {
		t.Fatalf("request length = %d, want 20", len(req))
	}
	want := "aa030000de2d00000000ffff220201016602"
	got := hex.EncodeToString(req[:18])
	if got != want {
		t.Fatalf("request head = %s, want %s", got, want)
	}
	if CRC16ARC(req) != 0 {
		t.Fatal("request does not CRC to 0")
	}
	req2 := BuildCDCRequest(0x01020304)
	if req2[6] != 0x04 || req2[9] != 0x01 {
		t.Fatal("sequence not encoded little-endian at offset 6")
	}
}
