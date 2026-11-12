package efble

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"tinygo.org/x/bluetooth"
)

// GATT characteristic UUIDs for the EcoFlow rfcomm-style protocol
// (eflib _BT_PROTOCOL_UUIDS), with the Nordic UART pair as fallback.
func mustUUID(s string) bluetooth.UUID {
	u, err := bluetooth.ParseUUID(s)
	if err != nil {
		panic(err)
	}
	return u
}

var (
	notifyUUIDs = []bluetooth.UUID{
		mustUUID("00000003-0000-1000-8000-00805f9b34fb"),
		mustUUID("6e400003-b5a3-f393-e0a9-e50e24dcca9e"),
	}
	writeUUIDs = []bluetooth.UUID{
		mustUUID("00000002-0000-1000-8000-00805f9b34fb"),
		mustUUID("6e400002-b5a3-f393-e0a9-e50e24dcca9e"),
	}
)

const manufacturerKey = 0xB5B5

// Client maintains a BLE connection to one EcoFlow unit and keeps
// the latest telemetry snapshot.
type Client struct {
	Address string // BLE MAC, e.g. "AA:BB:CC:DD:EE:FF"
	UserID  string
	Logf    func(format string, args ...any)

	mu        sync.Mutex
	latest    Telemetry
	lastTime  time.Time
	connected bool
	serial    string
}

func (c *Client) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// Latest returns the most recent telemetry snapshot, when it was
// received, and whether the client is currently connected.
func (c *Client) Latest() (Telemetry, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.latest, c.lastTime, c.connected
}

// Serial returns the device serial derived from its advertisement.
func (c *Client) Serial() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serial
}

func (c *Client) setConnected(v bool) {
	c.mu.Lock()
	c.connected = v
	c.mu.Unlock()
}

func (c *Client) update(t Telemetry) {
	c.mu.Lock()
	c.latest = t
	c.lastTime = time.Now()
	c.mu.Unlock()
}

