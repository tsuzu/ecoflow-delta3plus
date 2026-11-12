#!/usr/bin/env python3
"""EcoFlow BLE exporter: keeps a BLE connection to a DELTA 3 Plus
(via the ha-ef-ble eflib) and serves the power telemetry as
Prometheus metrics. Read-only: no control packets are ever sent.

The User ID is read from $EF_USER_ID_FILE (default
~/.config/ecoflow-ble/user-id) or $EF_USER_ID.
"""
import asyncio
import json
import os
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "lib"))

from bleak import BleakScanner  # noqa: E402

from eflib import NewDevice  # noqa: E402

ADDRESS = os.environ.get("EF_BLE_ADDRESS", "")
USER_ID_FILE = os.environ.get(
    "EF_USER_ID_FILE",
    os.path.expanduser("~/.config/ecoflow-ble/user-id"))
LISTEN = ("", int(os.environ.get("EF_BLE_PORT", "9747")))

# attr -> (metric name, help)
GAUGES = {
    "battery_level": ("ecoflow_ble_battery_charge_percent",
                      "Battery charge level, percent (BLE, precise)"),
    "input_power": ("ecoflow_ble_input_watts",
                    "Total input power, watts"),
    "output_power": ("ecoflow_ble_output_watts",
                     "Total output power, watts"),
    "ac_input_power": ("ecoflow_ble_ac_input_watts",
                       "AC input power, watts"),
    "ac_output_power": ("ecoflow_ble_ac_output_watts",
                        "AC output power, watts"),
    "solar_input_power": ("ecoflow_ble_solar_input_watts",
                          "Solar input power, port 1, watts"),
    "solar_input_power_2": ("ecoflow_ble_solar2_input_watts",
                            "Solar input power, port 2, watts"),
    "dc_port_input_power": ("ecoflow_ble_dc_input_watts",
                            "DC port 1 input power, watts"),
    "dc_port_2_input_power": ("ecoflow_ble_dc2_input_watts",
                              "DC port 2 input power, watts"),
    "usbc_output_power": ("ecoflow_ble_usbc1_output_watts",
                          "USB-C port 1 output power, watts"),
    "usbc2_output_power": ("ecoflow_ble_usbc2_output_watts",
                           "USB-C port 2 output power, watts"),
    "usba_output_power": ("ecoflow_ble_usba1_output_watts",
                          "USB-A port 1 output power, watts"),
    "usba2_output_power": ("ecoflow_ble_usba2_output_watts",
                           "USB-A port 2 output power, watts"),
    "battery_input_power": ("ecoflow_ble_battery_input_watts",
                            "Power into the battery, watts"),
    "battery_output_power": ("ecoflow_ble_battery_output_watts",
                             "Power out of the battery, watts"),
    "cell_temperature": ("ecoflow_ble_cell_temperature_celsius",
                         "Max cell temperature, celsius"),
    "remaining_time_discharging": (
        "ecoflow_ble_remaining_time_discharging_minutes",
        "Estimated remaining discharge time, minutes"),
}
BOOLS = {
    "plugged_in_ac": ("ecoflow_ble_ac_plugged_in", "AC input plugged in"),
}

STATE = {"values": {}, "ts": 0.0, "connected": False}
LOCK = threading.Lock()


def fmt(v):
    if isinstance(v, bool):
        return "1" if v else "0"
    s = f"{float(v):.2f}".rstrip("0").rstrip(".")
    return s if s else "0"


def render():
    with LOCK:
        values = dict(STATE["values"])
        ts = STATE["ts"]
        connected = STATE["connected"]
    up = 1 if connected and ts and time.time() - ts < 30 else 0
    lines = [
        "# HELP ecoflow_ble_up Whether BLE telemetry is flowing (1) or not (0)",
        "# TYPE ecoflow_ble_up gauge",
        f"ecoflow_ble_up {up}",
    ]
    if ts:
        lines += [
            "# HELP ecoflow_ble_last_success_unixtime_seconds Unix time of the last telemetry snapshot",
            "# TYPE ecoflow_ble_last_success_unixtime_seconds gauge",
            f"ecoflow_ble_last_success_unixtime_seconds {fmt(ts)}",
        ]
    if up:
        for attr, (name, help_) in {**GAUGES, **BOOLS}.items():
            if attr in values and values[attr] is not None:
                lines += [f"# HELP {name} {help_}",
                          f"# TYPE {name} gauge",
                          f"{name} {fmt(values[attr])}"]
    return "\n".join(lines) + "\n"


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path.startswith("/metrics"):
            body = render().encode()
            self.send_response(200)
            self.send_header("Content-Type",
                             "text/plain; version=0.0.4; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        elif self.path.startswith("/healthz"):
            body = render()
            ok = "\necoflow_ble_up 1\n" in "\n" + body
            self.send_response(200 if ok else 503)
            self.end_headers()
            self.wfile.write(b"ok\n" if ok else b"down\n")
        else:
            self.send_response(404)
            self.end_headers()

    def log_message(self, *a):
        pass


async def snapshot_loop(device):
    while device.is_connected:
        snap = {}
        for attr in list(GAUGES) + list(BOOLS):
            v = getattr(device, attr, None)
            if v is not None:
                snap[attr] = v
        if "input_power" in snap:
            with LOCK:
                STATE["values"] = snap
                STATE["ts"] = time.time()
                STATE["connected"] = True
        await asyncio.sleep(3)
    with LOCK:
        STATE["connected"] = False


async def supervise(user_id):
    while True:
        try:
            found = await BleakScanner.discover(timeout=12, return_adv=True)
            entry = None
            for addr, pair in found.items():
                if addr.upper() == ADDRESS.upper():
                    entry = pair
                    break
            if entry is None:
                print("supervise: device not in scan, retry", flush=True)
                await asyncio.sleep(10)
                continue
            device = NewDevice(entry[0], entry[1])
            if device is None:
                print("supervise: unsupported advertisement", flush=True)
                await asyncio.sleep(10)
                continue
            print(f"supervise: connecting to {device.name}", flush=True)
            await device.connect(user_id)
            await device.wait_connected(timeout=30)
            print("supervise: connected, streaming", flush=True)
            await snapshot_loop(device)
            print("supervise: disconnected, will rescan", flush=True)
            try:
                await device.disconnect()
            except Exception:
                pass
        except Exception as e:  # keep the exporter alive on any failure
            print(f"supervise: error {e!r}", flush=True)
            with LOCK:
                STATE["connected"] = False
            await asyncio.sleep(10)


def main():
    if not ADDRESS:
        print("EF_BLE_ADDRESS is required", file=sys.stderr)
        return 2
    user_id = os.environ.get("EF_USER_ID", "").strip()
    if not user_id and os.path.exists(USER_ID_FILE):
        user_id = open(USER_ID_FILE).read().strip()
    if not user_id:
        print("no user id (EF_USER_ID / user-id file)", file=sys.stderr)
        return 2
    server = ThreadingHTTPServer(LISTEN, Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    print(f"ecoflow-ble-exporter on :{LISTEN[1]} for {ADDRESS}", flush=True)
    asyncio.run(supervise(user_id))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
