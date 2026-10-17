package picture

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

var (
	red   = color.RGBA{0xff, 0, 0, 0xff}
	blue  = color.RGBA{0, 0, 0xff, 0xff}
	green = color.RGBA{0, 0xff, 0, 0xff}
)

// bands is a w×h picture split into vertical (or horizontal) bands of cs.
func bands(w, h int, vertical bool, cs ...color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			i := x * len(cs) / w
			if !vertical {
				i = y * len(cs) / h
			}
			img.SetRGBA(x, y, cs[i])
		}
	}
	return img
}

func pngOf(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func jpegOf(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// near reports whether c is within 24 of want per channel (scaling and
// JPEG blur edges a little).
func near(c color.Color, want color.RGBA) bool {
	r, g, b, _ := c.RGBA()
	d := func(x uint32, y uint8) bool { v := int(x>>8) - int(y); return v < 24 && v > -24 }
	return d(r, want.R) && d(g, want.G) && d(b, want.B)
}

func decodePNG(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if got := img.Bounds(); got.Dx() != Size || got.Dy() != Size {
		t.Fatalf("picture is %v, want %d px square", got, Size)
	}
	return img
}

func TestNormalizeCropsTheCentreSquare(t *testing.T) {
	img, err := normalize(pngOf(t, bands(600, 200, true, red, blue, green)))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []image.Point{{20, 512}, {512, 512}, {1000, 512}, {512, 20}, {512, 1000}} {
		if c := img.At(p.X, p.Y); !near(c, blue) {
			t.Fatalf("at %v: %v, want the middle band", p, c)
		}
	}
	tall, err := normalize(jpegOf(t, bands(200, 600, false, red, blue, green)))
	if err != nil {
		t.Fatal(err)
	}
	if c := tall.At(512, 512); !near(c, blue) {
		t.Fatalf("tall centre = %v", c)
	}
}

// A large picture is halved before the final scaling; the crop still holds.
func TestNormalizeShrinksLargePictures(t *testing.T) {
	img := bands(6000, 2400, true, red)
	for y := range 2400 {
		for x := 1800; x < 4200; x++ {
			img.SetRGBA(x, y, blue)
		}
	}
	out, err := normalize(jpegOf(t, img))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []image.Point{{8, 8}, {512, 512}, {1015, 1015}, {8, 1015}} {
		if c := out.At(p.X, p.Y); !near(c, blue) {
			t.Fatalf("at %v: %v", p, c)
		}
	}
}

func TestNormalizeFlattensTransparencyOntoWhite(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 200, 200))
	for x := range 100 {
		for y := range 200 {
			img.SetNRGBA(x, y, color.NRGBA{0, 0, 0xff, 0xff})
		}
	}
	out, err := normalize(pngOf(t, img))
	if err != nil {
		t.Fatal(err)
	}
	if !out.Opaque() || !near(out.At(1000, 512), color.RGBA{0xff, 0xff, 0xff, 0xff}) || !near(out.At(20, 512), blue) {
		t.Fatalf("opaque=%v right=%v left=%v", out.Opaque(), out.At(1000, 512), out.At(20, 512))
	}
}

func TestNormalizeReadsWebP(t *testing.T) {
	img, err := normalize(solidWebP(300, 200, color.NRGBA{0x2a, 0x6a, 0x54, 0xff}))
	if err != nil {
		t.Fatal(err)
	}
	if c := img.At(512, 512); !near(c, brandGreen) {
		t.Fatalf("webp centre = %v", c)
	}
}

func TestNormalizeTurnsPhotosUpright(t *testing.T) {
	// Stored with red on top; the orientation says where that top row
	// belongs once the photo is upright.
	for o, redAt := range map[int]image.Point{1: {512, 20}, 3: {512, 1000}, 6: {1000, 512}, 8: {20, 512}} {
		raw := withOrientation(t, jpegOf(t, bands(200, 200, false, red, blue)), o)
		if got := jpegOrientation(raw); got != o {
			t.Fatalf("orientation = %d, want %d", got, o)
		}
		img, err := normalize(raw)
		if err != nil {
			t.Fatal(err)
		}
		opposite := image.Pt(Size-1-redAt.X, Size-1-redAt.Y)
		if !near(img.At(redAt.X, redAt.Y), red) || !near(img.At(opposite.X, opposite.Y), blue) {
			t.Fatalf("orientation %d: %v at %v, %v at %v", o, img.At(redAt.X, redAt.Y), redAt, img.At(opposite.X, opposite.Y), opposite)
		}
	}
}

