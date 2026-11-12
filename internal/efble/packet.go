package efble

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Packet is the V2/V3 EcoFlow BLE packet (eflib packet.py), the
// inner protocol carried inside EncPacket frames.
type Packet struct {
	Src     byte
	Dst     byte
	CmdSet  byte
	CmdID   byte
	Payload []byte
	DSrc    byte
	DDst    byte
	Version byte // full version byte as on the wire (0x03 for V3)
	Seq     [4]byte
}

const packetPrefix = 0xAA

// marshal serializes the packet exactly like Packet.to_bytes
// (product byte 0x0d, zeroed seq unless set).
func (p *Packet) marshal() []byte {
	version := p.Version
	if version == 0 {
		version = 0x03
	}
	out := []byte{packetPrefix, version, 0, 0}
	binary.LittleEndian.PutUint16(out[2:4], uint16(len(p.Payload)))
	out = append(out, crc8(out[:4]))
	out = append(out, 0x0d) // product_byte for product_id >= 0
	out = append(out, p.Seq[:]...)
	out = append(out, 0x00, 0x00)
	out = append(out, p.Src, p.Dst)
	if version&0x0F >= 3 {
		out = append(out, p.DSrc, p.DDst)
	}
	out = append(out, p.CmdSet, p.CmdID)
	out = append(out, p.Payload...)
	var crcb [2]byte
	binary.LittleEndian.PutUint16(crcb[:], crc16(out))
	out = append(out, crcb[:]...)
	return out
}

// parsePacket mirrors Packet.from_bytes for V2/V3, including the
// CRC checks and the optional payload XOR with seq[0] that the
// DELTA 3 family parser requests (xorPayload).
func parsePacket(data []byte, xorPayload bool) (*Packet, error) {
	if len(data) == 0 || data[0] != packetPrefix {
		return nil, errors.New("bad packet prefix")
	}
	if len(data) < 2 {
		return nil, errors.New("packet too small")
	}
	versionByte := data[1]
	version := versionByte & 0x0F
	sentinel := versionByte&0x10 != 0
	if version == 4 {
		return nil, errors.New("V4 packets not supported by this port")
	}
	if (version == 2 && len(data) < 18) || (version == 3 && len(data) < 20) {
		return nil, errors.New("packet too small")
	}
	payloadLen := int(binary.LittleEndian.Uint16(data[2:4]))

	if version >= 2 && version <= 3 && !sentinel &&
		!(len(data) >= 2 && data[len(data)-2] == 0xBB && data[len(data)-1] == 0xBB) {
		want := binary.LittleEndian.Uint16(data[len(data)-2:])
		if crc16(data[:len(data)-2]) != want {
			return nil, errors.New("bad packet CRC16")
		}
	}
	if crc8(data[:4]) != data[4] {
		return nil, errors.New("bad header CRC8")
	}

	pkt := &Packet{Version: versionByte}
	copy(pkt.Seq[:], data[6:10])
	pkt.Src = data[12]
	pkt.Dst = data[13]
	payloadStart := 16
	if version == 2 {
		pkt.CmdSet, pkt.CmdID = data[14], data[15]
	} else {
		payloadStart = 18
		pkt.DSrc, pkt.DDst = data[14], data[15]
		pkt.CmdSet, pkt.CmdID = data[16], data[17]
	}
	if payloadLen > 0 {
		if payloadStart+payloadLen > len(data) {
			return nil, fmt.Errorf("payload overruns packet: %d > %d",
				payloadStart+payloadLen, len(data))
		}
		payload := make([]byte, payloadLen)
		copy(payload, data[payloadStart:payloadStart+payloadLen])
		if xorPayload && pkt.Seq[0] != 0 {
			for i := range payload {
				payload[i] ^= pkt.Seq[0]
			}
		}
		if sentinel && len(payload) >= 2 &&
			payload[len(payload)-2] == 0xBB && payload[len(payload)-1] == 0xBB {
			payload = payload[:len(payload)-2]
		}
		pkt.Payload = payload
	}
	return pkt, nil
}

