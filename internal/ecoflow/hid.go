package ecoflow

import (
	"fmt"
	"sort"

	"github.com/tsuzu/ecoflow-delta3plus/internal/hiddesc"
)

const (
	VendorID  = 0x3746
	ProductID = 0xffff

	// RuntimeSentinel is returned in RunTimeToEmpty when the unit is not
	// discharging (charging or idle): there is no meaningful runtime.
	RuntimeSentinel = 0x3317
)

// ReportSource is the userland USB transport used to talk HID to the
// unit. The implementation in usb.go uses libusb via gousb and claims
// the interface directly, bypassing the kernel HID parser (which
// rejects this device's malformed report descriptor).
type ReportSource interface {
	// ReportDescriptor returns the raw HID report descriptor.
	ReportDescriptor() ([]byte, error)
	// GetFeatureReport issues GET_REPORT (Feature) for one report ID.
	// The returned buffer starts with the report ID byte.
	GetFeatureReport(reportID uint8, length int) ([]byte, error)
	Close() error
}

// DecodedField is one descriptor field with its decoded value.
type DecodedField struct {
	Path     string `json:"path"`
	ReportID uint8  `json:"report_id"`
	Raw      int64  `json:"raw"`
}

// HIDStatus is the decoded UPS status from the HID interface.
//
// Mappings follow NUT's ecoflow-hid subdriver:
//   - RemainingCapacity (0x85/0x66) is the charge level in percent.
//   - RunTimeToEmpty (0x85/0x68) is in *minutes* on the wire (despite
//     the descriptor claiming seconds); 0x3317 is the not-discharging
//     sentinel.
//   - The PresentStatus booleans are 1-bit Battery System usages in a
//     single Feature report.
//   - ConfigActivePower (0x84/0x44) is the rated capacity/power figure
//     (1024 on a DELTA 3 Plus).
type HIDStatus struct {
	ChargePercent               *int  `json:"battery_charge_percent,omitempty"`
	RuntimeMinutes              *int  `json:"battery_runtime_minutes,omitempty"`
	RuntimeSentinel             bool  `json:"battery_runtime_sentinel,omitempty"`
	ACPresent                   *bool `json:"ac_present,omitempty"`
	Charging                    *bool `json:"charging,omitempty"`
	Discharging                 *bool `json:"discharging,omitempty"`
	FullyCharged                *bool `json:"fully_charged,omitempty"`
	FullyDischarged             *bool `json:"fully_discharged,omitempty"`
	BatteryPresent              *bool `json:"battery_present,omitempty"`
	BelowRemainingCapacityLimit *bool `json:"below_remaining_capacity_limit,omitempty"`
	NeedReplacement             *bool `json:"need_replacement,omitempty"`
	RemainingCapacityLimit      *int  `json:"battery_charge_low_percent,omitempty"`
	WarningCapacityLimit        *int  `json:"battery_charge_warning_percent,omitempty"`
	RemainingTimeLimitMinutes   *int  `json:"battery_runtime_low_minutes,omitempty"`
	FullChargeCapacityPercent   *int  `json:"full_charge_capacity_percent,omitempty"`
	DesignCapacityPercent       *int  `json:"design_capacity_percent,omitempty"`
	ConfigActivePower           *int  `json:"config_active_power,omitempty"`

	// Fields holds every decoded Feature field, for --dump style use
	// and for mapping fields whose scaling is not yet confirmed on
	// real DELTA 3 Plus hardware (Voltage, ConfigVoltage, ...).
	Fields []DecodedField `json:"fields,omitempty"`
	// Warnings lists report IDs that could not be read.
	Warnings []string `json:"warnings,omitempty"`
}

// HIDReader reads and decodes HID status through a ReportSource.
type HIDReader struct {
	src  ReportSource
	desc *hiddesc.Descriptor
}

// NewHIDReader fetches and parses the report descriptor from the unit.
func NewHIDReader(src ReportSource) (*HIDReader, error) {
	raw, err := src.ReportDescriptor()
	if err != nil {
		return nil, fmt.Errorf("get report descriptor: %w", err)
	}
	desc, err := hiddesc.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse report descriptor (%d bytes): %w", len(raw), err)
	}
	return &HIDReader{src: src, desc: desc}, nil
}

