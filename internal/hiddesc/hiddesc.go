// Package hiddesc is a small USB HID report-descriptor parser.
//
// It exists because the EcoFlow DELTA 3 Plus / River 3 Plus HID report
// descriptor is malformed: it is missing three End Collection (0xC0)
// bytes, so the Linux kernel hid-generic driver rejects the device
// ("unbalanced collection at end of report description", probe error
// -22) and no /dev/hidraw* node ever appears. A userland reader must
// therefore fetch the descriptor over libusb, repair it in memory, and
// parse it itself — which is what this package does.
package hiddesc

import (
	"fmt"
)

// Kind is the type of a HID main item.
type Kind uint8

const (
	Input Kind = iota
	Output
	Feature
)

func (k Kind) String() string {
	switch k {
	case Input:
		return "input"
	case Output:
		return "output"
	case Feature:
		return "feature"
	}
	return "unknown"
}

// Field is a single data element inside one report.
type Field struct {
	ReportID   uint8
	Kind       Kind
	UsagePage  uint16
	Usage      uint16 // usage ID within UsagePage
	BitOffset  int    // offset within the report payload (report ID byte excluded)
	BitSize    int
	LogicalMin int64
	LogicalMax int64
	Unit       uint32
	UnitExp    int // signed HID unit exponent
	Constant   bool
	// Collection is the chain of collection usages enclosing this field,
	// outermost first, encoded as page<<16|usage.
	Collection []uint32
}

// UsageCode packs a usage page and usage ID the way NUT's usage table does.
func (f Field) UsageCode() uint32 {
	return uint32(f.UsagePage)<<16 | uint32(f.Usage)
}

// Descriptor is a parsed report descriptor.
type Descriptor struct {
	Fields []Field
	// RepairedCollections is how many missing End Collection bytes had
	// to be appended to make the descriptor parse (3 on EcoFlow units).
	RepairedCollections int
}

// Repair appends the End Collection bytes that are missing from the end
// of a report descriptor and reports how many were added. Descriptors
// that are already balanced are returned unchanged.
func Repair(data []byte) ([]byte, int) {
	open := 0
	for i := 0; i < len(data); {
		b := data[i]
		if b == 0xfe { // long item: 0xFE, size, tag, data...
			if i+1 >= len(data) {
				break
			}
			i += 2 + int(data[i+1])
			continue
		}
		size := int(b & 0x03)
		if size == 3 {
			size = 4
		}
		typ := (b >> 2) & 0x03 // 0 = main item
		tag := (b >> 4) & 0x0f
		if typ == 0 { // main
			switch tag {
			case 0x0a: // Collection
				open++
			case 0x0c: // End Collection
				open--
			}
		}
		i += 1 + size
	}
	if open <= 0 {
		return data, 0
	}
	out := make([]byte, len(data), len(data)+open)
	copy(out, data)
	for j := 0; j < open; j++ {
		out = append(out, 0xc0)
	}
	return out, open
}

type globalState struct {
	usagePage  uint16
	logicalMin int64
	logicalMax int64
	unit       uint32
	unitExp    int
	reportSize int
	reportID   uint8
	reportCnt  int
}

func signExtend(v uint64, size int) int64 {
	if size == 0 {
		return 0
	}
	shift := 64 - uint(size*8)
	return int64(v<<shift) >> shift
}

