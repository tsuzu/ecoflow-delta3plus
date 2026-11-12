cdc_golden_frame.hex: one CDC-ACM telemetry response frame (sequence 0)
captured from an EcoFlow River 3 Plus by the r3pcomms project
(https://github.com/greyltc/r3pcomms, MIT), taken from its README debug
output. Note: the published output has the serial number redacted,
which invalidates the frame CRC — the test loader re-signs it before
parsing (see ecoflow_test.go).