// Descriptor returns the parsed descriptor (after in-memory repair).
func (r *HIDReader) Descriptor() *hiddesc.Descriptor { return r.desc }

// Read fetches every Feature report named by the descriptor and decodes
// it. Individual unreadable reports are tolerated (recorded as
// warnings), matching how some report IDs are not served by some
// firmwares; a missing RemainingCapacity is fatal.
func (r *HIDReader) Read() (*HIDStatus, error) {
	st := &HIDStatus{}
	byPath := map[string]int64{}

	for _, id := range r.desc.FeatureReportIDs() {
		fields := r.desc.FeatureFields(id)
		// Reports whose fields are all string-index usages
		// (iDeviceChemistry, iOEMInformation) carry no telemetry;
		// the firmware answers them with variable-length string
		// data that overflows the descriptor-sized buffer.
		telemetry := false
		for _, f := range fields {
			if f.UsageCode() != 0x00850089 && f.UsageCode() != 0x0085008f {
				telemetry = true
			}
		}
		if !telemetry {
			continue
		}
		length := r.desc.ReportLength(id, hiddesc.Feature)
		if length <= 0 {
			continue
		}
		raw, err := r.src.GetFeatureReport(id, length)
		if err != nil {
			st.Warnings = append(st.Warnings, fmt.Sprintf("report %d: %v", id, err))
			continue
		}
		payload := raw
		if len(payload) > 0 && payload[0] == id {
			payload = payload[1:]
		}
		for _, f := range fields {
			if f.UsageCode() == 0 {
				continue // descriptor padding, no usage assigned
			}
			// Note: EcoFlow marks most Feature items "Cnst" in the
			// descriptor, yet they carry live values — decode them all.
			v := hiddesc.Extract(payload, f)
			path := UsagePath(f.Collection, f.UsageCode())
			st.Fields = append(st.Fields, DecodedField{Path: path, ReportID: id, Raw: v})
			byPath[path] = v
		}
	}

	get := func(suffix string) (int64, bool) {
		// Match on the final usage name regardless of collection path,
		// so firmware descriptor reshuffles do not break decoding.
		for path, v := range byPath {
			if lastName(path) == suffix {
				return v, true
			}
		}
		return 0, false
	}
	getInt := func(suffix string) *int {
		if v, ok := get(suffix); ok {
			n := int(v)
			return &n
		}
		return nil
	}
	getBool := func(suffix string) *bool {
		if v, ok := get(suffix); ok {
			b := v != 0
			return &b
		}
		return nil
	}

	st.ChargePercent = getInt("RemainingCapacity")
	if v, ok := get("RunTimeToEmpty"); ok {
		if v == RuntimeSentinel {
			st.RuntimeSentinel = true
		} else {
			n := int(v)
			st.RuntimeMinutes = &n
		}
	}
	st.ACPresent = getBool("ACPresent")
	st.Charging = getBool("Charging")
	st.Discharging = getBool("Discharging")
	st.FullyCharged = getBool("FullyCharged")
	st.FullyDischarged = getBool("FullyDischarged")
	st.BatteryPresent = getBool("BatteryPresent")
	st.BelowRemainingCapacityLimit = getBool("BelowRemainingCapacityLimit")
	st.NeedReplacement = getBool("NeedReplacement")
	st.RemainingCapacityLimit = getInt("RemainingCapacityLimit")
	st.WarningCapacityLimit = getInt("WarningCapacityLimit")
	st.RemainingTimeLimitMinutes = getInt("RemainingTimeLimit")
	st.FullChargeCapacityPercent = getInt("FullChargeCapacity")
	st.DesignCapacityPercent = getInt("DesignCapacity")
	st.ConfigActivePower = getInt("ConfigActivePower")

	sort.Slice(st.Fields, func(i, j int) bool {
		if st.Fields[i].ReportID != st.Fields[j].ReportID {
			return st.Fields[i].ReportID < st.Fields[j].ReportID
		}
		return st.Fields[i].Path < st.Fields[j].Path
	})

	if st.ChargePercent == nil {
		return st, fmt.Errorf("RemainingCapacity not found in any feature report (warnings: %v)", st.Warnings)
	}
	return st, nil
}

func lastName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			return path[i+1:]
		}
	}
	return path
}
