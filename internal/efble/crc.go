// Package efble implements the EcoFlow BLE protocol subset needed to
// read power telemetry from a DELTA 3 Plus, ported to Go from the
// reverse-engineered protocol implemented by ha-ef-ble's eflib
// (Apache-2.0, https://github.com/rabits/ha-ef-ble).
//
// The port is deliberately read-mostly: it performs the ECDH session
// handshake and md5(user_id+serial) authentication, decodes the
// pushed DisplayPropertyUpload protobuf telemetry, and answers the
// device's time-sync requests the way the reference library does.
// It never sends configuration/control packets.
package efble

// CRC8 (CRC-8/CCITT: poly 0x07, init 0x00, no reflection) and
// CRC16 (CRC-16/ARC: poly 0x8005 reflected, init 0x0000), matching
// eflib/crc.py.

var crc8Table [256]byte
var crc16Table [256]uint16

func init() {
	for i := 0; i < 256; i++ {
		crc := byte(i)
		for j := 0; j < 8; j++ {
			if crc&0x80 != 0 {
				crc = crc<<1 ^ 0x07
			} else {
				crc <<= 1
			}
		}
		crc8Table[i] = crc
	}
	for i := 0; i < 256; i++ {
		crc := uint16(i)
		for j := 0; j < 8; j++ {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0xA001
			} else {
				crc >>= 1
			}
		}
		crc16Table[i] = crc
	}
}

func crc8(data []byte) byte {
	var crc byte
	for _, b := range data {
		crc = crc8Table[crc^b]
	}
	return crc
}

func crc16(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc = crc16Table[byte(crc)^b] ^ (crc >> 8)
	}
	return crc
}
