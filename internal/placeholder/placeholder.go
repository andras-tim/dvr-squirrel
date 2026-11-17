// SPDX-License-Identifier: GPL-3.0-or-later

// Package placeholder draws the picture shown instead of a channel's video: the filler stream during
// an outage and the fallback snapshot. Stdlib only.
package placeholder

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"time"
)

// Default size when the channel's resolution is not known yet.
const (
	DefaultWidth  = 352
	DefaultHeight = 288
)

// ReasonNoDVR is the reason shown while the DVR connection is down.
const ReasonNoDVR = "no connection to DVR"

// Style is a channel's configured look.
type Style struct {
	// Base is drawn scaled to the frame; nil draws a dark background.
	Base image.Image
	// Text enables the text lines; without it the picture is shown as is.
	Text bool
}

// Text holds the lines: Title and Status large, Reason smaller below. Empty lines are skipped.
type Text struct {
	Title  string
	Status string
	Reason string
}

// Offline is the text of a channel that is offline since the given time: the start in local time and
// the elapsed time, which changes once a minute.
func Offline(name string, since, now time.Time, reason string) Text {
	return Text{
		Title:  name,
		Status: "OFFLINE since " + since.Local().Format("15:04") + " \u00b7 " + Elapsed(now.Sub(since)),
		Reason: reason,
	}
}

// Elapsed formats an outage length in whole minutes: "12 min", "1 h 5 min", "2 d 3 h".
func Elapsed(d time.Duration) string {
	m := int(max(d, 0) / time.Minute)
	switch {
	case m < 60:
		return fmt.Sprintf("%d min", m)
	case m < 24*60:
		return fmt.Sprintf("%d h %d min", m/60, m%60)
	default:
		return fmt.Sprintf("%d d %d h", m/(24*60), m%(24*60)/60)
	}
}

var (
	background = color.RGBA{0x20, 0x22, 0x26, 0xff}
	foreground = color.RGBA{0xd8, 0xdc, 0xe0, 0xff}
	band       = color.RGBA{0x00, 0x00, 0x00, 0xb0} // behind the text on a custom picture
)

// Render draws the picture at w x h (the defaults when zero).
func Render(st Style, w, h int, t Text) *image.RGBA {
	if w <= 0 || h <= 0 {
		w, h = DefaultWidth, DefaultHeight
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if st.Base != nil {
		scale(img, st.Base)
	} else {
		draw.Draw(img, img.Bounds(), image.NewUniform(background), image.Point{}, draw.Src)
	}
	if !st.Text {
		return img
	}

	type line struct {
		text  []rune
		scale int
	}
	const margin = 8
	fit := func(s string, maxScale int) line {
		r := []rune(s)
		maxChars := max((w-2*margin)/(glyphW+1), 1)
		if len(r) > maxChars {
			r = r[:maxChars]
		}
		sc := 1
		if len(r) > 0 {
			sc = max(min((w-2*margin)/(len(r)*(glyphW+1)), maxScale), 1)
		}
		return line{r, sc}
	}
	big := max(min(h/(4*glyphH), 6), 1)
	var lines []line
	for _, s := range []string{t.Title, t.Status} {
		if s != "" {
			lines = append(lines, fit(s, big))
		}
	}
	// One scale for the large lines keeps them aligned in size.
	if len(lines) == 2 {
		sc := min(lines[0].scale, lines[1].scale)
		lines[0].scale, lines[1].scale = sc, sc
	}
	if t.Reason != "" {
		large := big
		if len(lines) > 0 {
			large = lines[0].scale
		}
		lines = append(lines, fit(t.Reason, max(large*2/3, 1)))
	}
	if len(lines) == 0 {
		return img
	}

	total := 0
	for i, l := range lines {
		total += glyphH * l.scale
		if i > 0 {
			total += glyphH * l.scale / 2
		}
	}
	y := (h - total) / 2
	if st.Base != nil {
		draw.Draw(img, image.Rect(0, y-margin, w, y+total+margin), image.NewUniform(band), image.Point{}, draw.Over)
	}
	for i, l := range lines {
		if i > 0 {
			y += glyphH * l.scale / 2
		}
		tw := len(l.text)*(glyphW+1)*l.scale - l.scale
		drawText(img, l.text, (w-tw)/2, y, l.scale)
		y += glyphH * l.scale
	}
	return img
}

func drawText(img *image.RGBA, text []rune, x0, y0, sc int) {
	fg := image.NewUniform(foreground)
	for i, r := range text {
		g := glyph(r)
		for row := range glyphH {
			for col := range glyphW {
				if g[row]&(1<<(glyphW-1-col)) == 0 {
					continue
				}
				px := x0 + (i*(glyphW+1)+col)*sc
				py := y0 + row*sc
				draw.Draw(img, image.Rect(px, py, px+sc, py+sc), fg, image.Point{}, draw.Src)
			}
		}
	}
}

// scale draws src over the whole of dst, nearest neighbor.
func scale(dst *image.RGBA, src image.Image) {
	sb := src.Bounds()
	w, h := dst.Bounds().Dx(), dst.Bounds().Dy()
	for y := range h {
		sy := sb.Min.Y + y*sb.Dy()/h
		for x := range w {
			dst.Set(x, y, src.At(sb.Min.X+x*sb.Dx()/w, sy))
		}
	}
}