func TestNormalizeRefuses(t *testing.T) {
	var gifBuf bytes.Buffer
	if err := gif.Encode(&gifBuf, bands(200, 200, true, red), nil); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		raw  []byte
		want error
	}{
		{"empty", nil, errNotPicture},
		{"text", []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), errNotPicture},
		{"gif", gifBuf.Bytes(), errNotPicture},
		{"truncated png", pngOf(t, bands(200, 200, true, red))[:60], errNotPicture},
		{"too small", pngOf(t, bands(300, 100, true, red)), errTooSmall},
		{"too many pixels a side", pngHeader(9000, 200, 8, 2), errTooBig},
		{"bomb", pngHeader(8000, 8000, 16, 6), errTooBig},
		{"over 5 MB", append(pngOf(t, bands(200, 200, true, red)), make([]byte, MaxUpload)...), errTooLarge},
	}
	for _, c := range cases {
		_, err := normalize(c.raw)
		if !errors.Is(err, c.want) || !errors.Is(err, core.ErrInvalid) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
}

func TestStoreKeepsTheAdminsPicture(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := New(dir)
	p, err := s.Current(ctx)
	if err != nil || p.Custom || p.Version != Default().Version || !bytes.Equal(p.PNG, defaultPNG) {
		t.Fatalf("fresh = %+v, %v", p.Version, err)
	}
	set, err := s.Set(ctx, jpegOf(t, bands(400, 400, true, blue)))
	if err != nil {
		t.Fatal(err)
	}
	if !set.Custom || set.Version == Default().Version || !near(decodePNG(t, set.PNG).At(512, 512), blue) {
		t.Fatalf("set = %+v", set.Version)
	}
	if j, err := jpeg.Decode(bytes.NewReader(set.JPEG)); err != nil || !near(j.At(512, 512), blue) {
		t.Fatalf("jpeg: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("stat = %v, %v", fi, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
	// A new process reads the same picture back.
	again, err := New(dir).Current(ctx)
	if err != nil || !again.Custom || again.Version != set.Version || !bytes.Equal(again.PNG, set.PNG) || len(again.JPEG) == 0 {
		t.Fatalf("again = %+v, %v", again.Version, err)
	}
	if _, err := s.Set(ctx, []byte("not a picture")); !errors.Is(err, errNotPicture) {
		t.Fatalf("bad upload: %v", err)
	}
	if cur, _ := s.Current(ctx); cur.Version != set.Version {
		t.Fatal("a refused upload replaced the picture")
	}
	reset, err := s.Reset(ctx)
	if err != nil || reset.Custom || reset.Version != Default().Version {
		t.Fatalf("reset = %+v, %v", reset, err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !os.IsNotExist(err) {
		t.Fatalf("picture.png after reset: %v", err)
	}
	if _, err := s.Reset(ctx); err != nil {
		t.Fatalf("second reset: %v", err)
	}
}

func TestStoreChecksAFilePutThereByHand(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), pngOf(t, bands(600, 300, true, red, blue)), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := New(dir).Current(ctx)
	if err != nil || !p.Custom {
		t.Fatalf("current = %+v, %v", p, err)
	}
	decodePNG(t, p.PNG)
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir).Current(ctx); err == nil || errors.Is(err, core.ErrInvalid) {
		t.Fatalf("junk picture.png: %v", err)
	}
}

func TestStoreWithoutDataDir(t *testing.T) {
	s := New("")
	if p, err := s.Current(context.Background()); err != nil || p.Version != Default().Version {
		t.Fatalf("current = %+v, %v", p, err)
	}
	if _, err := s.Set(context.Background(), pngOf(t, bands(200, 200, true, red))); !errors.Is(err, core.ErrNotImplemented) {
		t.Fatalf("set = %v", err)
	}
}

func TestJPEGOrientationIgnoresJunk(t *testing.T) {
	for _, b := range [][]byte{nil, {0xff, 0xd8}, {0xff, 0xd8, 0xff, 0xe1, 0xff, 0xff}, {0xff, 0xd8, 0xff, 0xe1, 0, 8, 'E', 'x', 'i', 'f', 0, 0}} {
		if o := jpegOrientation(b); o != 1 {
			t.Errorf("%x: %d", b, o)
		}
	}
}

func FuzzJPEGOrientation(f *testing.F) {
	f.Add(withOrientation(f, []byte{0xff, 0xd8, 0xff, 0xd9}, 6))
	f.Add([]byte{0xff, 0xd8, 0xff, 0xe1, 0, 0x10, 'E', 'x', 'i', 'f', 0, 0, 'M', 'M', 0, 42, 0, 0, 0, 8})
	f.Fuzz(func(t *testing.T, b []byte) {
		if o := jpegOrientation(b); o < 1 || o > 8 {
			t.Fatalf("orientation %d", o)
		}
	})
}

// withOrientation puts an EXIF segment with orientation o right after SOI.
func withOrientation(tb testing.TB, j []byte, o int) []byte {
	tb.Helper()
	tiff := []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0}
	entry := make([]byte, 12)
	binary.LittleEndian.PutUint16(entry[0:], 0x0112)
	binary.LittleEndian.PutUint16(entry[2:], 3)
	binary.LittleEndian.PutUint32(entry[4:], 1)
	binary.LittleEndian.PutUint16(entry[8:], uint16(o))
	tiff = append(append(tiff, entry...), 0, 0, 0, 0)
	seg := append([]byte("Exif\x00\x00"), tiff...)
	app1 := []byte{0xff, 0xe1, byte((len(seg) + 2) >> 8), byte(len(seg) + 2)}
	out := append([]byte{}, j[:2]...)
	out = append(append(out, app1...), seg...)
	return append(out, j[2:]...)
}

// pngHeader is a PNG whose IHDR declares w×h at depth and colour type but
// carries no image data: decoding the config succeeds, decoding fails.
func pngHeader(w, h int, depth, colorType byte) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
	ihdr[8], ihdr[9] = depth, colorType
	chunk := append([]byte("IHDR"), ihdr...)
	out := []byte("\x89PNG\r\n\x1a\n")
	out = binary.BigEndian.AppendUint32(out, 13)
	out = append(out, chunk...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(chunk))
}

