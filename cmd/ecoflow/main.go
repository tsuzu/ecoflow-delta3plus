// Command ecoflow reads EcoFlow DELTA 3 Plus (and River 3 Plus)
// status in userland on Linux — no kernel HID driver, no NUT.
//
// HID is read over libusb (GET_REPORT / GET_DESCRIPTOR on the control
// endpoint, interface claimed directly), because the unit's HID
// report descriptor is malformed and the kernel hid-generic driver
// rejects the device. CDC-ACM telemetry (watts, temperatures) is
// read over the composite device's /dev/ttyACM* interface.
package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tsuzu/ecoflow-delta3plus/internal/ecoflow"
	"github.com/tsuzu/ecoflow-delta3plus/internal/hiddesc"
)

type combined struct {
	HID      *ecoflow.HIDStatus  `json:"hid,omitempty"`
	CDC      *ecoflow.CDCMetrics `json:"cdc,omitempty"`
	CDCError string              `json:"cdc_error,omitempty"`
}

func main() {
	os.Exit(run())
}

func run() int {
	var (
		source     = flag.String("source", "all", "what to read: hid, cdc, or all")
		cdcPort    = flag.String("cdc-port", "", "CDC-ACM port (default: auto-detect /dev/serial/by-id or /dev/ttyACM*)")
		jsonOut    = flag.Bool("json", false, "print JSON instead of human-readable text")
		every      = flag.Duration("every", 0, "poll repeatedly with this interval (0 = once)")
		count      = flag.Int("count", 0, "number of polls when -every is set (0 = forever)")
		dump       = flag.Bool("dump", false, "HID: dump all decoded feature fields and raw reports")
		descriptor = flag.Bool("descriptor", false, "HID: print the raw report descriptor and parsed fields, then exit")
		promFile   = flag.String("prom-file", "", "also write Prometheus textfile metrics to PATH (atomic)")
		redact     = flag.Bool("redact-serial", false, "redact the serial number from CDC output")
	)
	flag.Parse()

	if *descriptor || *dump {
		return runHIDExplore(*descriptor)
	}

	wantHID := *source == "hid" || *source == "all"
	wantCDC := *source == "cdc" || *source == "all"
	if !wantHID && !wantCDC {
		fmt.Fprintf(os.Stderr, "error: -source must be hid, cdc, or all\n")
		return 2
	}

	var hidReader *ecoflow.HIDReader
	var usbDev *ecoflow.USBDevice
	if wantHID {
		var err error
		usbDev, err = ecoflow.OpenUSB()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 2
		}
		defer usbDev.Close()
		hidReader, err = ecoflow.NewHIDReader(usbDev)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	}

	var cdcReader *ecoflow.CDCReader
	var serPort *ecoflow.SerialPort
	var cdcErrMsg string
	cdcFatal := *source == "cdc"
	if wantCDC {
		port := *cdcPort
		var err error
		if port == "" {
			port, err = ecoflow.FindCDCPort()
		}
		if err == nil {
			serPort, err = ecoflow.OpenSerialPort(port)
			if err != nil {
				err = fmt.Errorf("open CDC port %s: %w", port, err)
			}
		}
		if err != nil {
			if cdcFatal {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return 2
			}
			// Tolerated in "all" mode: CDC presence on the DELTA 3
			// Plus is not yet confirmed on real hardware.
			cdcReader = nil
			cdcErrMsg = err.Error()
		} else {
			defer serPort.Close()
			cdcReader = ecoflow.NewCDCReader(serPort)
		}
	}

	polls := 1
	if *every > 0 {
		if *count > 0 {
			polls = *count
		} else {
			polls = -1 // forever
		}
	}
	for n := 0; polls < 0 || n < polls; n++ {
		if n > 0 {
			time.Sleep(*every)
		}
		out, err := readOnce(hidReader, cdcReader, cdcErrMsg, cdcFatal, *redact)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if *promFile != "" {
			if err := writeProm(*promFile, out); err != nil {
				fmt.Fprintf(os.Stderr, "error: write prom file: %v\n", err)
				return 1
			}
		}
		if *jsonOut {
			enc, _ := json.Marshal(out)
			fmt.Println(string(enc))
		} else {
			printHuman(out)
		}
	}
	return 0
}

func readOnce(hidReader *ecoflow.HIDReader, cdcReader *ecoflow.CDCReader, cdcErrMsg string, cdcFatal bool, redact bool) (*combined, error) {
	out := &combined{CDCError: cdcErrMsg}
	if hidReader != nil {
		st, err := hidReader.Read()
		if err != nil {
			return nil, err
		}
		out.HID = st
	}
	if cdcReader != nil {
		m, err := cdcReader.Read()
		if err != nil {
			if cdcFatal {
				return nil, err
			}
			out.CDCError = err.Error()
		} else {
			out.CDC = m
			if redact && m.SerialNumber != "" {
				m.SerialNumber = "REDACTED"
			}
		}
	}
	return out, nil
}

