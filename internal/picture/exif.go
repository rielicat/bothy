package picture

import (
	"encoding/binary"
	"image"
)

// jpegOrientation reads the EXIF orientation (1–8) of a JPEG; 1 when it has
// none. Phones store photos sideways with this tag, and re-encoding drops it,
// so it is applied before.
func jpegOrientation(b []byte) int {
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xff {
			return 1
		}
		marker := b[i+1]
		if marker == 0xd8 || (marker >= 0xd0 && marker <= 0xd7) || marker == 0x01 || marker == 0xff {
			i++
			continue
		}
		if marker == 0xda || marker == 0xd9 { // image data: no metadata after it
			return 1
		}
		n := int(binary.BigEndian.Uint16(b[i+2:]))
		if n < 2 || i+2+n > len(b) {
			return 1
		}
		seg := b[i+4 : i+2+n]
		if marker == 0xe1 && len(seg) > 6 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + n
	}
	return 1
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	if bo.Uint16(t[2:]) != 42 {
		return 1
	}
	off := int(bo.Uint32(t[4:]))
	if off < 8 || off+2 > len(t) {
		return 1
	}
	count := int(bo.Uint16(t[off:]))
	for k := range count {
		e := off + 2 + 12*k
		if e+12 > len(t) {
			return 1
		}
		const tagOrientation, typeShort = 0x0112, 3
		if bo.Uint16(t[e:]) == tagOrientation && bo.Uint16(t[e+2:]) == typeShort {
			if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// orient turns a square image upright for EXIF orientation o.
func orient(src *image.RGBA, o int) *image.RGBA {
	if o <= 1 || o > 8 {
		return src
	}
	n := src.Bounds().Dx() - 1
	dst := image.NewRGBA(src.Bounds())
	for y := 0; y <= n; y++ {
		for x := 0; x <= n; x++ {
			var sx, sy int
			switch o {
			case 2:
				sx, sy = n-x, y
			case 3:
				sx, sy = n-x, n-y
			case 4:
				sx, sy = x, n-y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, n-x
			case 7:
				sx, sy = n-y, n-x
			case 8:
				sx, sy = n-y, x
			}
			si, di := src.PixOffset(sx, sy), dst.PixOffset(x, y)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
}
