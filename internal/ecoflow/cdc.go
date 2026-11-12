package ecoflow

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

// CDC-ACM telemetry protocol, as documented by the r3pcomms project
// and independently re-implemented in NUT's ecoflow-cdc-protocol.c
// (read-only telemetry request only).
//
// The unit is a composite USB device: interface 0 is HID, interface 1
// is CDC-ACM and appears as /dev/ttyACM* (or
// /dev/serial/by-id/usb-EcoFlow_...-if01). No vendor driver is needed;
// the in-tree cdc_acm driver provides the tty.
//
// Frame layout (request and response share it):
//
//	[0:2]   preamble 0x03AA (little endian on the wire: AA 03)
//	[2:4]   variable payload length, LE16 (total frame = 20 + this)
//	[4:6]   command (request: DE 2D)
//	[6:10]  sequence number, LE32
//	[10:18] request tail: FF FF 22 02 01 01 66 02
//	[18:]   payload, XOR-obfuscated with byte(sequence) from offset 18
//	[-2:]   CRC-16/ARC over the whole frame; a valid frame CRCs to 0
//
// Response segments start at offset 22. Each segment is
// type LE16, length u8, then length bytes of data.
const (
	cdcPreamble     = 0x03aa
	cdcRequestSize  = 20
	cdcXOROffset    = 18
	cdcSegmentStart = 22
	cdcMaxFrame     = 4096
)

// CDCPort is a byte stream to the unit's CDC-ACM interface.
type CDCPort interface {
	Write([]byte) (int, error)
	// ReadFull reads exactly n bytes, honouring the port's deadline.
	ReadFull(n int) ([]byte, error)
	Close() error
}

// CDCMetrics is one decoded CDC telemetry frame. Nil pointers mean the
// segment was not present in the frame.
type CDCMetrics struct {
	DesignCapacityMAh     *uint32  `json:"design_capacity_mah,omitempty"`
	SystemTemperatureC    *float64 `json:"system_temperature_c,omitempty"`
	BatteryTemperatureC   *float64 `json:"battery_temperature_c,omitempty"`
	OutputPowerW          *float64 `json:"output_power_w,omitempty"`
	InputPowerW           *float64 `json:"input_power_w,omitempty"`
	ACInputPowerW         *float64 `json:"ac_input_power_w,omitempty"`
	ACInputVoltage        *float64 `json:"ac_input_voltage,omitempty"`
	ACInputFrequencyHz    *float64 `json:"ac_input_frequency_hz,omitempty"`
	SolarInputPowerW      *float64 `json:"solar_input_power_w,omitempty"`
	RatedOutputPowerW     *uint32  `json:"rated_output_power_w,omitempty"`
	ACOutputPowerW        *float64 `json:"ac_output_power_w,omitempty"`
	ACOutputFrequencyHz   *float64 `json:"ac_output_frequency_hz,omitempty"`
	DCOutputPowerW        *float64 `json:"dc_output_power_w,omitempty"`
	USBAOutputPowerW      *float64 `json:"usb_a_output_power_w,omitempty"`
	USBCOutputPowerW      *float64 `json:"usb_c_output_power_w,omitempty"`
	ExtraBatteryInputW    *float64 `json:"extra_battery_input_power_w,omitempty"`
	ExtraBatteryOutputW   *float64 `json:"extra_battery_output_power_w,omitempty"`
	ChargingRuntimeMinute *uint32  `json:"charging_runtime_minutes,omitempty"`
	EMSVersion            string   `json:"ems_version,omitempty"`
	SerialNumber          string   `json:"serial_number,omitempty"`
}

