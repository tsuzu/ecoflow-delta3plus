# Third-party notices

## EcoFlow BLE protocol (ha-ef-ble)

The BLE protocol implementation in `internal/efble/` (session
handshake, packet codec, session-key derivation including the
embedded `keydata.bin` table, and the `DisplayPropertyUpload`
field layout) is a Go port of the reverse-engineered protocol
implemented by the [ha-ef-ble](https://github.com/rabits/ha-ef-ble)
project, Copyright (c) the ha-ef-ble contributors, licensed under
the Apache License, Version 2.0 (see `LICENSE`).

The port was verified against the reference implementation with
golden test vectors and on real DELTA 3 Plus hardware.
