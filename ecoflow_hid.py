#!/usr/bin/env python3
"""EcoFlow DELTA 3 Plus — userland battery status over USB HID.

No kernel driver, no NUT, no hidraw: the device's HID report descriptor is
broken (missing End Collection bytes), so Linux hid-generic rejects it with
-EINVAL and no /dev/hidraw* appears. This script talks to it with libusb
(PyUSB) instead, claiming interface 0 directly and issuing GET_REPORT
(Feature) — the same approach NUT's usbhid-ups uses internally.

Grounded mappings (from the in-chat investigation, 2026-11-12; DELTA 3 Plus
only — plain DELTA 3 has no USB HID UPS function):
  - VID:PID 3746:ffff, front USB-C (HID) port, USB 2.0 *data* cable
    (charge-only cable does not work)
  - Feature report 12, byte 1: charge level (%)
  - Feature report 13, bytes 1..2 little-endian: runtime remaining (minutes);
    value 0x3317 is a sentinel meaning "not discharging" (no runtime)
  - Anything beyond SoC/runtime (AC present, charging/discharging flags,
    watts, temperatures) is NOT decoded here: the offsets need the real
    report descriptor from the user's unit. Use --dump to capture raw
    reports and map them against NUT's ecoflow-hid.c table.

Usage:
  pip install pyusb            # once, on the Linux host
  sudo cp 99-ecoflow-delta3plus.rules /etc/udev/rules.d/
  sudo udevadm control --reload-rules && sudo udevadm trigger
  python3 ecoflow_hid.py                    # human-readable one-liner
  python3 ecoflow_hid.py --json             # machine-readable
  python3 ecoflow_hid.py --prom-file /path/ecoflow.prom
                                            # Prometheus textfile (node_exporter
                                            # textfile collector / Thanos scrape)
  python3 ecoflow_hid.py --dump             # raw hex of feature reports 1..20,
                                            # for mapping further fields
  python3 ecoflow_hid.py --dump --report 12 # one raw report

Exit codes: 0 ok, 2 device not found / no libusb backend, 1 other USB error.
"""
import argparse
import json
import os
import sys
import tempfile

VID = 0x3746
PID = 0xFFFF
REPORT_SOC = 12
REPORT_RUNTIME = 13
RUNTIME_SENTINEL = 0x3317  # 13079: not discharging, no meaningful runtime

# GET_REPORT request: bmRequestType 0xA1 (class, interface, IN),
# bRequest 0x01, wValue = (Feature=0x03 << 8) | report_id.
GET_REPORT_FEATURE = 0xA1
FEATURE = 0x03


def open_device():
    try:
        import usb.core
        import usb.util
    except ImportError:
        print("error: PyUSB is not installed (pip install pyusb)", file=sys.stderr)
        sys.exit(2)
    dev = usb.core.find(idVendor=VID, idProduct=PID)
    if dev is None:
        print(f"error: no USB device {VID:04x}:{PID:04x} found — "
              "check the data cable and that the DELTA 3 Plus is on",
              file=sys.stderr)
        sys.exit(2)
    try:
        if dev.is_kernel_driver_active(0):
            dev.detach_kernel_driver(0)
    except Exception:
        pass  # no kernel driver attached (expected: hid-generic rejects it)
    dev.set_configuration()
    usb.util.claim_interface(dev, 0)
    return dev


def feature(dev, report_id, length=64):
    """GET_REPORT (Feature) for one report ID; returns bytes (may be short)."""
    return bytes(dev.ctrl_transfer(
        GET_REPORT_FEATURE, 0x01,
        (FEATURE << 8) | report_id, 0, length))


def read_status(dev):
    soc_raw = feature(dev, REPORT_SOC)
    if len(soc_raw) < 2:
        raise ValueError(f"report {REPORT_SOC} too short: {soc_raw.hex()}")
    soc = soc_raw[1]  # byte 0 is the report ID echoed back

    rt_raw = feature(dev, REPORT_RUNTIME)
    if len(rt_raw) < 3:
        raise ValueError(f"report {REPORT_RUNTIME} too short: {rt_raw.hex()}")
    runtime_min = int.from_bytes(rt_raw[1:3], "little")
    runtime = None if runtime_min == RUNTIME_SENTINEL else runtime_min
    return {"battery_charge_percent": soc,
            "battery_runtime_minutes": runtime,
            "runtime_sentinel": runtime_min == RUNTIME_SENTINEL}


def prom_text(status):
    lines = [
        "# HELP ecoflow_delta3plus_battery_charge_percent Battery charge level, percent",
        "# TYPE ecoflow_delta3plus_battery_charge_percent gauge",
        f"ecoflow_delta3plus_battery_charge_percent {status['battery_charge_percent']}",
    ]
    if status["battery_runtime_minutes"] is not None:
        lines += [
            "# HELP ecoflow_delta3plus_battery_runtime_minutes Estimated runtime remaining, minutes",
            "# TYPE ecoflow_delta3plus_battery_runtime_minutes gauge",
            f"ecoflow_delta3plus_battery_runtime_minutes {status['battery_runtime_minutes']}",
        ]
    return "\n".join(lines) + "\n"


def write_atomic(path, text):
    directory = os.path.dirname(os.path.abspath(path))
    fd, tmp = tempfile.mkstemp(dir=directory, prefix=".ecoflow-", suffix=".tmp")
    try:
        with os.fdopen(fd, "w") as f:
            f.write(text)
        os.replace(tmp, path)
    except BaseException:
        os.unlink(tmp)
        raise


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--json", action="store_true", help="print JSON instead of text")
    ap.add_argument("--dump", action="store_true",
                    help="dump raw feature reports instead of decoding")
    ap.add_argument("--report", type=int, default=None,
                    help="with --dump, dump only this report ID")
    ap.add_argument("--prom-file", default=None, metavar="PATH",
                    help="also write Prometheus textfile metrics to PATH")
    args = ap.parse_args()

    try:
        dev = open_device()
    except SystemExit:
        raise
    except Exception as e:  # libusb backend missing, permission denied, ...
        print(f"error: cannot open USB device: {e}", file=sys.stderr)
        return 2

    try:
        if args.dump:
            ids = [args.report] if args.report is not None else range(1, 21)
            out = {}
            for rid in ids:
                try:
                    raw = feature(dev, rid)
                    out[rid] = raw.hex()
                    print(f"report {rid:2d} ({len(raw)} bytes): {raw.hex()}")
                except Exception as e:
                    print(f"report {rid:2d}: error: {e}")
            if args.json:
                print(json.dumps(out, indent=2))
            return 0

        status = read_status(dev)
    except Exception as e:
        print(f"error: {e}", file=sys.stderr)
        return 1
    finally:
        try:
            import usb.util
            usb.util.release_interface(dev, 0)
        except Exception:
            pass

    if args.prom_file:
        write_atomic(args.prom_file, prom_text(status))

    if args.json:
        print(json.dumps(status))
    else:
        rt = (f"{status['battery_runtime_minutes']} min"
              if status["battery_runtime_minutes"] is not None
              else "— (not discharging)")
        print(f"DELTA 3 Plus: {status['battery_charge_percent']}%  "
              f"runtime {rt}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
