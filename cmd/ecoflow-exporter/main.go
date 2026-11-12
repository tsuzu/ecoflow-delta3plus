// Command ecoflow-exporter polls an EcoFlow DELTA 3 Plus over USB
// HID (userland, via libusb) and exposes the readings as Prometheus
// metrics, for scraping into Thanos/Grafana like the other home
// exporters (hems-exporter, node_exporter on the same host).
//
// The device is polled in the background and the latest reading is
// served from cache, so scrapes never block on USB. If the unit is
// unplugged the exporter keeps serving with ecoflow_up=0.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tsuzu/ecoflow-delta3plus/internal/ecoflow"
)

type state struct {
	mu          sync.Mutex
	status      *ecoflow.HIDStatus
	lastSuccess time.Time
	lastErr     string
}

func (s *state) setOK(st *ecoflow.HIDStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = st
	s.lastSuccess = time.Now()
	s.lastErr = ""
}

func (s *state) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErr = err.Error()
}

func (s *state) snapshot() (*ecoflow.HIDStatus, time.Time, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status, s.lastSuccess, s.lastErr
}

// render produces the Prometheus text exposition for one status.
// A nil status renders only ecoflow_up=0.
func render(st *ecoflow.HIDStatus, lastSuccess time.Time) string {
	var b strings.Builder
	gauge := func(name, help string, v float64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %v\n", name, help, name, name, v)
	}
	boolGauge := func(name, help string, p *bool) {
		if p == nil {
			return
		}
		v := 0.0
		if *p {
			v = 1
		}
		gauge(name, help, v)
	}
	intGauge := func(name, help string, p *int) {
		if p == nil {
			return
		}
		gauge(name, help, float64(*p))
	}

	if st == nil {
		gauge("ecoflow_up", "Whether the last USB poll succeeded (1) or not (0)", 0)
		return b.String()
	}
	gauge("ecoflow_up", "Whether the last USB poll succeeded (1) or not (0)", 1)
	if !lastSuccess.IsZero() {
		gauge("ecoflow_last_success_unixtime_seconds", "Unix time of the last successful USB poll", float64(lastSuccess.Unix()))
	}
	intGauge("ecoflow_battery_charge_percent", "Battery charge level, percent", st.ChargePercent)
	if st.RuntimeMinutes != nil {
		intGauge("ecoflow_battery_runtime_minutes", "Estimated runtime remaining, minutes (absent when not discharging)", st.RuntimeMinutes)
	}
	boolGauge("ecoflow_ac_present", "AC input present", st.ACPresent)
	boolGauge("ecoflow_charging", "Battery is charging", st.Charging)
	boolGauge("ecoflow_discharging", "Battery is discharging", st.Discharging)
	boolGauge("ecoflow_fully_charged", "Battery fully charged (at the configured charge limit)", st.FullyCharged)
	boolGauge("ecoflow_fully_discharged", "Battery fully discharged (at the configured discharge limit)", st.FullyDischarged)
	boolGauge("ecoflow_battery_present", "Battery present", st.BatteryPresent)
	boolGauge("ecoflow_below_remaining_capacity_limit", "Charge below the remaining-capacity limit", st.BelowRemainingCapacityLimit)
	boolGauge("ecoflow_need_replacement", "Battery needs replacement", st.NeedReplacement)
	intGauge("ecoflow_charge_low_percent", "Configured low-charge threshold (discharge limit), percent", st.RemainingCapacityLimit)
	intGauge("ecoflow_charge_warning_percent", "Configured charge warning threshold, percent", st.WarningCapacityLimit)
	intGauge("ecoflow_runtime_low_minutes", "Configured low-runtime threshold, minutes", st.RemainingTimeLimitMinutes)
	intGauge("ecoflow_full_charge_capacity_percent", "Full charge capacity relative to design, percent", st.FullChargeCapacityPercent)
	intGauge("ecoflow_design_capacity_percent", "Design capacity, percent", st.DesignCapacityPercent)
	intGauge("ecoflow_config_active_power", "ConfigActivePower raw value (1024 on DELTA 3 Plus, its Wh rating)", st.ConfigActivePower)

	// Voltage fields are exposed raw: the descriptor's unit fields
	// are not trustworthy, so the scaling is not yet confirmed.
	raw := map[string]int64{}
	for _, f := range st.Fields {
		raw[f.Path] = f.Raw
	}
	for _, key := range []string{"UPS.PowerSummary.Voltage", "UPS.PowerSummary.ConfigVoltage"} {
		if v, ok := raw[key]; ok {
			name := "ecoflow_voltage_raw"
			if strings.HasSuffix(key, "ConfigVoltage") {
				name = "ecoflow_config_voltage_raw"
			}
			gauge(name, key+" raw value (scaling unconfirmed)", float64(v))
		}
	}
	return b.String()
}

// effectiveStatus returns the status to expose: while the latest poll
// is failing, readings are hidden so ecoflow_up=0 is the only signal
// (stale charge values with up=1 would be worse than no values).
func effectiveStatus(status *ecoflow.HIDStatus, lastErr string) *ecoflow.HIDStatus {
	if lastErr != "" {
		return nil
	}
	return status
}

func pollLoop(st *state, interval time.Duration) {
	var reader *ecoflow.HIDReader
	var dev *ecoflow.USBDevice
	closeDev := func() {
		if dev != nil {
			dev.Close()
			dev = nil
			reader = nil
		}
	}
	for {
		if reader == nil {
			var err error
			dev, err = ecoflow.OpenUSB()
			if err == nil {
				reader, err = ecoflow.NewHIDReader(dev)
				if err != nil {
					closeDev()
				}
			}
			if err != nil {
				st.setErr(err)
				log.Printf("open: %v (retry in %s)", err, interval)
				time.Sleep(interval)
				continue
			}
			log.Printf("USB device opened")
		}
		status, err := reader.Read()
		if err != nil {
			st.setErr(err)
			log.Printf("read: %v (reopening device)", err)
			closeDev()
			time.Sleep(interval)
			continue
		}
		st.setOK(status)
		time.Sleep(interval)
	}
}

func main() {
	listen := flag.String("listen", ":9746", "listen address for /metrics")
	interval := flag.Duration("interval", 15*time.Second, "USB poll interval")
	flag.Parse()

	st := &state{}
	go pollLoop(st, *interval)

	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		status, lastSuccess, lastErr := st.snapshot()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprint(w, render(effectiveStatus(status, lastErr), lastSuccess))
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, lastSuccess, lastErr := st.snapshot()
		if lastSuccess.IsZero() {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "never polled successfully: %s\n", lastErr)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	log.Printf("ecoflow-exporter listening on %s, polling every %s", *listen, *interval)
	log.Fatal(http.ListenAndServe(*listen, mux))
}
