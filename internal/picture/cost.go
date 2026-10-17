package picture

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
)

// decodeCost is at most what decoding b allocates, worked out from its
// header before decoding. The decoders keep more than the image they
// return, and more than DecodeConfig's colour model says.
func decodeCost(format string, b []byte, cfg image.Config) int64 {
	px := int64(cfg.Width) * int64(cfg.Height)
	switch format {
	case "jpeg":
		return jpegCost(b, cfg)
	case "webp":
		switch cfg.ColorModel {
		case color.NRGBAModel:
			// Lossless: packed palette indices expand into a second copy.
			return 6 * px
		case color.NYCbCrAModel:
			// The alpha plane is decoded through a lossless image of its own.
			return 9 * px
		}
		return 3 * px
	}
	// PNG: a tRNS chunk turns grey and truecolour into NRGBA, and each Adam7
	// pass is decoded on its own before it is copied in.
	n := int64(4)
	switch m := cfg.ColorModel; m {
	case color.Gray16Model, color.RGBA64Model, color.NRGBA64Model:
		n = 8
	default:
		if _, ok := m.(color.Palette); ok {
			n = 1
		}
	}
	if len(b) > 28 && b[28] == 1 { // IHDR's interlace method
		n *= 2
	}
	return n * px
}

// jpegCost is at most what image/jpeg allocates for b: the planes it decodes
// into, a byte a sample as if nothing were subsampled; the copy it converts
// CMYK and RGB into; and for a progressive frame a 4-byte coefficient for
// every sample, all kept until the last scan.
func jpegCost(b []byte, cfg image.Config) int64 {
	f, ok := readJPEGFrame(b)
	if !ok {
		// image/jpeg found a frame this walk didn't: assume the worst.
		n := 3
		switch cfg.ColorModel {
		case color.GrayModel:
			n = 1
		case color.CMYKModel:
			n = 4
		}
		f = jpegFrame{progressive: true}
		for range n {
			f.sampling = append(f.sampling, [2]int{1, 1})
		}
	}
	maxH, maxV, blocks := 1, 1, 0
	for _, s := range f.sampling {
		maxH, maxV = max(maxH, s[0]), max(maxV, s[1])
		blocks += s[0] * s[1]
	}
	mcuX := int64((cfg.Width + 8*maxH - 1) / (8 * maxH))
	mcuY := int64((cfg.Height + 8*maxV - 1) / (8 * maxV))
	cost := int64(len(f.sampling)) * 64 * int64(maxH*maxV) * mcuX * mcuY
	// DecodeConfig stops at the first scan, but an Adobe marker after it
	// still turns a colour JPEG into RGB.
	rgb := cfg.ColorModel == color.RGBAModel ||
		len(f.sampling) == 3 && (!ok || bytes.Contains(b[f.end:], []byte{0xff, 0xee}))
	if rgb || len(f.sampling) == 4 {
		cost += 4 * int64(cfg.Width) * int64(cfg.Height)
	}
	if f.progressive {
		cost += 4 * 64 * int64(blocks) * mcuX * mcuY
	}
	return cost
}

type jpegFrame struct {
	progressive bool
	sampling    [][2]int // each component's horizontal and vertical factor
	end         int      // the offset just past the frame header
}

// readJPEGFrame finds the frame header image/jpeg decodes by, stepping over
// stray and fill bytes between segments as leniently as it does.
func readJPEGFrame(b []byte) (jpegFrame, bool) {
	for i := 2; i < len(b); {
		if b[i] != 0xff {
			i++
			continue
		}
		for i < len(b) && b[i] == 0xff {
			i++
		}
		if i+3 > len(b) {
			return jpegFrame{}, false
		}
		marker := b[i]
		i++
		switch {
		case marker == 0x00 || marker >= 0xd0 && marker <= 0xd7:
			continue
		case marker == 0xd9 || marker == 0xda: // the end, or a scan before any frame
			return jpegFrame{}, false
		}
		n := int(binary.BigEndian.Uint16(b[i:]))
		if n < 2 || i+n > len(b) {
			return jpegFrame{}, false
		}
		seg := b[i+2 : i+n]
		i += n
		if marker < 0xc0 || marker > 0xc2 {
			continue
		}
		if len(seg) < 6 || len(seg) < 6+3*int(seg[5]) {
			return jpegFrame{}, false
		}
		f := jpegFrame{progressive: marker == 0xc2, end: i}
		for k := range int(seg[5]) {
			h, v := int(seg[7+3*k]>>4), int(seg[7+3*k]&0x0f)
			if seg[5] == 1 { // a lone component's factors are ignored
				h, v = 1, 1
			}
			if h < 1 || h > 4 || v < 1 || v > 4 {
				return jpegFrame{}, false
			}
			f.sampling = append(f.sampling, [2]int{h, v})
		}
		return f, len(f.sampling) > 0
	}
	return jpegFrame{}, false
}
