package main

import (
	"strings"
	"testing"
	"time"

	"github.com/tsuzu/ecoflow-delta3plus/internal/ecoflow"
)

func intp(v int) *int    { return &v }
func boolp(v bool) *bool { return &v }

func TestRenderFull(t *testing.T) {
	st := &ecoflow.HIDStatus{
		ChargePercent:     intp(80),
		RuntimeMinutes:    intp(3200),
		ACPresent:         boolp(true),
		Charging:          boolp(false),
		FullyCharged:      boolp(true),
		ConfigActivePower: intp(1024),
		Fields: []ecoflow.DecodedField{
			{Path: "UPS.PowerSummary.Voltage", ReportID: 11, Raw: 1300},
			{Path: "UPS.PowerSummary.ConfigVoltage", ReportID: 10, Raw: 1380},
		},
	}
	out := render(st, time.Unix(1762920000, 0))
	for _, want := range []string{
		"ecoflow_up 1",
		"ecoflow_battery_charge_percent 80",
		"ecoflow_battery_runtime_minutes 3200",
		"ecoflow_ac_present 1",
		"ecoflow_charging 0",
		"ecoflow_fully_charged 1",
		"ecoflow_config_active_power 1024",
		"ecoflow_voltage_raw 1300",
		"ecoflow_config_voltage_raw 1380",
	} {
		if !strings.Contains(out, want+"\n") {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
}

func TestRenderSentinelOmitsRuntime(t *testing.T) {
	st := &ecoflow.HIDStatus{
		ChargePercent:   intp(100),
		RuntimeSentinel: true,
	}
	out := render(st, time.Now())
	if strings.Contains(out, "ecoflow_battery_runtime_minutes") {
		t.Fatalf("runtime metric must be absent on the sentinel:\n%s", out)
	}
}

func TestEffectiveStatusHidesStaleReadings(t *testing.T) {
	st := &ecoflow.HIDStatus{ChargePercent: intp(80)}
	if got := effectiveStatus(st, "usb: device gone"); got != nil {
		t.Fatalf("failing poll must hide stale readings, got %+v", got)
	}
	if got := effectiveStatus(st, ""); got != st {
		t.Fatal("healthy poll must expose readings")
	}
	out := render(effectiveStatus(st, "usb: device gone"), time.Now())
	if !strings.Contains(out, "ecoflow_up 0\n") || strings.Contains(out, "ecoflow_battery_charge_percent") {
		t.Fatalf("down render must be up=0 without charge:\n%s", out)
	}
}

func TestRenderDown(t *testing.T) {
	out := render(nil, time.Time{})
	if !strings.Contains(out, "ecoflow_up 0\n") {
		t.Fatalf("nil status must render ecoflow_up 0:\n%s", out)
	}
	if strings.Contains(out, "ecoflow_battery_charge_percent") {
		t.Fatalf("nil status must not invent charge data:\n%s", out)
	}
}