// EncPacket wire framing (eflib encpacket.py): the outer wrapper
// actually transferred over BLE.
//
//	5A 5A | frame_type<<4 | 0x01 | len(payload)+2 LE16 | payload | CRC16
const (
	encPrefix0 = 0x5A
	encPrefix1 = 0x5A

	frameTypeCommand  = 0x00
	frameTypeProtocol = 0x01
)

func marshalEncFrame(frameType byte, payload []byte) []byte {
	out := []byte{encPrefix0, encPrefix1, frameType << 4, 0x01, 0, 0}
	binary.LittleEndian.PutUint16(out[4:6], uint16(len(payload)+2))
	out = append(out, payload...)
	var crcb [2]byte
	binary.LittleEndian.PutUint16(crcb[:], crc16(out))
	out = append(out, crcb[:]...)
	return out
}

// marshalSimpleFrame wraps a plaintext handshake payload in a
// COMMAND EncPacket frame (SimplePacketAssembler.encode).
func marshalSimpleFrame(payload []byte) []byte {
	return marshalEncFrame(frameTypeCommand, payload)
}

// marshalProtocolFrame encrypts a Packet and wraps it in a
// PROTOCOL EncPacket frame (EncPacketAssembler.encode).
func marshalProtocolFrame(pkt *Packet, enc *type7Encryption) []byte {
	return marshalEncFrame(frameTypeProtocol, enc.encrypt(pkt.marshal()))
}

// frameExtractor incrementally extracts EncPacket frames from the
// notification byte stream, following EncPacketAssembler /
// SimplePacketAssembler resynchronization behavior: scan for the
// 5A 5A prefix, verify CRC16, skip spurious prefixes.
type frameExtractor struct {
	buf []byte
}

type encFrame struct {
	frameType byte
	payload   []byte // payload still encrypted for protocol frames
}

// feed appends data and returns every complete frame now available.
func (fx *frameExtractor) feed(data []byte) []encFrame {
	fx.buf = append(fx.buf, data...)
	var frames []encFrame
	for {
		f, ok := fx.next()
		if !ok {
			return frames
		}
		frames = append(frames, f)
	}
}

func (fx *frameExtractor) next() (encFrame, bool) {
	for {
		start := indexEncPrefix(fx.buf)
		if start < 0 {
			// Keep a trailing lone 0x5A: it may be half a prefix.
			if n := len(fx.buf); n > 0 && fx.buf[n-1] == encPrefix0 {
				fx.buf = fx.buf[n-1:]
			} else {
				fx.buf = fx.buf[:0]
			}
			return encFrame{}, false
		}
		if start > 0 {
			fx.buf = fx.buf[start:]
		}
		if len(fx.buf) < 8 {
			return encFrame{}, false
		}
		payloadLen := int(binary.LittleEndian.Uint16(fx.buf[4:6]))
		if payloadLen > 10000 {
			fx.buf = fx.buf[2:]
			continue
		}
		total := 6 + payloadLen
		if total > len(fx.buf) {
			// Incomplete: if another prefix shows up later in the
			// buffer, this candidate was spurious.
			if idx := indexEncPrefix(fx.buf[2:]); idx >= 0 {
				fx.buf = fx.buf[2+idx:]
				continue
			}
			return encFrame{}, false
		}
		frame := fx.buf[:total]
		payload := frame[6 : total-2]
		want := binary.LittleEndian.Uint16(frame[total-2:])
		hdr := make([]byte, 0, 6+len(payload))
		hdr = append(hdr, frame[:6]...)
		hdr = append(hdr, payload...)
		if crc16(hdr) != want {
			fx.buf = fx.buf[2:]
			continue
		}
		frameType := fx.buf[2] >> 4
		payloadCopy := make([]byte, len(payload))
		copy(payloadCopy, payload)
		fx.buf = fx.buf[total:]
		return encFrame{frameType: frameType, payload: payloadCopy}, true
	}
}

func indexEncPrefix(b []byte) int {
	for i := 0; i+1 < len(b); i++ {
		if b[i] == encPrefix0 && b[i+1] == encPrefix1 {
			return i
		}
	}
	return -1
}