func printHuman(out *combined) {
	var parts []string
	if h := out.HID; h != nil {
		s := "DELTA 3 Plus:"
		if h.ChargePercent != nil {
			s += fmt.Sprintf(" %d%%", *h.ChargePercent)
		}
		if h.RuntimeMinutes != nil {
			s += fmt.Sprintf("  runtime %d min", *h.RuntimeMinutes)
		} else if h.RuntimeSentinel {
			s += "  runtime — (not discharging)"
		}
		if h.ACPresent != nil {
			s += fmt.Sprintf("  AC:%s", present(*h.ACPresent))
		}
		if h.Charging != nil && *h.Charging {
			s += "  charging"
		}
		if h.Discharging != nil && *h.Discharging {
			s += "  discharging"
		}
		parts = append(parts, s)
	}
	if c := out.CDC; c != nil {
		s := "CDC:"
		if c.InputPowerW != nil {
			s += fmt.Sprintf(" in %.1fW", *c.InputPowerW)
		}
		if c.OutputPowerW != nil {
			s += fmt.Sprintf(" out %.1fW", *c.OutputPowerW)
		}
		if c.ACInputPowerW != nil {
			s += fmt.Sprintf(" (AC in %.1fW", *c.ACInputPowerW)
		}
		if c.SolarInputPowerW != nil {
			s += fmt.Sprintf(", solar %.1fW", *c.SolarInputPowerW)
		}
		if c.ACInputPowerW != nil || c.SolarInputPowerW != nil {
			s += ")"
		}
		if c.SystemTemperatureC != nil && c.BatteryTemperatureC != nil {
			s += fmt.Sprintf("  temps sys %.0f°C batt %.0f°C", *c.SystemTemperatureC, *c.BatteryTemperatureC)
		}
		parts = append(parts, s)
	}
	if out.CDCError != "" {
		parts = append(parts, "CDC unavailable: "+out.CDCError)
	}
	fmt.Println(strings.Join(parts, "  |  "))
}

func present(b bool) string {
	if b {
		return "present"
	}
	return "absent"
}

func runHIDExplore(descriptorOnly bool) int {
	dev, err := ecoflow.OpenUSB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	defer dev.Close()

	raw, err := dev.ReportDescriptor()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: get descriptor: %v\n", err)
		return 1
	}
	desc, err := hiddesc.Parse(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: parse descriptor: %v\n", err)
		return 1
	}
	fmt.Printf("report descriptor: %d bytes, %d missing End Collection byte(s) repaired, %d fields\n",
		len(raw), desc.RepairedCollections, len(desc.Fields))
	if descriptorOnly {
		fmt.Printf("hex: %s\n", hex.EncodeToString(raw))
		for _, f := range desc.Fields {
			fmt.Printf("  report %2d %-7s offset %3d size %2d  %s\n",
				f.ReportID, f.Kind, f.BitOffset, f.BitSize,
				ecoflow.UsagePath(f.Collection, f.UsageCode()))
		}
		return 0
	}
	reader, err := ecoflow.NewHIDReader(dev)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	st, err := reader.Read()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	for _, f := range st.Fields {
		fmt.Printf("report %2d  %-50s %d\n", f.ReportID, f.Path, f.Raw)
	}
	for _, w := range st.Warnings {
		fmt.Printf("warning: %s\n", w)
	}
	return 0
}

func writeProm(path string, out *combined) error {
	var lines []string
	add := func(name, help string, v float64) {
		lines = append(lines,
			"# HELP "+name+" "+help,
			"# TYPE "+name+" gauge",
			fmt.Sprintf("%s %v", name, v))
	}
	if h := out.HID; h != nil {
		if h.ChargePercent != nil {
			add("ecoflow_delta3plus_battery_charge_percent", "Battery charge level, percent", float64(*h.ChargePercent))
		}
		if h.RuntimeMinutes != nil {
			add("ecoflow_delta3plus_battery_runtime_minutes", "Estimated runtime remaining, minutes", float64(*h.RuntimeMinutes))
		}
		if h.ACPresent != nil {
			v := 0.0
			if *h.ACPresent {
				v = 1
			}
			add("ecoflow_delta3plus_ac_present", "AC input present (1) or not (0)", v)
		}
	}
	if c := out.CDC; c != nil {
		if c.InputPowerW != nil {
			add("ecoflow_delta3plus_input_power_watts", "Total input power, watts", *c.InputPowerW)
		}
		if c.OutputPowerW != nil {
			add("ecoflow_delta3plus_output_power_watts", "Total output power, watts", *c.OutputPowerW)
		}
		if c.BatteryTemperatureC != nil {
			add("ecoflow_delta3plus_battery_temperature_celsius", "Battery temperature, celsius", *c.BatteryTemperatureC)
		}
	}
	text := strings.Join(lines, "\n") + "\n"
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ecoflow-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
