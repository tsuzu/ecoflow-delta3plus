package ecoflow

// HID usage names for the Power Device (0x84) and Battery System (0x85)
// pages, from the USB HID Power Devices usage table (the same table
// NUT's libhid uses). Keys are page<<16|usage.
var UsageNames = map[uint32]string{
	0x00840002: "PresentStatus",
	0x00840004: "UPS",
	0x00840010: "BatterySystem",
	0x00840012: "Battery",
	0x00840018: "OutletSystem",
	0x0084001a: "Input",
	0x0084001c: "Output",
	0x0084001e: "Flow",
	0x00840020: "Outlet",
	0x00840024: "PowerSummary",
	0x00840030: "Voltage",
	0x00840031: "Current",
	0x00840032: "Frequency",
	0x00840034: "ActivePower",
	0x00840035: "PercentLoad",
	0x00840036: "Temperature",
	0x00840040: "ConfigVoltage",
	0x00840041: "ConfigCurrent",
	0x00840042: "ConfigFrequency",
	0x00840044: "ConfigActivePower",
	0x00840055: "DelayBeforeReboot",
	0x00840056: "DelayBeforeStartup",
	0x00840057: "DelayBeforeShutdown",
	0x0084005a: "AudibleAlarmControl",
	0x00840065: "Overload",
	0x00840068: "ShutdownRequested",
	0x00840069: "ShutdownImminent",
	0x00840073: "CommunicationLost",

	0x00850029: "RemainingCapacityLimit",
	0x0085002a: "RemainingTimeLimit",
	0x0085002c: "CapacityMode",
	0x00850040: "TerminateCharge",
	0x00850041: "TerminateDischarge",
	0x00850042: "BelowRemainingCapacityLimit",
	0x00850043: "RemainingTimeLimitExpired",
	0x00850044: "Charging",
	0x00850045: "Discharging",
	0x00850046: "FullyCharged",
	0x00850047: "FullyDischarged",
	0x0085004b: "NeedReplacement",
	0x00850060: "AtRateTimeToFull",
	0x00850061: "AtRateTimeToEmpty",
	0x00850064: "RelativeStateOfCharge",
	0x00850065: "AbsoluteStateOfCharge",
	0x00850066: "RemainingCapacity",
	0x00850067: "FullChargeCapacity",
	0x00850068: "RunTimeToEmpty",
	0x00850069: "AverageTimeToEmpty",
	0x0085006a: "AverageTimeToFull",
	0x0085006b: "CycleCount",
	0x00850083: "DesignCapacity",
	0x00850084: "SpecificationInfo",
	0x00850085: "ManufacturerDate",
	0x00850086: "SerialNumber",
	0x00850089: "iDeviceChemistry",
	0x0085008b: "Rechargeable",
	0x0085008c: "WarningCapacityLimit",
	0x0085008d: "CapacityGranularity1",
	0x0085008e: "CapacityGranularity2",
	0x0085008f: "iOEMInformation",
	0x008500d0: "ACPresent",
	0x008500d1: "BatteryPresent",
	0x008500d2: "PowerFail",
	0x008500d8: "VoltageOutOfRange",
	0x008500da: "CurrentNotRegulated",
	0x008500db: "VoltageNotRegulated",
}

// UsageName returns a dotted path like
// "UPS.PowerSummary.RemainingCapacity" for a field, using its enclosing
// collections, or a hex fallback for unknown usages.
func UsagePath(collection []uint32, code uint32) string {
	path := ""
	for _, c := range collection {
		if name, ok := UsageNames[c]; ok {
			if path != "" {
				path += "."
			}
			path += name
		}
	}
	name, ok := UsageNames[code]
	if !ok {
		name = ""
	}
	if name == "" {
		if path != "" {
			path += "."
		}
		return path + hexUsage(code)
	}
	if path != "" {
		return path + "." + name
	}
	return name
}

func hexUsage(code uint32) string {
	const digits = "0123456789abcdef"
	b := []byte("0x00000000")
	for i := 0; i < 8; i++ {
		b[9-i] = digits[code&0xf]
		code >>= 4
	}
	return string(b)
}
