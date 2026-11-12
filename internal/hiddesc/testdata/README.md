river3plus.rd.hex.txt: raw HID report descriptor of an EcoFlow River 3 Plus
(same USB HID UPS family and VID:PID 3746:ffff as the DELTA 3 Plus),
captured from /sys/kernel/debug/hid by the r3pcomms project
(https://github.com/greyltc/r3pcomms, MIT). Used as a parser fixture:
it exhibits the real firmware bug — 3 missing End Collection bytes.

delta3plus.rd.hex: raw HID report descriptor of a DELTA 3 Plus,
captured 2026-11-12 via GET_DESCRIPTOR from the Go tool in this repo
(401 bytes, continuous hex). This unit's descriptor is well-formed
(0 missing End Collection bytes) and its layout differs from the
River's (e.g. RemainingTimeLimit is report 8, Voltage report 11) —
the regression fixture for descriptor-driven decoding.
