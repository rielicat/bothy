//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// Bothy's picture: a bot without one gets the default when it connects, and
// an admin's change reaches Telegram at once while Slack needs a person.
func TestBotPicture(t *testing.T) {
	e := chStart(t, chOpts{telegram: true, slack: true})
	chEventually(t, 15*time.Second, "the default picture on Telegram", func() bool { return e.tg.ProfilePhoto() != nil })
	first := e.tg.ProfilePhoto()
	if c := picCentre(t, first, "jpeg"); !picNear(c.At(16, 16), color.RGBA{0x2a, 0x6a, 0x54, 0xff}) {
		t.Fatalf("the default picture's corner is %v, not the brand green", c.At(16, 16))
	}
	var info dto.PictureInfo
	if e.JSON(t, http.MethodGet, "/api/agent/picture/info", nil, &info) != http.StatusOK || info.Custom || info.Version == "" {
		t.Fatalf("info = %+v", info)
	}

	orange := color.RGBA{0xf0, 0x80, 0x20, 0xff}
	upload := image.NewRGBA(image.Rect(0, 0, 600, 300))
	for y := range 300 {
		for x := range 600 {
			upload.SetRGBA(x, y, orange)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, upload); err != nil {
		t.Fatal(err)
	}
	resp := e.Do(t, http.MethodPut, "/api/agent/picture", buf.Bytes(), http.Header{"Content-Type": {"application/octet-stream"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT = %s %s", resp.Status, resp.Body)
	}
	var change dto.PictureChange
	if err := json.Unmarshal(resp.Body, &change); err != nil {
		t.Fatal(err)
	}
	if !change.Picture.Custom || len(change.Apps) != 2 ||
		change.Apps[0] != (dto.PictureAppResult{App: "slack", Result: core.PictureManual, Detail: "In Slack, upload it under App icon in Slack's app settings."}) ||
		change.Apps[1] != (dto.PictureAppResult{App: "telegram", Result: core.PictureUpdated, Detail: "Updated in Telegram."}) {
		t.Fatalf("change = %+v", change)
	}
	if c := picCentre(t, e.tg.ProfilePhoto(), "jpeg"); !picNear(c.At(512, 512), orange) {
		t.Fatalf("Telegram shows %v", c.At(512, 512))
	}
	got := e.Do(t, http.MethodGet, "/api/agent/picture", nil, nil)
	if got.Header.Get("ETag") != `"`+change.Picture.Version+`"` || !picNear(picCentre(t, got.Body, "png").At(512, 512), orange) {
		t.Fatalf("GET picture: %s ETag %q", got.Status, got.Header.Get("ETag"))
	}
	fi, err := os.Stat(filepath.Join(e.DataDir, "picture.png"))
	if err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("picture.png: %v %v", fi, err)
	}

	resp = e.Do(t, http.MethodDelete, "/api/agent/picture", nil, nil)
	change = dto.PictureChange{}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(resp.Body, &change) != nil || change.Picture.Custom ||
		change.Picture.Version != info.Version || change.Apps[1].Result != core.PictureUpdated {
		t.Fatalf("DELETE = %s %s", resp.Status, resp.Body)
	}
	if c := picCentre(t, e.tg.ProfilePhoto(), "jpeg"); !picNear(c.At(16, 16), color.RGBA{0x2a, 0x6a, 0x54, 0xff}) {
		t.Fatalf("Telegram after the reset shows %v", c.At(16, 16))
	}
	if _, err := os.Stat(filepath.Join(e.DataDir, "picture.png")); !os.IsNotExist(err) {
		t.Fatalf("picture.png after the reset: %v", err)
	}
}

func picCentre(t *testing.T, b []byte, format string) image.Image {
	t.Helper()
	var img image.Image
	var err error
	if format == "png" {
		img, err = png.Decode(bytes.NewReader(b))
	} else {
		img, err = jpeg.Decode(bytes.NewReader(b))
	}
	if err != nil {
		t.Fatalf("decode %s: %v", format, err)
	}
	if r := img.Bounds(); r.Dx() != 1024 || r.Dy() != 1024 {
		t.Fatalf("picture is %v", r)
	}
	return img
}

func picNear(c color.Color, want color.RGBA) bool {
	r, g, b, _ := c.RGBA()
	d := func(x uint32, y uint8) bool { v := int(x>>8) - int(y); return v < 12 && v > -12 }
	return d(r, want.R) && d(g, want.G) && d(b, want.B)
}
