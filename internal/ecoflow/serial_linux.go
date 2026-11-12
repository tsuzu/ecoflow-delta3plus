package ecoflow

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/sys/unix"
)

// ErrCDCNotFound means no CDC-ACM port for the unit was found. It is
// kept distinct so callers can treat a missing CDC interface (it is
// not yet confirmed to exist on every DELTA 3 Plus firmware) as a
// warning when HID data is available.
var ErrCDCNotFound = errors.New("no EcoFlow CDC-ACM port found")

// SerialPort is a /dev/ttyACM* port configured for 115200 8N1 raw.
type SerialPort struct {
	f       *os.File
	timeout time.Duration
}

// FindCDCPort locates the unit's CDC-ACM interface via
// /dev/serial/by-id (interface 01 of the composite device).
//
// There is deliberately no fallback to a bare /dev/ttyACM*: on a host
// with other serial devices (smart-meter adapters, modems) guessing
// the wrong tty would inject EcoFlow frames into someone else's
// device. Pass the port explicitly with -cdc-port in that case.
func FindCDCPort() (string, error) {
	for _, pattern := range []string{
		"/dev/serial/by-id/*EcoFlow*if01*",
		"/dev/serial/by-id/*EF-UPS*if01*",
	} {
		if byID, _ := filepath.Glob(pattern); len(byID) > 0 {
			sort.Strings(byID)
			return byID[0], nil
		}
	}
	return "", ErrCDCNotFound
}

// OpenSerialPort opens and configures a CDC-ACM tty.
func OpenSerialPort(path string) (*SerialPort, error) {
	f, err := os.OpenFile(path, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	fd := int(f.Fd())
	termios, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("tcgets: %w", err)
	}
	termios.Iflag = 0
	termios.Oflag = 0
	termios.Lflag = 0
	termios.Cflag = unix.B115200 | unix.CS8 | unix.CREAD | unix.CLOCAL
	for i := range termios.Cc {
		termios.Cc[i] = 0
	}
	termios.Cc[unix.VMIN] = 0
	termios.Cc[unix.VTIME] = 10 // 1s read timeout tick
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, termios); err != nil {
		f.Close()
		return nil, fmt.Errorf("tcsets: %w", err)
	}
	// Flush any stale bytes (TCFLSH, TCIOFLUSH=2).
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), unix.TCFLSH, 2); errno != 0 {
		f.Close()
		return nil, fmt.Errorf("tcflush: %v", errno)
	}
	return &SerialPort{f: f, timeout: 3 * time.Second}, nil
}

func (p *SerialPort) Write(b []byte) (int, error) { return p.f.Write(b) }
func (p *SerialPort) Close() error                { return p.f.Close() }

// ReadFull reads exactly n bytes before the port timeout expires.
// The tty is configured with VMIN=0/VTIME=10, so individual reads may
// return 0 bytes; keep going until the deadline.
func (p *SerialPort) ReadFull(n int) ([]byte, error) {
	buf := make([]byte, 0, n)
	deadline := time.Now().Add(p.timeout)
	tmp := make([]byte, n)
	for len(buf) < n {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("serial read timeout (%d/%d bytes)", len(buf), n)
		}
		m, err := p.f.Read(tmp[:n-len(buf)])
		if err != nil {
			return nil, err
		}
		buf = append(buf, tmp[:m]...)
	}
	return buf, nil
}
