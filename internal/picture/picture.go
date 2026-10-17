// Package picture keeps Bothy's picture in the chat apps (core.AgentPicture):
// the embedded default, rendered from the brand mark, or one an admin
// uploaded, kept as <data>/picture.png.
package picture

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"os"
	"sync"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/webp"

	"github.com/rielicat/bothy/internal/core"
)

const (
	// Size is the side of every picture Bothy keeps and sends: inside
	// Slack's 512–2000 px for app icons, and plenty for Telegram and Discord.
	Size = 1024
	// MaxUpload is the largest upload Set accepts.
	MaxUpload = 5 << 20
	// FileName is the admin's picture in the data dir.
	FileName = "picture.png"

	maxSide = 8000
	minSide = 128
	// maxDecoded bounds what decoding an upload may allocate; a small file
	// can declare a huge image (a decompression bomb).
	maxDecoded = 160 << 20
	// maxStored bounds reading picture.png back; Set writes far less.
	maxStored   = 16 << 20
	jpegQuality = 90
)

var _ core.AgentPicture = (*Store)(nil)

var (
	errNotPicture = fmt.Errorf("that file isn't a picture Bothy can use; choose a PNG, JPEG or WebP image: %w", core.ErrInvalid)
	errTooLarge   = fmt.Errorf("that picture is over 5 MB; choose a smaller file: %w", core.ErrInvalid)
	errTooBig     = fmt.Errorf("that picture is too large; choose one under %d pixels a side: %w", maxSide, core.ErrInvalid)
	errTooSmall   = fmt.Errorf("that picture is too small; choose one at least %d pixels a side: %w", minSide, core.ErrInvalid)
	errNoDataDir  = fmt.Errorf("picture: no data directory: %w", core.ErrNotImplemented)
)

var def = func() core.Picture {
	return core.Picture{PNG: defaultPNG, JPEG: defaultJPEG, Version: version(defaultPNG)}
}()

// Default is the built-in picture.
func Default() core.Picture { return def }

// Store implements core.AgentPicture over dir, the data dir.
type Store struct {
	dir string

	// mu serialises Set, Reset and the cache; decoding an upload may take
	// up to maxDecoded, so two never run at once.
	mu         sync.Mutex
	cached     core.Picture
	cachedFile string // version of the picture.png bytes cached was read from
}

// New returns a Store for dataDir; with "" only the default exists.
func New(dataDir string) *Store { return &Store{dir: dataDir} }

func (s *Store) Current(_ context.Context) (core.Picture, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir == "" {
		return def, nil
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return core.Picture{}, err
	}
	defer root.Close()
	f, err := root.Open(FileName)
	if errors.Is(err, fs.ErrNotExist) {
		return def, nil
	}
	if err != nil {
		return core.Picture{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxStored+1))
	if err != nil {
		return core.Picture{}, err
	}
	sum := version(b)
	if s.cachedFile == sum {
		return s.cached, nil
	}
	p, err := load(b)
	if err != nil {
		return core.Picture{}, fmt.Errorf("picture: %s: %w", FileName, err)
	}
	s.cached, s.cachedFile = p, sum
	return p, nil
}

// load reads picture.png back. Set's own output is used as it is; a file
// someone put there by hand goes through the same checks as an upload.
func load(b []byte) (core.Picture, error) {
	if cfg, err := png.DecodeConfig(bytes.NewReader(b)); err == nil && cfg.Width == Size && cfg.Height == Size {
		if img, err := png.Decode(bytes.NewReader(b)); err == nil {
			if o, ok := img.(interface{ Opaque() bool }); ok && o.Opaque() {
				jpg, err := encodeJPEG(img)
				if err != nil {
					return core.Picture{}, err
				}
				return core.Picture{PNG: b, JPEG: jpg, Custom: true, Version: version(b)}, nil
			}
		}
	}
	img, err := normalize(b)
	if err != nil {
		return core.Picture{}, err
	}
	p, err := encode(img)
	p.Custom = true
	return p, err
}

