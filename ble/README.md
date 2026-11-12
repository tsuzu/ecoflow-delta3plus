# EcoFlow BLE power telemetry (DELTA 3 Plus) — Go

Power telemetry (watts) for the DELTA 3 Plus is not available over
USB — the HID descriptor has no live power fields and the unit has
no CDC-ACM interface. It *is* available over Bluetooth LE, using
the reverse-engineered EcoFlow BLE protocol.

The implementation is Go, in this repository:

- `internal/efble/` — the protocol: SECP160r1 ECDH session
  handshake, AES-CBC session encryption, session-key derivation,
  V3 packet codec, and the `DisplayPropertyUpload` protobuf
  telemetry decoder for the DELTA 3 family.
- `cmd/ecoflow-ble-exporter/` — a Prometheus exporter (`:9747`,
  metrics prefixed `ecoflow_ble_`) that keeps the BLE connection
  up and reconnects automatically. `-probe` prints JSON snapshots
  instead of serving metrics.

The protocol was ported from the Python implementation in
[rabits/ha-ef-ble](https://github.com/rabits/ha-ef-ble) (its
`eflib`); see `THIRD_PARTY_NOTICES.md` at the repository root.
The Go port is verified byte-for-byte against the reference with
golden-vector tests (CRC, ECDH, session key, packet framing) and
on real hardware. An earlier Python version of this exporter
(used until 2026-11-13) was replaced by the Go port.

What you get (pushed by the unit every ~2 s once authenticated):

- total input / output watts
- AC input / output watts, solar input watts (both ports),
  DC input watts, USB-C / USB-A output watts per port
- battery charge % (with decimals), battery in/out watts,
  max cell temperature, remaining discharge time

## Setup

Authentication is ECDH + `md5(user_id + serial)`: the exporter
needs your **EcoFlow account User ID**. It is not shown anywhere
in the EcoFlow app UI. Ways to get it:

- the helper page https://gnox.github.io/user_id (logs in against
  the official EcoFlow API from your browser), or
- log in on ecoflow.com and read `data.user.userId` from the
  `POST /auth/login` response in devtools.

Store it in a file only the exporter can read:

```sh
mkdir -p ~/.config/ecoflow-ble
printf %s 'YOUR_USER_ID' > ~/.config/ecoflow-ble/user-id
chmod 600 ~/.config/ecoflow-ble/user-id
```

Find the unit's BLE address with any scanner (`bluetoothctl scan le`
on Linux); EcoFlow units advertise as `EF-D3*`. The serial used
for authentication is derived from the advertisement itself.

```sh
go build -o ecoflow-ble-exporter ./cmd/ecoflow-ble-exporter
EF_BLE_ADDRESS=AA:BB:CC:DD:EE:FF ./ecoflow-ble-exporter -probe -duration 30s
EF_BLE_ADDRESS=AA:BB:CC:DD:EE:FF ./ecoflow-ble-exporter   # :9747/metrics
```

A sample systemd unit is in `deploy/ecoflow-ble-exporter.service`
(fill in your BLE address and user-id path). The host needs a
working BlueZ stack; the exporter talks to it over D-Bus
(pure Go, no cgo).

## Notes

- BLE is single-connection exclusive: while the exporter holds the
  connection, the EcoFlow app cannot connect over Bluetooth
  (Wi-Fi/cloud access is unaffected).
- The protocol is reverse-engineered; a firmware update may break
  it. The exporter never sends configuration/control packets; it
  only authenticates, reads the pushed telemetry, and answers the
  device's time-sync requests the way the reference library does.
- Scope of the port: DELTA 3 Plus (SN prefix `P351`) telemetry.
  Other models share most of the protocol but their protobuf
  layouts are not included.
