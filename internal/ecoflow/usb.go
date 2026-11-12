package ecoflow

import (
	"fmt"

	"github.com/google/gousb"
)

// USBDevice is the libusb-backed ReportSource. It claims the HID
// interface directly in userland (with kernel-driver auto-detach), so
// the kernel HID parser — which rejects this device's malformed
// report descriptor — is never involved.
type USBDevice struct {
	ctx  *gousb.Context
	dev  *gousb.Device
	cfg  *gousb.Config
	intf *gousb.Interface
}

// OpenUSB opens the first EcoFlow UPS device (3746:ffff) on the bus.
func OpenUSB() (*USBDevice, error) {
	ctx := gousb.NewContext()
	devs, err := ctx.OpenDevices(func(desc *gousb.DeviceDesc) bool {
		return desc.Vendor == gousb.ID(VendorID) && desc.Product == gousb.ID(ProductID)
	})
	if err != nil {
		ctx.Close()
		return nil, fmt.Errorf("open devices: %w", err)
	}
	if len(devs) == 0 {
		ctx.Close()
		return nil, fmt.Errorf("no USB device %04x:%04x found — check the USB 2.0 data cable and that the unit is on", VendorID, ProductID)
	}
	dev := devs[0]
	for _, extra := range devs[1:] {
		extra.Close()
	}
	if err := dev.SetAutoDetach(true); err != nil {
		dev.Close()
		ctx.Close()
		return nil, fmt.Errorf("set auto-detach: %w", err)
	}
	cfg, err := dev.Config(1)
	if err != nil {
		dev.Close()
		ctx.Close()
		return nil, fmt.Errorf("get config: %w", err)
	}
	intf, err := cfg.Interface(0, 0)
	if err != nil {
		cfg.Close()
		dev.Close()
		ctx.Close()
		return nil, fmt.Errorf("claim HID interface: %w", err)
	}
	return &USBDevice{ctx: ctx, dev: dev, cfg: cfg, intf: intf}, nil
}

func (d *USBDevice) Close() error {
	if d.intf != nil {
		d.intf.Close()
	}
	if d.cfg != nil {
		d.cfg.Close()
	}
	if d.dev != nil {
		d.dev.Close()
	}
	if d.ctx != nil {
		d.ctx.Close()
	}
	return nil
}

// ReportDescriptor issues GET_DESCRIPTOR (Report) on the control
// endpoint. The raw descriptor is returned as-is; repairing the
// missing End Collection bytes is the parser's job.
func (d *USBDevice) ReportDescriptor() ([]byte, error) {
	buf := make([]byte, 4096)
	n, err := d.dev.Control(0x81, 0x06, 0x2200, 0, buf)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("empty report descriptor")
	}
	return buf[:n], nil
}

// GetFeatureReport issues GET_REPORT (Feature): bmRequestType 0xA1,
// bRequest 0x01, wValue = (Feature 0x03 << 8) | report ID.
func (d *USBDevice) GetFeatureReport(reportID uint8, length int) ([]byte, error) {
	buf := make([]byte, length)
	n, err := d.dev.Control(0xA1, 0x01, (0x03<<8)|uint16(reportID), 0, buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}
