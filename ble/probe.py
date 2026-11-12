#!/usr/bin/env python3
"""Probe an EcoFlow DELTA 3 Plus over BLE using the ha-ef-ble eflib.

Read-only: connects, authenticates with the EcoFlow account User ID
(md5(user_id + serial) auth inside eflib), waits for the pushed
telemetry packets, prints JSON snapshots of the power fields, and
disconnects. No control packets are sent.
"""
import argparse
import asyncio
import json
import os
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "lib"))

from bleak import BleakScanner  # noqa: E402

from eflib import NewDevice  # noqa: E402

FIELDS = [
    "battery_level",
    "input_power",
    "output_power",
    "ac_input_power",
    "ac_output_power",
    "solar_input_power",
    "solar_input_power_2",
    "dc_port_input_power",
    "dc_port_2_input_power",
    "usbc_output_power",
    "usbc2_output_power",
    "usba_output_power",
    "usba2_output_power",
    "battery_input_power",
    "battery_output_power",
    "plugged_in_ac",
    "remaining_time_discharging",
    "cell_temperature",
]


async def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--address", required=True, help="BLE MAC of the unit")
    ap.add_argument("--user-id", default=os.environ.get("EF_USER_ID", ""))
    ap.add_argument("--duration", type=int, default=40,
                    help="max seconds to wait for telemetry")
    args = ap.parse_args()

    found = await BleakScanner.discover(timeout=12, return_adv=True)
    entry = None
    for addr, pair in found.items():
        if addr.upper() == args.address.upper():
            entry = pair
            break
    if entry is None:
        print(json.dumps({"error": "device_not_found_in_scan",
                          "seen": sorted(found)}))
        return 2
    ble_dev, adv = entry
    device = NewDevice(ble_dev, adv)
    if device is None:
        print(json.dumps({"error": "unsupported_advertisement"}))
        return 2
    print(json.dumps({"device": device.device,
                      "name": device.name,
                      "class": type(device).__name__}), flush=True)

    await device.connect(args.user_id, max_attempts=1)
    good = 0
    try:
        await device.wait_connected(timeout=25)
        deadline = time.time() + args.duration
        while time.time() < deadline:
            snap = {}
            for f in FIELDS:
                v = getattr(device, f, None)
                if v is not None:
                    snap[f] = v
            if snap:
                print(json.dumps({"t": int(time.time()), **snap}), flush=True)
                if "input_power" in snap and "output_power" in snap:
                    good += 1
                    if good >= 3:
                        break
            await asyncio.sleep(2)
    finally:
        await device.disconnect()
    return 0 if good else 3


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