// solidWebP is a lossless WebP of one colour: every prefix code has a
// single symbol, so the pixels themselves take no bits.
func solidWebP(w, h int, c color.NRGBA) []byte {
	var bw bitWriter
	bw.write(0x2f, 8)
	bw.write(uint32(w-1), 14)
	bw.write(uint32(h-1), 14)
	bw.write(0, 1) // alpha unused
	bw.write(0, 3) // version
	bw.write(0, 1) // no transform
	bw.write(0, 1) // no colour cache
	bw.write(0, 1) // no meta prefix codes
	for _, sym := range []uint8{c.G, c.R, c.B, c.A, 0} {
		bw.write(1, 1) // simple code
		bw.write(0, 1) // one symbol
		bw.write(1, 1) // eight bits
		bw.write(uint32(sym), 8)
	}
	data := bw.bytes()
	if len(data)%2 == 1 {
		data = append(data, 0)
	}
	out := []byte("RIFF")
	out = binary.LittleEndian.AppendUint32(out, uint32(4+8+len(data)))
	out = append(out, "WEBPVP8L"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(data)))
	return append(out, data...)
}

type bitWriter struct {
	buf  []byte
	acc  uint64
	bits uint
}

func (b *bitWriter) write(v uint32, n uint) {
	b.acc |= uint64(v) << b.bits
	b.bits += n
	for b.bits >= 8 {
		b.buf = append(b.buf, byte(b.acc))
		b.acc >>= 8
		b.bits -= 8
	}
}

func (b *bitWriter) bytes() []byte {
	if b.bits > 0 {
		return append(b.buf, byte(b.acc))
	}
	return b.buf
}
