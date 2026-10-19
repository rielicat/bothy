package picture

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "re-encode default.png and write default.jpg from it (make icons)")

// The picture's source; make icons records its hash in default.sum.
const source = "../../web/public/icon-maskable.svg"

// TestDefaultPicture checks the committed default: rendered from the current
// icon-maskable.svg, 1024 px and opaque, the JPEG Telegram gets matching the
// PNG, and nothing but the dark tile within 32 px of a circle crop's edge, so
// Telegram's and Discord's circles show the whole dog. With -update it
// re-encodes the PNG make icons rendered with Bothy's encoders and writes
// default.jpg from it.
func TestDefaultPicture(t *testing.T) {
	if *update {
		b, err := os.ReadFile("default.png")
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		p, err := encodePNG(img)
		if err != nil {
			t.Fatal(err)
		}
		j, err := encodeJPEG(img)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("default.png", p, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("default.jpg", j, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Skip("re-encoded default.png and wrote default.jpg; rebuild to embed them")
	}
	svg, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := os.ReadFile("default.sum")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(svg)
	if f := strings.Fields(string(rec)); len(f) == 0 || f[0] != hex.EncodeToString(sum[:]) {
		t.Fatalf("%s changed since default.png was rendered from it; run make icons", source)
	}
	got, err := png.Decode(bytes.NewReader(defaultPNG))
	if err != nil {
		t.Fatal(err)
	}
	if b := got.Bounds(); b.Dx() != Size || b.Dy() != Size {
		t.Fatalf("default.png is %v", b)
	}
	if o, ok := got.(interface{ Opaque() bool }); !ok || !o.Opaque() {
		t.Fatal("default.png has transparent pixels; chat apps would show them black or white")
	}
	gotJ, err := jpeg.Decode(bytes.NewReader(defaultJPEG))
	if err != nil {
		t.Fatal(err)
	}
	if mean := meanDiff(gotJ, got); mean > 2 {
		t.Fatalf("default.jpg differs from default.png by %.2f per channel on average; run make icons", mean)
	}
	for y := range Size {
		for x := range Size {
			if math.Hypot(float64(x)+0.5-Size/2, float64(y)+0.5-Size/2) <= Size/2-32 {
				continue
			}
			r, g, b, _ := got.At(x, y).RGBA()
			if max(r, g, b)>>8 > 0x40 {
				t.Fatalf("ink at (%d, %d), outside what a circle crop shows", x, y)
			}
		}
	}
}

func meanDiff(a, b image.Image) float64 {
	var sum float64
	for y := range Size {
		for x := range Size {
			ar, ag, ab, _ := a.At(x, y).RGBA()
			br, bg, bb, _ := b.At(x, y).RGBA()
			sum += math.Abs(float64(ar>>8)-float64(br>>8)) + math.Abs(float64(ag>>8)-float64(bg>>8)) + math.Abs(float64(ab>>8)-float64(bb>>8))
		}
	}
	return sum / (3 * Size * Size)
}
