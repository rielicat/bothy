package picture

import (
	"bytes"
	"flag"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"testing"
)

var update = flag.Bool("update", false, "rewrite default.png and default.jpg from the brand mark")

// The brand mark of web/public/icon-maskable.svg: the roofline
// M3 21 16 9.5 29 21 and the chimney M23 15.3V10 in a 32-unit box, stroked
// 3 units wide with round caps and joins, white on #2A6A54.
var (
	brandGreen = color.RGBA{0x2a, 0x6a, 0x54, 0xff}
	markLines  = [][2][2]float64{{{3, 21}, {16, 9.5}}, {{16, 9.5}, {29, 21}}, {{23, 15.3}, {23, 10}}}
)

const (
	markStroke = 3.0
	// The centre of the mark's ink (x 1.5–30.5, y 8–22.5) goes to the
	// centre of the picture.
	markCX, markCY = 16.0, 15.25
	// markScale is picture pixels per mark unit at Size. The ink then
	// reaches 61% of the way to the edge of a circle crop (the roofline's
	// ends are 15.7 units from the centre), so Telegram's and Discord's
	// circles and Slack's rounded squares all show the whole mark.
	markScale = 20.0 * Size / 1024
)

// renderDefault draws the mark as distance-field strokes: each pixel's
// coverage is how far its centre lies inside the nearest line's capsule,
// which gives round caps and joins and one pixel of anti-aliasing.
func renderDefault() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, Size, Size))
	half := markStroke * markScale / 2
	for y := range Size {
		for x := range Size {
			px := (float64(x)+0.5-Size/2)/markScale + markCX
			py := (float64(y)+0.5-Size/2)/markScale + markCY
			d := math.Inf(1)
			for _, l := range markLines {
				d = math.Min(d, segmentDistance(px, py, l[0][0], l[0][1], l[1][0], l[1][1]))
			}
			cover := math.Max(0, math.Min(1, half-d*markScale+0.5))
			mix := func(bg uint8) uint8 { return uint8(math.Round(float64(bg) + (255-float64(bg))*cover)) }
			img.SetRGBA(x, y, color.RGBA{mix(brandGreen.R), mix(brandGreen.G), mix(brandGreen.B), 0xff})
		}
	}
	return img
}

func segmentDistance(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	t := math.Max(0, math.Min(1, ((px-ax)*dx+(py-ay)*dy)/(dx*dx+dy*dy)))
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

// TestDefaultPicture checks the committed default against the brand mark,
// pixel by pixel within a step of rounding (float fusion differs between
// architectures), and rewrites both files with -update.
func TestDefaultPicture(t *testing.T) {
	want := renderDefault()
	if *update {
		p, err := encodePNG(want)
		if err != nil {
			t.Fatal(err)
		}
		j, err := encodeJPEG(want)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("default.png", p, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("default.jpg", j, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Skip("rewrote default.png and default.jpg; rebuild to embed them")
	}
	got, err := png.Decode(bytes.NewReader(defaultPNG))
	if err != nil {
		t.Fatal(err)
	}
	if b := got.Bounds(); b.Dx() != Size || b.Dy() != Size {
		t.Fatalf("default.png is %v", b)
	}
	if worst := maxDiff(got, want); worst > 2 {
		t.Fatalf("default.png differs from the brand mark by up to %d per channel; run with -update", worst)
	}
	gotJ, err := jpeg.Decode(bytes.NewReader(defaultJPEG))
	if err != nil {
		t.Fatal(err)
	}
	if mean := meanDiff(gotJ, want); mean > 2 {
		t.Fatalf("default.jpg differs from the brand mark by %.2f per channel on average; run with -update", mean)
	}
	// The corners are the brand green and the mark sits inside the circle:
	// nothing white within 32 px of the circle's edge.
	if c := color.RGBAModel.Convert(got.At(0, 0)).(color.RGBA); c != brandGreen {
		t.Fatalf("corner = %v", c)
	}
	for y := range Size {
		for x := range Size {
			r := math.Hypot(float64(x)+0.5-Size/2, float64(y)+0.5-Size/2)
			if r > Size/2-32 && color.RGBAModel.Convert(got.At(x, y)).(color.RGBA) != brandGreen {
				t.Fatalf("ink at (%d, %d), %.0f px from the centre", x, y, r)
			}
		}
	}
}

func maxDiff(a, b image.Image) int {
	worst := 0
	for y := range Size {
		for x := range Size {
			ar, ag, ab, _ := a.At(x, y).RGBA()
			br, bg, bb, _ := b.At(x, y).RGBA()
			for _, d := range []int{int(ar>>8) - int(br>>8), int(ag>>8) - int(bg>>8), int(ab>>8) - int(bb>>8)} {
				worst = max(worst, d, -d)
			}
		}
	}
	return worst
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
