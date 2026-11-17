// SPDX-License-Identifier: GPL-3.0-or-later

package snapshot

import (
	"bytes"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andras-tim/dvr-squirrel/internal/fakedvr"
	"github.com/andras-tim/dvr-squirrel/internal/placeholder"
)

func decodeSize(t *testing.T, b []byte) (int, int) {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("not a JPEG: %v", err)
	}
	return img.Bounds().Dx(), img.Bounds().Dy()
}

// fakeFFmpeg writes a script that counts its runs in dir/count and prints out (or fails when out is "").
func fakeFFmpeg(t *testing.T, out []byte, delay string) (path string, runs func() int) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "out.jpg"), out, 0o600); err != nil {
		t.Fatal(err)
	}
	body := "cat >/dev/null\necho x >>\"$(dirname \"$0\")/count\"\nsleep " + delay + "\n"
	if out == nil {
		body += "echo 'broken stream' >&2\nexit 1\n"
	} else {
		body += "cat \"$(dirname \"$0\")/out.jpg\"\n"
	}
	path = filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path, func() int {
		b, _ := os.ReadFile(filepath.Join(dir, "count"))
		return strings.Count(string(b), "x")
	}
}

func TestPlaceholder(t *testing.T) {
	s := &Snapshotter{Name: "Yard 2", LastIDR: func() []byte { return nil }}
	if w, h := decodeSize(t, s.JPEG()); w != 352 || h != 288 {
		t.Fatalf("default size %dx%d", w, h)
	}
	s = &Snapshotter{Name: strings.Repeat("Long name ", 20), LastIDR: func() []byte { return nil },
		Size: func() (int, int) { return 704, 576 }}
	if w, h := decodeSize(t, s.JPEG()); w != 704 || h != 576 {
		t.Fatalf("stream size %dx%d", w, h)
	}
}

func TestOfflineShowsPlaceholder(t *testing.T) {
	ff, runs := fakeFFmpeg(t, []byte{0xff, 0xd8, 0xff, 0xd9}, "0")
	s := &Snapshotter{Name: "x", FFmpeg: ff, Style: placeholder.Style{Text: true},
		LastIDR: func() []byte { return fakedvr.IDRPayload(100, 1) },
		Offline: func() (bool, time.Time) { return true, time.Now() }}
	if w, h := decodeSize(t, s.JPEG()); w != 352 || h != 288 || runs() != 0 {
		t.Fatalf("%dx%d, ffmpeg runs %d", w, h, runs())
	}
}

func TestFFmpegCacheAndSingleFlight(t *testing.T) {
	want := (&Snapshotter{Name: "decoded", LastIDR: func() []byte { return nil }}).JPEG()
	ff, runs := fakeFFmpeg(t, want, "0.2")
	s := &Snapshotter{Name: "x", FFmpeg: ff, CacheFor: 300 * time.Millisecond,
		LastIDR: func() []byte { return fakedvr.IDRPayload(100, 1) }}

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if got := s.JPEG(); !bytes.Equal(got, want) {
				t.Error("concurrent caller got another image")
			}
		})
	}
	wg.Wait()
	if runs() != 1 {
		t.Fatalf("ffmpeg ran %d times for concurrent callers", runs())
	}
	s.JPEG()
	if runs() != 1 {
		t.Fatal("cached snapshot not reused")
	}
	time.Sleep(350 * time.Millisecond)
	s.JPEG()
	if runs() != 2 {
		t.Fatalf("expired snapshot not renewed: %d runs", runs())
	}
}

func TestFFmpegFailureFallsBack(t *testing.T) {
	ff, runs := fakeFFmpeg(t, nil, "0")
	s := &Snapshotter{Name: "x", FFmpeg: ff, LastIDR: func() []byte { return fakedvr.IDRPayload(100, 1) }}
	decodeSize(t, s.JPEG())
	decodeSize(t, s.JPEG())
	if runs() != 2 || !s.warned {
		t.Fatalf("runs %d, warned %t", runs(), s.warned)
	}
}

func TestNoFrameSkipsFFmpeg(t *testing.T) {
	ff, runs := fakeFFmpeg(t, []byte{0xff, 0xd8, 0xff, 0xd9}, "0")
	s := &Snapshotter{Name: "x", FFmpeg: ff, LastIDR: func() []byte { return nil }}
	decodeSize(t, s.JPEG())
	if runs() != 0 {
		t.Fatal("ffmpeg ran without a frame")
	}
}

// TestRealFFmpeg encodes a test picture with ffmpeg and decodes it back; skipped without ffmpeg and libx264.
func TestRealFFmpeg(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not in PATH")
	}
	idr, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=352x288:rate=10",
		"-frames:v", "1", "-pix_fmt", "yuv420p", "-c:v", "libx264", "-profile:v", "baseline", "-f", "h264", "pipe:1").Output()
	if err != nil {
		t.Skipf("cannot encode a test frame: %v", err)
	}
	s := &Snapshotter{Name: "x", FFmpeg: ff, LastIDR: func() []byte { return idr }}
	if w, h := decodeSize(t, s.JPEG()); w != 352 || h != 288 || s.warned {
		t.Fatalf("decoded %dx%d, warned %t", w, h, s.warned)
	}
}

func TestServeHTTP(t *testing.T) {
	s := &Snapshotter{Name: "x", LastIDR: func() []byte { return nil }}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/snapshot.jpg", nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	decodeSize(t, w.Body.Bytes())
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/snapshot.jpg", nil))
	if w.Code != 405 {
		t.Fatalf("POST: %d", w.Code)
	}
}
