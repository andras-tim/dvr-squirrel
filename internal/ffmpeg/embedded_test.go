// SPDX-License-Identifier: GPL-3.0-or-later

//go:build embedffmpeg && linux && amd64

package ffmpeg

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os/exec"
	"testing"

	"github.com/andras-tim/dvr-squirrel/internal/h264pcm"
)

// TestEmbeddedDecodes runs the embedded build the way the snapshots do, on an I_PCM frame of our own encoder.
func TestEmbeddedDecodes(t *testing.T) {
	t.Setenv("CACHE_DIRECTORY", t.TempDir())
	p, err := Extract()
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 352, 288))
	for y := range 288 {
		for x := range 352 {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	cmd := exec.Command(p, "-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "h264", "-i", "pipe:0", "-frames:v", "1", "-f", "mjpeg", "-q:v", "4", "pipe:1")
	cmd.Stdin = bytes.NewReader(h264pcm.Encode(img, 0))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %s", err, stderr.String())
	}
	got, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil || got.Bounds().Dx() != 352 || got.Bounds().Dy() != 288 {
		t.Fatalf("decoded %v, %v", got.Bounds(), err)
	}
}
