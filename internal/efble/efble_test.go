package efble

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"math/big"
	"testing"
)

// Golden vectors generated with the reference Python implementation
// (ha-ef-ble eflib + python-ecdsa + PyCryptodome) on real library
// code, so the Go port is checked byte-for-byte against it.

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCRCVectors(t *testing.T) {
	if got := crc8([]byte("123456789")); got != 0xF4 {
		t.Errorf("crc8 check = %02x, want f4", got)
	}
	if got := crc16([]byte("123456789")); got != 0xBB3D {
		t.Errorf("crc16 check = %04x, want bb3d", got)
	}
}

const (
	vecPriv1  = "1234567890abcdef1234567890abcdef12345678"
	vecPub1   = "d463026b5115d49f639b1bb411b9a9af37aa79bef17c1e630abccc30e297d91d00ac4522cbc1f0fa"
	vecPub2   = "c0266f450442faff0df62f977a5f0b1f348080228fbd868fa109b8f8f53e8625e3d2df909be9723e"
	vecShared = "a42803a059e4270ce6ded1f896ee89b3f7ac58e9"
)

func TestECDHVector(t *testing.T) {
	d1, _ := new(big.Int).SetString(vecPriv1, 16)
	kp := &ecdhKeyPair{priv: d1, pub: scalarMult(d1, ecPoint{secp160r1Gx, secp160r1Gy})}
	if got := kp.publicBytes(); !bytes.Equal(got, mustHex(t, vecPub1)) {
		t.Errorf("public key = %x, want %s", got, vecPub1)
	}
	peer, err := parsePublicBytes(mustHex(t, vecPub2))
	if err != nil {
		t.Fatal(err)
	}
	shared, err := kp.sharedSecret(peer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(shared, mustHex(t, vecShared)) {
		t.Errorf("shared = %x, want %s", shared, vecShared)
	}
}

func TestType7Vector(t *testing.T) {
	shared := mustHex(t, vecShared)
	iv := md5.Sum(shared)
	enc := &type7Encryption{key: shared[:16], iv: iv[:]}
	pt := mustHex(t, "aa031c00000000000000000000000000000000000000000000000000000000000000")
	want := mustHex(t, "ac0dda6ddd58f315a03155dab49df9ed03b5337d7045e6f0c2ebb830ff42f58003230584280ee306a5355e273943299a")
	if got := enc.encrypt(pt); !bytes.Equal(got, want) {
		t.Errorf("encrypt = %x, want %x", got, want)
	}
	if got := enc.decrypt(want); !bytes.Equal(got, pt) {
		t.Errorf("decrypt = %x, want %x", got, pt)
	}
}

func TestGenSessionKeyVector(t *testing.T) {
	seed := []byte{0x2a, 0x05}
	srand := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	got := genSessionKey(seed, srand)
	want := mustHex(t, "06ac9b38d90ba82bd8e176385e40fdd2")
	if !bytes.Equal(got, want) {
		t.Errorf("session key = %x, want %x", got, want)
	}
}

func TestPacketMarshalVectors(t *testing.T) {
	status := &Packet{Src: 0x21, Dst: 0x35, CmdSet: 0x35, CmdID: 0x89,
		DSrc: 0x01, DDst: 0x01, Version: 0x03}
	want := mustHex(t, "aa030000de0d00000000000021350101358930e4")
	if got := status.marshal(); !bytes.Equal(got, want) {
		t.Errorf("auth status packet = %x, want %x", got, want)
	}

	auth := &Packet{Src: 0x21, Dst: 0x35, CmdSet: 0x35, CmdID: 0x86,
		Payload: authToken("1234567890", "P351TESTSERIAL00"),
		DSrc:    0x01, DDst: 0x01, Version: 0x03}
	want = mustHex(t, "aa032000700d0000000000002135010135863336433431314546463941373933333632444639323842343431354341384542055a")
	if got := auth.marshal(); !bytes.Equal(got, want) {
		t.Errorf("auth packet = %x, want %x", got, want)
	}

	// Round-trip: parse the reference bytes back.
	parsed, err := parsePacket(want, true)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Src != 0x21 || parsed.CmdSet != 0x35 || parsed.CmdID != 0x86 {
		t.Errorf("parsed = %+v", parsed)
	}
	if string(parsed.Payload) != "36C411EFF9A793362DF928B4415CA8EB" {
		t.Errorf("parsed payload = %q", parsed.Payload)
	}
}

func TestEncFrameVectors(t *testing.T) {
	pub := mustHex(t, vecPub1)
	payload := append([]byte{0x01, 0x00}, pub...)
	want := mustHex(t, "5a5a00012c000100d463026b5115d49f639b1bb411b9a9af37aa79bef17c1e630abccc30e297d91d00ac4522cbc1f0fa6e09")
	if got := marshalSimpleFrame(payload); !bytes.Equal(got, want) {
		t.Errorf("simple frame = %x, want %x", got, want)
	}

	shared := mustHex(t, vecShared)
	iv := md5.Sum(shared)
	enc := &type7Encryption{key: shared[:16], iv: iv[:]}
	status := &Packet{Src: 0x21, Dst: 0x35, CmdSet: 0x35, CmdID: 0x89,
		DSrc: 0x01, DDst: 0x01, Version: 0x03}
	want = mustHex(t, "5a5a1001220033ea6fba5e183b5588fd230449c8b51bbbf42fab9965ead799d5cda66ec3c936853c")
	if got := marshalProtocolFrame(status, enc); !bytes.Equal(got, want) {
		t.Errorf("protocol frame = %x, want %x", got, want)
	}

	// The extractor must recover the frame from arbitrarily chunked input.
	fx := &frameExtractor{}
	var frames []encFrame
	for _, chunk := range [][]byte{want[:1], want[1:7], want[7:20], want[20:]} {
		frames = append(frames, fx.feed(chunk)...)
	}
	if len(frames) != 1 || frames[0].frameType != frameTypeProtocol {
		t.Fatalf("frames = %+v", frames)
	}
	plain := enc.decrypt(frames[0].payload)
	pkt, err := parsePacket(plain, true)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.CmdID != 0x89 {
		t.Errorf("round-tripped cmd_id = %02x", pkt.CmdID)
	}
}