// Parse repairs (if needed) and parses a HID report descriptor.
func Parse(data []byte) (*Descriptor, error) {
	data, repaired := Repair(data)
	desc := &Descriptor{RepairedCollections: repaired}

	var g globalState
	var saved []globalState
	var usages []uint32 // packed page<<16|usage, local state
	var usageMin, usageMax *uint32
	var collections []uint32
	offsets := map[[2]uint8]int{} // (reportID, kind) -> next bit offset

	itemVal := func(payload []byte) uint64 {
		var v uint64
		for i, b := range payload {
			v |= uint64(b) << (8 * i)
		}
		return v
	}

	for i := 0; i < len(data); {
		b := data[i]
		if b == 0xfe {
			if i+1 >= len(data) {
				return nil, fmt.Errorf("truncated long item at %d", i)
			}
			i += 2 + int(data[i+1])
			continue
		}
		size := int(b & 0x03)
		if size == 3 {
			size = 4
		}
		if i+1+size > len(data) {
			return nil, fmt.Errorf("truncated item at offset %d", i)
		}
		typ := (b >> 2) & 0x03
		tag := (b >> 4) & 0x0f
		payload := data[i+1 : i+1+size]
		val := itemVal(payload)
		i += 1 + size

		switch typ {
		case 1: // global
			switch tag {
			case 0x0: // Usage Page
				g.usagePage = uint16(val)
			case 0x1: // Logical Minimum
				g.logicalMin = signExtend(val, size)
			case 0x2: // Logical Maximum
				g.logicalMax = signExtend(val, size)
			case 0x6: // Unit
				g.unit = uint32(val)
			case 0x5: // Unit Exponent (4-bit signed)
				e := int(val & 0x0f)
				if e >= 8 {
					e -= 16
				}
				g.unitExp = e
			case 0x7: // Report Size
				g.reportSize = int(val)
			case 0x8: // Report ID
				g.reportID = uint8(val)
			case 0x9: // Report Count
				g.reportCnt = int(val)
			case 0xA: // Push
				saved = append(saved, g)
			case 0xB: // Pop
				if len(saved) == 0 {
					return nil, fmt.Errorf("pop with empty stack")
				}
				g = saved[len(saved)-1]
				saved = saved[:len(saved)-1]
			}
		case 2: // local
			switch tag {
			case 0x0: // Usage
				usages = append(usages, uint32(g.usagePage)<<16|uint32(uint16(val)))
			case 0x1: // Usage Minimum
				v := uint32(g.usagePage)<<16 | uint32(uint16(val))
				usageMin = &v
			case 0x2: // Usage Maximum
				v := uint32(g.usagePage)<<16 | uint32(uint16(val))
				usageMax = &v
			}
		case 0: // main
			switch tag {
			case 0x8, 0x9, 0xB: // Input, Output, Feature
				kind := Feature
				if tag == 0x8 {
					kind = Input
				} else if tag == 0x9 {
					kind = Output
				}
				// Expand Usage Minimum/Maximum into the usage list.
				list := usages
				if usageMin != nil && usageMax != nil {
					list = nil
					for u := *usageMin; u <= *usageMax; u++ {
						list = append(list, u)
					}
				}
				key := [2]uint8{g.reportID, uint8(kind)}
				off := offsets[key]
				for n := 0; n < g.reportCnt; n++ {
					var code uint32
					switch {
					case n < len(list):
						code = list[n]
					case len(list) > 0:
						code = list[len(list)-1]
					}
					f := Field{
						ReportID:   g.reportID,
						Kind:       kind,
						UsagePage:  uint16(code >> 16),
						Usage:      uint16(code),
						BitOffset:  off + n*g.reportSize,
						BitSize:    g.reportSize,
						LogicalMin: g.logicalMin,
						LogicalMax: g.logicalMax,
						Unit:       g.unit,
						UnitExp:    g.unitExp,
						Constant:   payload[0]&0x01 != 0,
						Collection: append([]uint32(nil), collections...),
					}
					desc.Fields = append(desc.Fields, f)
				}
				offsets[key] = off + g.reportCnt*g.reportSize
			case 0xA: // Collection
				var code uint32
				if len(usages) > 0 {
					code = usages[len(usages)-1]
				}
				collections = append(collections, code)
			case 0xC: // End Collection
				if len(collections) > 0 {
					collections = collections[:len(collections)-1]
				}
			}
			// Local state is consumed by every main item.
			usages = nil
			usageMin, usageMax = nil, nil
		}
	}
	return desc, nil
}

// FeatureFields returns the Feature fields of one report, in offset order.
func (d *Descriptor) FeatureFields(reportID uint8) []Field {
	var out []Field
	for _, f := range d.Fields {
		if f.Kind == Feature && f.ReportID == reportID {
			out = append(out, f)
		}
	}
	return out
}

// FeatureReportIDs returns the sorted distinct report IDs that carry
// Feature fields.
func (d *Descriptor) FeatureReportIDs() []uint8 {
	seen := map[uint8]bool{}
	var ids []uint8
	for _, f := range d.Fields {
		if f.Kind == Feature && !seen[f.ReportID] {
			seen[f.ReportID] = true
			ids = append(ids, f.ReportID)
		}
	}
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			if ids[j] < ids[i] {
				ids[i], ids[j] = ids[j], ids[i]
			}
		}
	}
	return ids
}

// ReportLength returns the total byte length (including the report ID
// byte) needed to hold a report of the given kind.
func (d *Descriptor) ReportLength(reportID uint8, kind Kind) int {
	bits := 0
	for _, f := range d.Fields {
		if f.ReportID == reportID && f.Kind == kind {
			if end := f.BitOffset + f.BitSize; end > bits {
				bits = end
			}
		}
	}
	if bits == 0 {
		return 0
	}
	return 1 + (bits+7)/8
}

// Extract reads a field's value out of a report payload. The payload is
// the report without its leading report-ID byte. Multi-byte values are
// little-endian bit order, per the HID specification; values are sign
// extended when the field's logical minimum is negative.
func Extract(payload []byte, f Field) int64 {
	var v uint64
	for i := 0; i < f.BitSize; i++ {
		bit := f.BitOffset + i
		byteIdx := bit / 8
		if byteIdx >= len(payload) {
			break
		}
		if payload[byteIdx]>>(uint(bit)%8)&1 != 0 {
			v |= 1 << uint(i)
		}
	}
	if f.LogicalMin < 0 && f.BitSize > 0 && f.BitSize < 64 {
		shift := 64 - uint(f.BitSize)
		return int64(v<<shift) >> shift
	}
	return int64(v)
}