// CRC16ARC computes CRC-16/ARC (reflected, polynomial 0xA001, init 0).
func CRC16ARC(data []byte) uint16 {
	crc := uint16(0)
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0xa001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

// BuildCDCRequest builds the 20-byte read-only telemetry request for a
// sequence number.
func BuildCDCRequest(sequence uint32) []byte {
	req := make([]byte, cdcRequestSize)
	binary.LittleEndian.PutUint16(req[0:2], cdcPreamble)
	binary.LittleEndian.PutUint16(req[2:4], 0)
	req[4] = 0xde
	req[5] = 0x2d
	binary.LittleEndian.PutUint32(req[6:10], sequence)
	copy(req[10:18], []byte{0xff, 0xff, 0x22, 0x02, 0x01, 0x01, 0x66, 0x02})
	crc := CRC16ARC(req[:cdcRequestSize-2])
	binary.LittleEndian.PutUint16(req[cdcRequestSize-2:], crc)
	return req
}

// ParseCDCFrame validates and decodes one response frame.
func ParseCDCFrame(frame []byte, expectedSequence uint32) (*CDCMetrics, error) {
	if len(frame) < cdcSegmentStart+2 || len(frame) > cdcMaxFrame {
		return nil, fmt.Errorf("cdc: bad frame length %d", len(frame))
	}
	if binary.LittleEndian.Uint16(frame[0:2]) != cdcPreamble {
		return nil, fmt.Errorf("cdc: bad preamble %04x", binary.LittleEndian.Uint16(frame[0:2]))
	}
	if want := cdcRequestSize + int(binary.LittleEndian.Uint16(frame[2:4])); want != len(frame) {
		return nil, fmt.Errorf("cdc: frame length mismatch: header says %d, got %d", want, len(frame))
	}
	if CRC16ARC(frame) != 0 {
		return nil, fmt.Errorf("cdc: CRC check failed")
	}
	sequence := binary.LittleEndian.Uint32(frame[6:10])
	if sequence != expectedSequence {
		return nil, fmt.Errorf("cdc: sequence mismatch: want %d, got %d", expectedSequence, sequence)
	}

	decoded := make([]byte, len(frame)-2)
	copy(decoded, frame[:len(frame)-2])
	xorByte := byte(sequence & 0xff)
	for i := cdcXOROffset; i < len(decoded); i++ {
		decoded[i] ^= xorByte
	}

	m := &CDCMetrics{}
	offset := cdcSegmentStart
	end := len(decoded)
	for offset < end {
		if end-offset < 3 {
			return nil, fmt.Errorf("cdc: truncated segment header at %d", offset)
		}
		typ := binary.LittleEndian.Uint16(decoded[offset : offset+2])
		segLen := int(decoded[offset+2])
		offset += 3
		if segLen > end-offset {
			return nil, fmt.Errorf("cdc: segment %d overruns frame", typ)
		}
		parseCDCSegment(m, typ, decoded[offset:offset+segLen])
		offset += segLen
	}
	return m, nil
}

func f32(data []byte) (float64, bool) {
	if len(data) != 4 {
		return 0, false
	}
	v := math.Float32frombits(binary.LittleEndian.Uint32(data))
	if math.IsInf(float64(v), 0) || math.IsNaN(float64(v)) {
		return 0, false
	}
	return float64(v), true
}

func f32abs(data []byte) (float64, bool) {
	v, ok := f32(data)
	if ok && v < 0 {
		v = -v
	}
	return v, ok
}

func parseCDCSegment(m *CDCMetrics, typ uint16, data []byte) {
	setF := func(dst **float64, abs bool) {
		var v float64
		var ok bool
		if abs {
			v, ok = f32abs(data)
		} else {
			v, ok = f32(data)
		}
		if ok {
			*dst = &v
		}
	}
	switch typ {
	case 3:
		if len(data) == 4 {
			v := binary.LittleEndian.Uint32(data)
			m.DesignCapacityMAh = &v
		}
	case 4:
		if len(data) >= 2 {
			sys := float64(data[0])
			bat := float64(data[1])
			m.SystemTemperatureC = &sys
			m.BatteryTemperatureC = &bat
		}
	case 7:
		setF(&m.OutputPowerW, false)
	case 8:
		setF(&m.InputPowerW, false)
	case 9:
		setF(&m.ACInputPowerW, false)
	case 10:
		if len(data) == 4 {
			v := float64(binary.LittleEndian.Uint16(data))
			m.ACInputFrequencyHz = &v
		}
	case 11:
		setF(&m.ACInputVoltage, false)
	case 12:
		setF(&m.SolarInputPowerW, false)
	case 13:
		if len(data) == 4 {
			v := binary.LittleEndian.Uint32(data)
			m.RatedOutputPowerW = &v
		}
	case 14:
		setF(&m.ACOutputPowerW, true)
	case 15:
		if len(data) == 4 {
			v := float64(binary.LittleEndian.Uint16(data))
			m.ACOutputFrequencyHz = &v
		}
	case 16:
		setF(&m.DCOutputPowerW, true)
	case 17:
		setF(&m.USBAOutputPowerW, true)
	case 18:
		setF(&m.USBCOutputPowerW, true)
	case 20:
		setF(&m.ExtraBatteryInputW, false)
	case 21:
		setF(&m.ExtraBatteryOutputW, true)
	case 22:
		s := strings.Map(func(r rune) rune {
			if r == 0xff || r == 0 {
				return -1
			}
			return r
		}, string(data))
		if s != "" {
			m.SerialNumber = s
		}
	case 23:
		// 0x1733 (wire bytes 33 17) is the sentinel for "not charging".
		if len(data) >= 2 {
			if v := binary.LittleEndian.Uint16(data); v != 0x1733 {
				minutes := uint32(v)
				m.ChargingRuntimeMinute = &minutes
			}
		}
	case 25:
		if len(data) == 4 {
			m.EMSVersion = fmt.Sprintf("%d.%d.%d.%d", data[3], data[2], data[1], data[0])
		}
	}
}

// CDCReader polls telemetry over a CDCPort.
type CDCReader struct {
	port     CDCPort
	sequence uint32
}

func NewCDCReader(port CDCPort) *CDCReader {
	return &CDCReader{port: port}
}

// Read sends one telemetry request and decodes the response.
func (r *CDCReader) Read() (*CDCMetrics, error) {
	req := BuildCDCRequest(r.sequence)
	if _, err := r.port.Write(req); err != nil {
		return nil, fmt.Errorf("cdc write: %w", err)
	}
	header, err := r.port.ReadFull(4)
	if err != nil {
		return nil, fmt.Errorf("cdc read header: %w", err)
	}
	total := cdcRequestSize + int(binary.LittleEndian.Uint16(header[2:4]))
	if total < cdcRequestSize || total > cdcMaxFrame {
		return nil, fmt.Errorf("cdc: implausible frame length %d", total)
	}
	rest, err := r.port.ReadFull(total - 4)
	if err != nil {
		return nil, fmt.Errorf("cdc read frame: %w", err)
	}
	frame := append(header, rest...)
	m, err := ParseCDCFrame(frame, r.sequence)
	if err != nil {
		return nil, err
	}
	r.sequence++
	return m, nil
}
