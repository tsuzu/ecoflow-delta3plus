# EcoFlow DELTA 3 Plus — userland USB status fetcher (+ BLE power)

Reads battery charge, runtime and AC/charging state from a DELTA 3
Plus over USB, entirely in userland on Linux — no kernel HID driver,
no NUT. Live power (watts) is *not* available over USB on this model
(no CDC interface, and the HID descriptor has no live power fields);
for watts there is a BLE companion exporter under `ble/` that speaks
the EcoFlow Bluetooth protocol.

Three implementations live here:

- **Go (main)**: `cmd/ecoflow` + `internal/` — HID over libusb *and*
  the CDC-ACM telemetry protocol.
- **Python (reference / minimal)**: `ecoflow_hid.py` — HID only,
  charge % + runtime only.
- **BLE exporter (Go)**: `cmd/ecoflow-ble-exporter` +
  `internal/efble` — total/AC/solar/USB power, cell temperature
  and precise charge % over Bluetooth LE, as Prometheus metrics.
  The protocol is a Go port of ha-ef-ble's eflib; see `ble/`.

DELTA 3 Plus only; plain DELTA 3 has no USB HID UPS function.

## Why userland (and why hidraw doesn't work)

In this device family the HID report descriptor may be malformed
(the River 3 Plus's is missing 3 `End Collection 0xC0` bytes, so Linux
`hid-generic` rejects it with `probe failed with error -22` and no
`/dev/hidraw*` appears; the DELTA 3 Plus unit tested here ships a
well-formed 401-byte descriptor — see Status). Talking to it with
libusb and issuing `GET_REPORT (Feature)` directly bypasses the
kernel HID parser either way — this is also how NUT's `usbhid-ups`
reaches it. The Go HID parser repairs the descriptor in memory when
needed and is fully descriptor-driven, so field offsets come from
the descriptor itself, not from hard-coded report layouts.

## What the Go version reads

**HID** (interface 0, VID:PID `3746:ffff`), decoded per NUT's
`ecoflow-hid` subdriver mapping:

| Item | Source |
|---|---|
| Charge % | `PowerSummary.RemainingCapacity` |
| Runtime remaining (min) | `RunTimeToEmpty`; wire value is minutes, `0x3317` = not discharging |
| AC present / charging / discharging / fully charged / battery present / need replacement | `PresentStatus.*` 1-bit flags |
| Charge low / warning %, runtime low (min), full-charge & design capacity %, ConfigActivePower (1024 on DELTA 3 Plus) | Feature reports |

Every other decoded Feature field (voltage etc., whose scaling is not
yet confirmed on real DELTA 3 Plus hardware) is available via
`--dump` / in the JSON `fields` array.

**CDC-ACM**: the protocol is implemented (CRC-16/ARC framing,
sequence-number XOR obfuscation, TLV segments; input/output power
split by AC/solar/DC/USB-A/USB-C, temperatures, EMS version, …) for
the River 3 Plus, which enumerates a CDC interface as if01.
**The DELTA 3 Plus does not expose a CDC interface at all**
(verified 2026-11-12 on real hardware: `bNumInterfaces=1`, HID only),
so on the DELTA the CDC section simply reports "no EcoFlow CDC-ACM
port found" in `-source all` mode. Auto-detection only matches
`/dev/serial/by-id` EcoFlow entries — there is intentionally no bare
`/dev/ttyACM*` fallback, so the tool can never inject frames into an
unrelated serial device (smart-meter adapter, modem) on the same host.

## Build & run (Linux host)

```sh
# build deps: Go >= 1.23, libusb-1.0 headers (runtime lib is standard)
sudo apt install libusb-1.0-0-dev pkg-config
go build -o ecoflow ./cmd/ecoflow

# permissions
sudo cp 99-ecoflow-delta3plus.rules /etc/udev/rules.d/
sudo udevadm control --reload-rules && sudo udevadm trigger
# front USB-C (HID) port on the unit -> host USB-A, USB 2.0 *data* cable
# (charge-only cable will not work; the front USB-C is occupied while in use)

./ecoflow                       # one-shot, human readable
./ecoflow -json                 # JSON (hid + cdc sections)
./ecoflow -source hid           # HID only
./ecoflow -every 10s            # poll (add -count N to stop)
./ecoflow -prom-file /path/ecoflow.prom
                                # Prometheus textfile metrics, atomic write
                                # (for node_exporter textfile collector)
./ecoflow -descriptor           # raw report descriptor + parsed field list
./ecoflow -dump                 # all decoded HID feature fields
```

Exit codes: 0 ok, 2 device/port not found, 1 read error.

## Tests

```sh
go test ./...
```

Fixtures are real captures, see the testdata READMEs:

- River 3 Plus HID report descriptor (376 bytes, exhibits the real
  missing-End-Collection bug) — parser repair + field layout tests.
- River 3 Plus CDC telemetry frame — segment decode tests (the
  published capture is serial-redacted, so its CRC is re-signed in
  the test loader; see testdata README).

## Exporter mode

`cmd/ecoflow-exporter` polls the unit every 15s in the background
and serves Prometheus metrics on `:9746` (`ecoflow_up`,
`ecoflow_battery_charge_percent`, `ecoflow_battery_runtime_minutes`,
`ecoflow_ac_present`, `ecoflow_charging`, …, `ecoflow_voltage_raw`).
A sample systemd unit is in `deploy/ecoflow-exporter.service`.
If the unit is unplugged, the exporter serves `ecoflow_up 0` (and no
stale readings) and recovers automatically when it reappears.

For power telemetry (input/output watts per port group), see the
BLE exporter: `cmd/ecoflow-ble-exporter` (Prometheus on `:9747`,
sample unit `deploy/ecoflow-ble-exporter.service`, docs in `ble/`).

## Status

**Verified on real DELTA 3 Plus hardware (2026-11-12, Raspberry Pi,
arm64 binary cross-built in a container):**

- Device enumerates as `3746:ffff "EF-UPS-DELTA"`, serial on the USB
  descriptor; `bNumInterfaces=1` — HID only, **no CDC interface**.
- Its report descriptor is **401 bytes and well-formed** (0 missing
  End Collection bytes) — on this firmware the "broken descriptor"
  bug does not reproduce, `hid-generic` binds and `/dev/hidraw0`
  appears. (The kernel's hidraw node is root-only and unnecessary;
  the libusb path works regardless.) The River 3 Plus's broken
  376-byte descriptor remains in testdata as the repair fixture.
- Sample read: charge **80%**, runtime 3200 min, AC present,
  `FullyCharged=true` at 80% (consistent with an 80% charge limit
  set in the app), ConfigActivePower **1024**, charge-low 30%,
  warning 5%, runtime-low 10 min.
- Layout differs from the River (RemainingTimeLimit = report 8,
  Voltage = report 11); the descriptor-driven decoder handles it,
  and the captured descriptor is now a regression fixture
  (`internal/hiddesc/testdata/delta3plus.rd.hex`).
- Still unconfirmed: Voltage scaling (raw 1300, ConfigVoltage raw
  1380; the descriptor's unit fields are not trustworthy), and
  RunTimeToEmpty semantics while AC is present (firmware returned
  3200, not the `0x3317` sentinel, with AC present and not
  discharging).
