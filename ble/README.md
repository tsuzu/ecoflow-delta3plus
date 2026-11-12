# EcoFlow BLE power exporter (DELTA 3 Plus)

Power telemetry (watts) for the DELTA 3 Plus is not available over
USB — the HID descriptor has no live power fields and the unit has
no CDC-ACM interface. It *is* available over Bluetooth LE, using
the reverse-engineered EcoFlow BLE protocol implemented by
[rabits/ha-ef-ble](https://github.com/rabits/ha-ef-ble). Its
protocol library (`eflib`) is vendored under `lib/eflib`
(Apache-2.0, see `THIRD_PARTY_NOTICES.md`) and used standalone —
no Home Assistant required.

What you get (pushed by the unit every ~2 s once authenticated):

- total input / output watts (`pow_in_sum_w` / `pow_out_sum_w`)
- AC input / output watts, solar input watts (both ports),
  DC input watts, USB-C / USB-A output watts per port
- battery charge % (with decimals), battery in/out watts,
  max cell temperature, remaining discharge time

## Files

- `probe.py` — one-shot: connect, authenticate, print JSON
  snapshots of the power fields, disconnect. Sends no control
  packets.
- `exporter.py` — long-running Prometheus exporter on `:9747`
  (metrics prefixed `ecoflow_ble_`), with auto-reconnect.
  `ecoflow-ble-exporter.service` is a sample systemd unit
  (fill in your BLE address and user-id path).
- `lib/eflib/` — vendored protocol library from ha-ef-ble.

## Setup

```sh
pip install -r requirements.txt   # bleak, PyCryptodome, ...
# the vendored eflib is picked up from ./lib automatically
```

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
on Linux); EcoFlow units advertise as `EF-D3*`.

```sh
EF_BLE_ADDRESS=AA:BB:CC:DD:EE:FF python3 probe.py --address AA:BB:CC:DD:EE:FF
EF_BLE_ADDRESS=AA:BB:CC:DD:EE:FF python3 exporter.py   # :9747/metrics
```

## Notes

- BLE is single-connection exclusive: while the exporter holds the
  connection, the EcoFlow app cannot connect over Bluetooth
  (Wi-Fi/cloud access is unaffected).
- The protocol is reverse-engineered; a firmware update may break
  it. The exporter is read-only and never sends control packets.
