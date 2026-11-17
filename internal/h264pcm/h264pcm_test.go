// SPDX-License-Identifier: GPL-3.0-or-later

package h264pcm

import (
	"bytes"
	"image"
	"image/color"
	"os/exec"
	"testing"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
)

func testImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), uint8((x + y) % 256), 255})
		}
	}
	return img
}

func TestStructure(t *testing.T) {
	for _, size := range [][2]int{{352, 288}, {100, 60}, {1920, 1080}} {
		var au h264.AnnexB
		if err := au.Unmarshal(Encode(testImage(size[0], size[1]), 0)); err != nil {
			t.Fatal(err)
		}
		want := []h264.NALUType{h264.NALUTypeSPS, h264.NALUTypePPS, h264.NALUTypeIDR}
		if len(au) != 3 {
			t.Fatalf("%v: %d NALUs", size, len(au))
		}
		for i, n := range au {
			if got := h264.NALUType(n[0] & 0x1f); got != want[i] {
				t.Fatalf("%v: NALU %d is %v", size, i, got)
			}
		}
		var sps h264.SPS
		if err := sps.Unmarshal(au[0]); err != nil {
			t.Fatal(err)
		}
		if sps.Width() != size[0] || sps.Height() != size[1] || sps.ProfileIdc != 66 {
			t.Fatalf("%v: SPS says %dx%d profile %d", size, sps.Width(), sps.Height(), sps.ProfileIdc)
		}
		if !h264.IsRandomAccess(au) {
			t.Fatalf("%v: not a random access unit", size)
		}
	}
}

func TestEscape(t *testing.T) {
	got := escape([]byte{0, 0, 0, 0, 0, 1, 0, 0, 4, 0, 0})
	want := []byte{0, 0, 3, 0, 0, 3, 0, 1, 0, 0, 4, 0, 0}
	if !bytes.Equal(got, want) {
		t.Fatalf("got % x", got)
	}
}

// TestFFmpegDecodesLossless decodes the access unit with ffmpeg; I_PCM is lossless, so the decoded
// planes must equal the encoder's. Skipped without ffmpeg.
func TestFFmpegDecodesLossless(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not in PATH")
	}
	for _, size := range [][2]int{{352, 288}, {100, 60}} {
		img := testImage(size[0], size[1])
		cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-f", "h264", "-i", "pipe:0",
			"-f", "rawvideo", "-pix_fmt", "yuv420p", "pipe:1")
		cmd.Stdin = bytes.NewReader(Encode(img, 1))
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%v: ffmpeg: %v: %s", size, err, stderr.String())
		}
		w, h := size[0], size[1]
		if len(out) != w*h*3/2 {
			t.Fatalf("%v: %d bytes decoded, want %d", size, len(out), w*h*3/2)
		}
		p := toPlanes(img)
		pw := p.mbW * 16
		for y := range h {
			for x := range w {
				if out[y*w+x] != p.y[y*pw+x] {
					t.Fatalf("%v: luma (%d,%d) = %d, want %d", size, x, y, out[y*w+x], p.y[y*pw+x])
				}
			}
		}
		cb := out[w*h:]
		for y := range h / 2 {
			for x := range w / 2 {
				if cb[y*w/2+x] != p.cb[y*pw/2+x] {
					t.Fatalf("%v: Cb (%d,%d) differs", size, x, y)
				}
			}
		}
	}
}