func (s *Store) Set(_ context.Context, upload []byte) (core.Picture, error) {
	if s.dir == "" {
		return core.Picture{}, errNoDataDir
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	img, err := normalize(upload)
	if err != nil {
		return core.Picture{}, err
	}
	p, err := encode(img)
	if err != nil {
		return core.Picture{}, err
	}
	p.Custom = true
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return core.Picture{}, err
	}
	defer root.Close()
	if err := writeAtomic(root, FileName, p.PNG); err != nil {
		return core.Picture{}, err
	}
	s.cached, s.cachedFile = p, p.Version
	return p, nil
}

func (s *Store) Reset(_ context.Context) (core.Picture, error) {
	if s.dir == "" {
		return def, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return core.Picture{}, err
	}
	defer root.Close()
	if err := root.Remove(FileName); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return core.Picture{}, err
	}
	s.cached, s.cachedFile = core.Picture{}, ""
	return def, nil
}

// normalize sniffs the format (never trusting a declared type), checks the
// declared dimensions before decoding, crops the centre square, turns it
// upright, flattens it onto white and scales it to Size.
func normalize(b []byte) (*image.RGBA, error) {
	if len(b) > MaxUpload {
		return nil, errTooLarge
	}
	format := sniff(b)
	if format == "" {
		return nil, errNotPicture
	}
	cfg, err := decodeConfig(format, b)
	if err != nil {
		return nil, errNotPicture
	}
	if cfg.Width > maxSide || cfg.Height > maxSide || int64(cfg.Width)*int64(cfg.Height)*bytesPerPixel(cfg.ColorModel) > maxDecoded {
		return nil, errTooBig
	}
	if min(cfg.Width, cfg.Height) < minSide {
		return nil, errTooSmall
	}
	src, err := decode(format, b)
	if err != nil {
		return nil, errNotPicture
	}
	sb := src.Bounds()
	side := min(sb.Dx(), sb.Dy())
	if side < minSide {
		return nil, errTooSmall
	}
	x0, y0 := sb.Min.X+(sb.Dx()-side)/2, sb.Min.Y+(sb.Dy()-side)/2
	dst := image.NewRGBA(image.Rect(0, 0, Size, Size))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, image.Rect(x0, y0, x0+side, y0+side), xdraw.Over, nil)
	if format == "jpeg" {
		dst = orient(dst, jpegOrientation(b))
	}
	return dst, nil
}

func sniff(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}):
		return "jpeg"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "webp"
	}
	return ""
}

func decodeConfig(format string, b []byte) (image.Config, error) {
	r := bytes.NewReader(b)
	switch format {
	case "png":
		return png.DecodeConfig(r)
	case "jpeg":
		return jpeg.DecodeConfig(r)
	}
	return webp.DecodeConfig(r)
}

func decode(format string, b []byte) (image.Image, error) {
	r := bytes.NewReader(b)
	switch format {
	case "png":
		return png.Decode(r)
	case "jpeg":
		return jpeg.Decode(r)
	}
	return webp.Decode(r)
}

// bytesPerPixel is what the decoder allocates per pixel for a colour model,
// rounded up (YCbCr as if it were not subsampled).
func bytesPerPixel(m color.Model) int64 {
	if _, ok := m.(color.Palette); ok {
		return 1
	}
	switch m {
	case color.GrayModel, color.AlphaModel:
		return 1
	case color.Gray16Model, color.Alpha16Model:
		return 2
	case color.YCbCrModel:
		return 3
	case color.RGBA64Model, color.NRGBA64Model:
		return 8
	}
	return 4
}

func encode(img *image.RGBA) (core.Picture, error) {
	p, err := encodePNG(img)
	if err != nil {
		return core.Picture{}, err
	}
	j, err := encodeJPEG(img)
	if err != nil {
		return core.Picture{}, err
	}
	return core.Picture{PNG: p, JPEG: j, Version: version(p)}, nil
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeJPEG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func version(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// writeAtomic writes name inside root via a temp file, fsync and rename, 0600.
func writeAtomic(root *os.Root, name string, data []byte) error {
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	tmp := "." + name + "." + hex.EncodeToString(rnd[:]) + ".tmp"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, name)
	}
	if err != nil {
		_ = root.Remove(tmp)
	}
	return err
}
