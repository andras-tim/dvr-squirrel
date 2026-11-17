// SPDX-License-Identifier: GPL-3.0-or-later

package placeholder

import (
	"image"
	"image/color"
	"testing"
	"time"
)

func brightPixels(img *image.RGBA) int {
	n := 0
	for i := 0; i < len(img.Pix); i += 4 {
		if img.Pix[i] > 0xa0 && img.Pix[i+1] > 0xa0 {
			n++
		}
	}
	return n
}

func TestRenderText(t *testing.T) {
	blank := Render(Style{Text: true}, 0, 0, Text{})
	if blank.Bounds().Dx() != DefaultWidth || blank.Bounds().Dy() != DefaultHeight || brightPixels(blank) != 0 {
		t.Fatal("empty text should give a plain default-size background")
	}
	title := Render(Style{Text: true}, 704, 576, Text{Title: "Yard"})
	full := Render(Style{Text: true}, 704, 576, Offline("Yard", time.Now(), time.Now(), ReasonNoDVR))
	if title.Bounds().Dx() != 704 || brightPixels(title) < 100 || brightPixels(full) <= brightPixels(title) {
		t.Fatalf("text pixels: title %d, full %d", brightPixels(title), brightPixels(full))
	}
	if n := brightPixels(Render(Style{}, 352, 288, Offline("Yard", time.Now(), time.Now(), ReasonNoDVR))); n != 0 {
		t.Fatalf("Text off still drew %d pixels", n)
	}
	// a very long name is cut, not drawn outside
	Render(Style{Text: true}, 64, 48, Text{Title: "a very long channel name that does not fit"})
}

func TestRenderBase(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 2, 2))
	base.Set(0, 0, color.RGBA{255, 0, 0, 255})
	base.Set(1, 0, color.RGBA{0, 255, 0, 255})
	base.Set(0, 1, color.RGBA{0, 0, 255, 255})
	base.Set(1, 1, color.RGBA{255, 255, 255, 255})
	img := Render(Style{Base: base}, 100, 50, Text{Title: "ignored"})
	for _, c := range []struct {
		x, y int
		want color.RGBA
	}{{10, 10, color.RGBA{255, 0, 0, 255}}, {90, 10, color.RGBA{0, 255, 0, 255}}, {10, 40, color.RGBA{0, 0, 255, 255}}} {
		if got := img.RGBAAt(c.x, c.y); got != c.want {
			t.Fatalf("(%d,%d) = %v, want %v", c.x, c.y, got, c.want)
		}
	}
}

func TestOfflineText(t *testing.T) {
	since := time.Date(2026, 1, 2, 0, 2, 0, 0, time.Local)
	got := Offline("Yard", since, since.Add(12*time.Minute+30*time.Second), ReasonNoDVR)
	if got != (Text{Title: "Yard", Status: "OFFLINE since 00:02 \u00b7 12 min", Reason: "no connection to DVR"}) {
		t.Fatalf("%+v", got)
	}
	for d, want := range map[time.Duration]string{
		-time.Second:                  "0 min",
		59 * time.Second:              "0 min",
		59 * time.Minute:              "59 min",
		65 * time.Minute:              "1 h 5 min",
		24*time.Hour + 3*time.Hour:    "1 d 3 h",
		49*time.Hour + 59*time.Minute: "2 d 1 h",
	} {
		if got := Elapsed(d); got != want {
			t.Errorf("Elapsed(%s) = %q, want %q", d, got, want)
		}
	}
	// the middle dot has a glyph of its own, not the "?" fallback
	if glyph('\u00b7') == glyph('?') {
		t.Fatal("no glyph for the middle dot")
	}
}
