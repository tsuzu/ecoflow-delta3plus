package efble

import (
	"math"

	"google.golang.org/protobuf/encoding/protowire"
)

// DisplayPropertyUpload field numbers (proto/pd335_sys.proto in
// ha-ef-ble), restricted to the fields the exporter exposes.
const (
	fPowInSumW    = 3
	fPowOutSumW   = 4
	fPowGetQcusb1 = 9
	fPowGetQcusb2 = 10
	fPowGetTypec1 = 11
	fPowGetTypec2 = 12
	fPowGetAcIn   = 54
	fPowGetPv2    = 70
	fPowGetBms    = 158
	fPlugInAcFlag = 202
	fMaxCellTemp  = 259
	fCmsBattSoc   = 262
	fCmsDsgRemMin = 268
	fPowGetPv     = 361
	fPlugInPvType = 363
	fPowGetAcOut  = 368
	fPlugInPv2Typ = 422
)

const dcPortSolar = 2

// rawState accumulates DisplayPropertyUpload fields across packets:
// the device sends partial updates, and the reference implementation
// merges them into persistent device attributes.
type rawState struct {
	floats map[protowire.Number]float64
	ints   map[protowire.Number]int64
	seen   map[protowire.Number]bool
}

func newRawState() *rawState {
	return &rawState{
		floats: map[protowire.Number]float64{},
		ints:   map[protowire.Number]int64{},
		seen:   map[protowire.Number]bool{},
	}
}

// merge decodes one DisplayPropertyUpload protobuf message into the
// accumulated state. Unknown fields are skipped.
func (s *rawState) merge(payload []byte) {
	b := payload
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return
		}
		b = b[n:]
		switch typ {
		case protowire.Fixed32Type:
			v, n := protowire.ConsumeFixed32(b)
			if n < 0 {
				return
			}
			s.floats[num] = float64(math.Float32frombits(v))
			s.seen[num] = true
			b = b[n:]
		case protowire.VarintType:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return
			}
			s.ints[num] = int64(v)
			s.seen[num] = true
			b = b[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return
			}
			b = b[n:]
		}
	}
}

// Telemetry is one exporter snapshot, keyed like the Python
// exporter's attribute names.
type Telemetry struct {
	Values map[string]float64
	Bools  map[string]bool
}

// snapshot applies the reference implementation's field transforms
// (props/transforms.py and devices/_delta3_base.py) to the merged
// state. ok is false until the total input power has been seen.
func (s *rawState) snapshot() (Telemetry, bool) {
	if !s.seen[fPowInSumW] {
		return Telemetry{}, false
	}
	t := Telemetry{Values: map[string]float64{}, Bools: map[string]bool{}}
	f := func(num protowire.Number) (float64, bool) {
		if s.seen[num] {
			return s.floats[num], true
		}
		return 0, false
	}
	set := func(name string, num protowire.Number, tr func(float64) float64) {
		if v, ok := f(num); ok {
			if tr != nil {
				v = tr(v)
			}
			t.Values[name] = v
		}
	}
	set("battery_level", fCmsBattSoc, pround2)
	set("input_power", fPowInSumW, nil)
	set("output_power", fPowOutSumW, nil)
	set("ac_input_power", fPowGetAcIn, nil)
	set("ac_output_power", fPowGetAcOut, outPower)
	set("dc_port_input_power", fPowGetPv, pround2)
	set("dc_port_2_input_power", fPowGetPv2, pround2)
	set("usbc_output_power", fPowGetTypec1, outPower)
	set("usbc2_output_power", fPowGetTypec2, outPower)
	set("usba_output_power", fPowGetQcusb1, outPower)
	set("usba2_output_power", fPowGetQcusb2, outPower)
	if v, ok := f(fPowGetBms); ok {
		t.Values["battery_input_power"] = math.Max(0, v)
		t.Values["battery_output_power"] = -math.Min(0, v)
	}
	// Solar input is a computed field: the DC port power counts as
	// solar only when the port reports itself as a solar input;
	// the computed value is always present (0 otherwise).
	solar := func(powerNum, typeNum protowire.Number) float64 {
		if s.seen[typeNum] && s.ints[typeNum] == dcPortSolar {
			if v, ok := f(powerNum); ok {
				return round2(v)
			}
		}
		return 0
	}
	t.Values["solar_input_power"] = solar(fPowGetPv, fPlugInPvType)
	t.Values["solar_input_power_2"] = solar(fPowGetPv2, fPlugInPv2Typ)
	if s.seen[fMaxCellTemp] {
		t.Values["cell_temperature"] = float64(s.ints[fMaxCellTemp])
	}
	if s.seen[fCmsDsgRemMin] {
		t.Values["remaining_time_discharging"] = float64(s.ints[fCmsDsgRemMin])
	}
	if s.seen[fPlugInAcFlag] {
		t.Bools["plugged_in_ac"] = s.ints[fPlugInAcFlag] != 0
	}
	return t, true
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }

func pround2(x float64) float64 { return round2(x) }

// outPower negates and rounds output power (the device reports
// outputs as negative values), returning 0 for zero.
func outPower(x float64) float64 {
	if x == 0 {
		return 0
	}
	return -round2(x)
}
