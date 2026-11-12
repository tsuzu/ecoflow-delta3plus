// ecoflow-ble-exporter keeps a BLE connection to an EcoFlow
// DELTA 3 Plus and serves its power telemetry as Prometheus
// metrics. Go port of the original Python exporter; the protocol
// implementation lives in internal/efble.
//
// Configuration (environment):
//
//	EF_BLE_ADDRESS   BLE MAC of the unit (required)
//	EF_USER_ID       EcoFlow account User ID, or
//	EF_USER_ID_FILE  file holding it (default ~/.config/ecoflow-ble/user-id)
//	EF_BLE_PORT      metrics port (default 9747)
//
// With -probe it prints JSON telemetry snapshots instead of
// serving metrics, and exits after -duration.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tsuzu/ecoflow-delta3plus/internal/efble"
)

// metric definitions in exporter attribute order:
// attr -> (metric name, help)
var gaugeDefs = []struct{ attr, name, help string }{
	{"battery_level", "ecoflow_ble_battery_charge_percent", "Battery charge level, percent (BLE, precise)"},
	{"input_power", "ecoflow_ble_input_watts", "Total input power, watts"},
	{"output_power", "ecoflow_ble_output_watts", "Total output power, watts"},
	{"ac_input_power", "ecoflow_ble_ac_input_watts", "AC input power, watts"},
	{"ac_output_power", "ecoflow_ble_ac_output_watts", "AC output power, watts"},
	{"solar_input_power", "ecoflow_ble_solar_input_watts", "Solar input power, port 1, watts"},
	{"solar_input_power_2", "ecoflow_ble_solar2_input_watts", "Solar input power, port 2, watts"},
	{"dc_port_input_power", "ecoflow_ble_dc_input_watts", "DC port 1 input power, watts"},
	{"dc_port_2_input_power", "ecoflow_ble_dc2_input_watts", "DC port 2 input power, watts"},
	{"usbc_output_power", "ecoflow_ble_usbc1_output_watts", "USB-C port 1 output power, watts"},
	{"usbc2_output_power", "ecoflow_ble_usbc2_output_watts", "USB-C port 2 output power, watts"},
	{"usba_output_power", "ecoflow_ble_usba1_output_watts", "USB-A port 1 output power, watts"},
	{"usba2_output_power", "ecoflow_ble_usba2_output_watts", "USB-A port 2 output power, watts"},
	{"battery_input_power", "ecoflow_ble_battery_input_watts", "Power into the battery, watts"},
	{"battery_output_power", "ecoflow_ble_battery_output_watts", "Power out of the battery, watts"},
	{"cell_temperature", "ecoflow_ble_cell_temperature_celsius", "Max cell temperature, celsius"},
	{"remaining_time_discharging", "ecoflow_ble_remaining_time_discharging_minutes", "Estimated remaining discharge time, minutes"},
}
var boolDefs = []struct{ attr, name, help string }{
	{"plugged_in_ac", "ecoflow_ble_ac_plugged_in", "AC input plugged in"},
}

type server struct {
	client *efble.Client
}

func fmtVal(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}

func (s *server) render() (string, bool) {
	t, ts, _ := s.client.Latest()
	up := !ts.IsZero() && time.Since(ts) < 30*time.Second
	var b strings.Builder
	b.WriteString("# HELP ecoflow_ble_up Whether BLE telemetry is flowing (1) or not (0)\n")
	b.WriteString("# TYPE ecoflow_ble_up gauge\n")
	if up {
		b.WriteString("ecoflow_ble_up 1\n")
	} else {
		b.WriteString("ecoflow_ble_up 0\n")
	}
	if !ts.IsZero() {
		b.WriteString("# HELP ecoflow_ble_last_success_unixtime_seconds Unix time of the last telemetry snapshot\n")
		b.WriteString("# TYPE ecoflow_ble_last_success_unixtime_seconds gauge\n")
		fmt.Fprintf(&b, "ecoflow_ble_last_success_unixtime_seconds %d\n", ts.Unix())
	}
	if up {
		for _, d := range gaugeDefs {
			if v, ok := t.Values[d.attr]; ok {
				fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n",
					d.name, d.help, d.name, d.name, fmtVal(v))
			}
		}
		for _, d := range boolDefs {
			if v, ok := t.Bools[d.attr]; ok {
				val := "0"
				if v {
					val = "1"
				}
				fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n",
					d.name, d.help, d.name, d.name, val)
			}
		}
	}
	return b.String(), up
}

func (s *server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	body, _ := s.render()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(body))
}

func (s *server) healthzHandler(w http.ResponseWriter, r *http.Request) {
	_, up := s.render()
	if up {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("down\n"))
	}
}

func loadUserID() string {
	if v := strings.TrimSpace(os.Getenv("EF_USER_ID")); v != "" {
		return v
	}
	path := os.Getenv("EF_USER_ID_FILE")
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".config", "ecoflow-ble", "user-id")
	}
	if b, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

func main() {
	probe := flag.Bool("probe", false, "print JSON telemetry snapshots and exit")
	duration := flag.Duration("duration", 60*time.Second, "probe duration")
	flag.Parse()

	address := os.Getenv("EF_BLE_ADDRESS")
	if address == "" {
		log.Fatal("EF_BLE_ADDRESS is required")
	}
	userID := loadUserID()
	if userID == "" {
		log.Fatal("no user id (EF_USER_ID / user-id file)")
	}
	port := os.Getenv("EF_BLE_PORT")
	if port == "" {
		port = "9747"
	}

	client := &efble.Client{
		Address: address,
		UserID:  userID,
		Logf:    log.Printf,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go client.Run(ctx)

	if *probe {
		runProbe(client, *duration)
		return
	}

	srv := &server{client: client}
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", srv.metricsHandler)
	mux.HandleFunc("/healthz", srv.healthzHandler)
	log.Printf("ecoflow-ble-exporter (go) on :%s for %s", port, address)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

func runProbe(client *efble.Client, duration time.Duration) {
	deadline := time.Now().Add(duration)
	var lastJSON string
	for time.Now().Before(deadline) {
		t, ts, _ := client.Latest()
		if !ts.IsZero() && time.Since(ts) < 5*time.Second {
			m := map[string]any{"t": ts.Unix()}
			keys := make([]string, 0, len(t.Values))
			for k := range t.Values {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				m[k] = t.Values[k]
			}
			for k, v := range t.Bools {
				m[k] = v
			}
			b, _ := json.Marshal(m)
			if string(b) != lastJSON {
				fmt.Println(string(b))
				lastJSON = string(b)
			}
		}
		time.Sleep(2 * time.Second)
	}
}