// Run supervises the connection: scan, connect, authenticate,
// stream, and reconnect with backoff until ctx is cancelled.
func (c *Client) Run(ctx context.Context) {
	adapter := bluetooth.DefaultAdapter
	if err := adapter.Enable(); err != nil {
		c.logf("efble: enable adapter: %v", err)
		return
	}
	for {
		if ctx.Err() != nil {
			return
		}
		err := c.connectAndStream(ctx, adapter)
		if err != nil && ctx.Err() == nil {
			c.logf("efble: session ended: %v", err)
		}
		c.setConnected(false)
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

func (c *Client) connectAndStream(ctx context.Context, adapter *bluetooth.Adapter) error {
	serial, addr, err := c.scan(ctx, adapter)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.serial = serial
	c.mu.Unlock()

	device, err := adapter.Connect(addr, bluetooth.ConnectionParams{})
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer device.Disconnect()

	services, err := device.DiscoverServices(nil)
	if err != nil {
		return fmt.Errorf("discover services: %w", err)
	}
	var notifyChar, writeChar *bluetooth.DeviceCharacteristic
	for _, svc := range services {
		chars, err := svc.DiscoverCharacteristics(nil)
		if err != nil {
			continue
		}
		for i := range chars {
			u := chars[i].UUID()
			for _, nu := range notifyUUIDs {
				if u == nu {
					notifyChar = &chars[i]
				}
			}
			for _, wu := range writeUUIDs {
				if u == wu {
					writeChar = &chars[i]
				}
			}
		}
	}
	if notifyChar == nil || writeChar == nil {
		return errors.New("EcoFlow GATT characteristics not found")
	}

	s := &session{
		client: c,
		serial: serial,
		fx:     &frameExtractor{},
		inbox:  make(chan []byte, 64),
		raw:    newRawState(),
		write: func(b []byte) error {
			_, err := writeChar.WriteWithoutResponse(b)
			return err
		},
	}
	if err := notifyChar.EnableNotifications(func(buf []byte) {
		cp := make([]byte, len(buf))
		copy(cp, buf)
		select {
		case s.inbox <- cp:
		default: // drop under backpressure rather than block BlueZ
		}
	}); err != nil {
		return fmt.Errorf("enable notifications: %w", err)
	}
	c.setConnected(true)
	return s.run(ctx)
}

// scan waits for the target device to advertise and extracts its
// serial number from the manufacturer data.
func (c *Client) scan(ctx context.Context, adapter *bluetooth.Adapter) (string, bluetooth.Address, error) {
	type found struct {
		serial string
		addr   bluetooth.Address
	}
	ch := make(chan found, 1)
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		_ = adapter.Scan(func(a *bluetooth.Adapter, r bluetooth.ScanResult) {
			if !strings.EqualFold(r.Address.String(), c.Address) {
				return
			}
			var data []byte
			for _, el := range r.AdvertisementPayload.ManufacturerData() {
				if el.CompanyID == manufacturerKey {
					data = el.Data
					break
				}
			}
			if len(data) < 17 {
				return
			}
			serial := strings.TrimRight(string(data[1:17]), "\x00")
			select {
			case ch <- found{serial, r.Address}:
			default:
			}
		})
	}()
	select {
	case f := <-ch:
		adapter.StopScan()
		<-scanDone
		return f.serial, f.addr, nil
	case <-ctx.Done():
		adapter.StopScan()
		return "", bluetooth.Address{}, ctx.Err()
	case <-time.After(60 * time.Second):
		adapter.StopScan()
		return "", bluetooth.Address{}, errors.New("device not found in scan")
	}
}

// session drives one connection: handshake, auth, streaming.
type session struct {
	client *Client
	serial string
	fx     *frameExtractor
	inbox  chan []byte
	write  func([]byte) error

	enc          *type7Encryption // current encryption (post-ECDH, then session key)
	sessionReady bool
	raw          *rawState
	pending      []encFrame // extracted frames not yet consumed
	lastTimeSync time.Time
}

const stageTimeout = 20 * time.Second

func (s *session) run(ctx context.Context) error {
	if err := s.exchangeKeys(ctx); err != nil {
		return fmt.Errorf("key exchange: %w", err)
	}
	if err := s.deriveSessionKey(ctx); err != nil {
		return fmt.Errorf("session key: %w", err)
	}
	if err := s.authStatus(ctx); err != nil {
		return fmt.Errorf("auth status: %w", err)
	}
	if err := s.authenticate(ctx); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	s.client.logf("efble: authenticated, streaming")
	return s.stream(ctx)
}

// nextFrame returns the next complete EncPacket frame, feeding the
// extractor from the notification inbox until the deadline.
func (s *session) nextFrame(ctx context.Context, deadline time.Time) (encFrame, error) {
	for {
		if len(s.pending) > 0 {
			f := s.pending[0]
			s.pending = s.pending[1:]
			return f, nil
		}
		remain := time.Until(deadline)
		if remain <= 0 {
			return encFrame{}, errors.New("timeout waiting for frame")
		}
		select {
		case <-ctx.Done():
			return encFrame{}, ctx.Err()
		case chunk := <-s.inbox:
			s.pending = append(s.pending, s.fx.feed(chunk)...)
		case <-time.After(remain):
			return encFrame{}, errors.New("timeout waiting for frame")
		}
	}
}

// nextPacket returns the next decrypted protocol Packet, skipping
// command frames and (pre-session) undecryptable frames, and
// processing nothing else. Data packets that arrive while a stage
// is waiting are handed to the data path.
func (s *session) nextPacket(ctx context.Context, deadline time.Time) (*Packet, error) {
	for {
		f, err := s.nextFrame(ctx, deadline)
		if err != nil {
			return nil, err
		}
		if f.frameType == frameTypeCommand {
			continue
		}
		if !s.sessionReady || s.enc == nil {
			continue // cannot decode yet (mirrors _parse_enc_packets)
		}
		plain := s.enc.decrypt(f.payload)
		pkt, err := parsePacket(plain, true)
		if err != nil {
			continue // undecryptable/corrupt frame
		}
		return pkt, nil
	}
}

// readSimpleReply waits for a handshake (command frame) payload of
// the given type and minimum length, like _read_simple_reply.
func (s *session) readSimpleReply(ctx context.Context, wantType byte, minLen int) ([]byte, error) {
	deadline := time.Now().Add(stageTimeout)
	for {
		f, err := s.nextFrame(ctx, deadline)
		if err != nil {
			return nil, err
		}
		if f.frameType != frameTypeCommand {
			continue
		}
		payload := f.payload
		if len(payload) >= 1 && payload[0] == wantType && len(payload) >= minLen {
			return payload, nil
		}
	}
}

func (s *session) exchangeKeys(ctx context.Context) error {
	kp, err := generateKeyPair()
	if err != nil {
		return err
	}
	req := append([]byte{0x01, 0x00}, kp.publicBytes()...)
	if err := s.write(marshalSimpleFrame(req)); err != nil {
		return err
	}
	data, err := s.readSimpleReply(ctx, 0x01, 43)
	if err != nil {
		return err
	}
	keySize := ecdhTypeSize(data[2])
	if len(data) < keySize+3 {
		return fmt.Errorf("pubkey reply too short: %d", len(data))
	}
	peer, err := parsePublicBytesFixed(data[3 : 3+keySize])
	if err != nil {
		return err
	}
	shared, err := kp.sharedSecret(peer)
	if err != nil {
		return err
	}
	iv := md5.Sum(shared)
	s.enc = &type7Encryption{key: shared[:16], iv: iv[:]}
	return nil
}

func ecdhTypeSize(curveNum byte) int {
	switch curveNum {
	case 1:
		return 52
	case 2:
		return 56
	case 3, 4:
		return 64
	default:
		return 40
	}
}

// parsePublicBytesFixed accepts the device public key encoding: the
// reference reads ecdh_type_size bytes as the key string; for
// SECP160r1 that is the 40-byte X||Y form. Larger sizes belong to
// bigger curves this port does not use; the DELTA 3 Plus answers
// with the default (40).
func parsePublicBytesFixed(b []byte) (ecPoint, error) {
	if len(b) != 2*secp160r1Size {
		return ecPoint{}, fmt.Errorf("unsupported device key size %d", len(b))
	}
	return parsePublicBytes(b)
}

func (s *session) deriveSessionKey(ctx context.Context) error {
	if err := s.write(marshalSimpleFrame([]byte{0x02})); err != nil {
		return err
	}
	reply, err := s.readSimpleReply(ctx, 0x02, 33)
	if err != nil {
		return err
	}
	data := s.enc.decrypt(reply[1:])
	if len(data) < 18 {
		return fmt.Errorf("key info too short: %d", len(data))
	}
	srand, seed := data[:16], data[16:18]
	sessionKey := genSessionKey(seed, srand)
	s.enc = &type7Encryption{key: sessionKey, iv: s.enc.iv}
	s.sessionReady = true
	return nil
}

func (s *session) sendPacket(p *Packet) error {
	return s.write(marshalProtocolFrame(p, s.enc))
}

func (s *session) authStatus(ctx context.Context) error {
	pkt := &Packet{Src: 0x21, Dst: 0x35, CmdSet: 0x35, CmdID: 0x89,
		DSrc: 0x01, DDst: 0x01, Version: 0x03}
	if err := s.sendPacket(pkt); err != nil {
		return err
	}
	deadline := time.Now().Add(stageTimeout)
	for {
		p, err := s.nextPacket(ctx, deadline)
		if err != nil {
			return err
		}
		if p.Src == 0x35 && p.CmdSet == 0x35 {
			return nil
		}
		s.handleDataPacket(ctx, p)
	}
}

func (s *session) authenticate(ctx context.Context) error {
	pkt := &Packet{Src: 0x21, Dst: 0x35, CmdSet: 0x35, CmdID: 0x86,
		Payload: authToken(s.client.UserID, s.serial),
		DSrc:    0x01, DDst: 0x01, Version: 0x03}
	if err := s.sendPacket(pkt); err != nil {
		return err
	}
	deadline := time.Now().Add(stageTimeout)
	for {
		p, err := s.nextPacket(ctx, deadline)
		if err != nil {
			return err
		}
		isAuthReply := p.Src == 0x35 && p.CmdSet == 0x35 && p.CmdID == 0x86
		if isAuthReply {
			if len(p.Payload) == 1 && p.Payload[0] != 0x00 {
				return fmt.Errorf("authentication failed, device response 0x%02x", p.Payload[0])
			}
			return nil
		}
		if p.Src == 0x35 && p.CmdSet == 0x35 {
			continue // other auth-exchange packets don't confirm auth
		}
		// First real data packet also confirms authentication.
		s.handleDataPacket(ctx, p)
		return nil
	}
}

func (s *session) stream(ctx context.Context) error {
	for {
		// Telemetry arrives every ~2s; treat 45s of silence as a
		// dead session and let the supervisor reconnect.
		p, err := s.nextPacket(ctx, time.Now().Add(45*time.Second))
		if err != nil {
			return err
		}
		s.handleDataPacket(ctx, p)
	}
}

// handleDataPacket routes one decrypted packet: telemetry merges
// into state, time-sync requests get the reference replies, and
// anything else is ignored (read-only port).
func (s *session) handleDataPacket(ctx context.Context, p *Packet) {
	switch {
	case p.Src == 0x02 && p.CmdSet == 0xFE && p.CmdID == 0x15:
		s.raw.merge(p.Payload)
		if t, ok := s.raw.snapshot(); ok {
			s.client.update(t)
		}
	case p.Src == 0x35 && p.CmdSet == 0x01 && p.CmdID == 0x53 && len(p.Payload) == 0:
		s.answerTimeSync()
	}
}

// answerTimeSync mirrors TimeCommands.async_send_all (30s throttle):
// a UTC-time protobuf packet and an RTC response packet.
func (s *session) answerTimeSync() {
	now := time.Now()
	if !s.lastTimeSync.IsZero() && now.Sub(s.lastTimeSync) < 30*time.Second {
		return
	}
	s.lastTimeSync = now

	// SysUTCSync { sys_utc_time = 1 (uint32) } as protobuf varint field.
	utcPayload := append([]byte{0x08}, varintBytes(uint64(now.Unix()))...)
	_ = s.sendPacket(&Packet{Src: 0x21, Dst: 0x0B, CmdSet: 0x01, CmdID: 0x55,
		Payload: utcPayload, DSrc: 0x01, DDst: 0x01, Version: 0x13})

	_, offset := now.Zone()
	tzHours := offset / 3600
	tzMinPart := (offset - tzHours*3600) / 36 // matches the reference's (frac * 100) quirk
	payload := make([]byte, 0, 6)
	var tb [4]byte
	tb[0] = byte(now.Unix())
	tb[1] = byte(now.Unix() >> 8)
	tb[2] = byte(now.Unix() >> 16)
	tb[3] = byte(now.Unix() >> 24)
	payload = append(payload, tb[:]...)
	payload = append(payload, byte(int8(tzHours)), byte(int8(tzMinPart)))
	_ = s.sendPacket(&Packet{Src: 0x21, Dst: 0x35, CmdSet: 0x01, CmdID: 0x53,
		Payload: payload, DSrc: 0x01, DDst: 0x01, Version: 0x03})
}

func varintBytes(v uint64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7F)
		v >>= 7
		if v == 0 {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}
