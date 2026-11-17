// SPDX-License-Identifier: GPL-3.0-or-later

// Package snapshot serves a JPEG of a channel: its latest IDR frame decoded by ffmpeg, or the
// channel's placeholder while it is offline, or when ffmpeg is missing or fails.
package snapshot

import (
	"bytes"
	"context"
	"errors"
	"image/jpeg"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/andras-tim/dvr-squirrel/internal/placeholder"
)

// Snapshotter renders and caches the snapshot of one channel. Concurrent callers share one render.
type Snapshotter struct {
	Name string
	// FFmpeg is the ffmpeg executable; empty serves placeholders only.
	FFmpeg   string
	CacheFor time.Duration
	Timeout  time.Duration
	// LastIDR returns the Annex-B payload of the latest IDR frame (SPS, PPS, slice), or nil.
	LastIDR func() []byte
	// Size returns the video resolution, zero when unknown.
	Size func() (width, height int)
	// Offline reports whether the channel shows filler, and since when; nil means always live.
	Offline func() (bool, time.Time)
	Style   placeholder.Style
	Log     *slog.Logger

	mu       sync.Mutex
	jpeg     []byte
	at       time.Time
	inflight *render
	warned   bool
}

type render struct {
	done chan struct{}
	jpeg []byte
}

func (s *Snapshotter) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}

// JPEG returns the current snapshot, rendering it when the cached one is older than CacheFor.
func (s *Snapshotter) JPEG() []byte {
	s.mu.Lock()
	if s.jpeg != nil && time.Since(s.at) < s.CacheFor {
		j := s.jpeg
		s.mu.Unlock()
		return j
	}
	if r := s.inflight; r != nil {
		s.mu.Unlock()
		<-r.done
		return r.jpeg
	}
	r := &render{done: make(chan struct{})}
	s.inflight = r
	s.mu.Unlock()

	r.jpeg = s.render()

	s.mu.Lock()
	s.jpeg, s.at, s.inflight = r.jpeg, time.Now(), nil
	s.mu.Unlock()
	close(r.done)
	return r.jpeg
}

func (s *Snapshotter) render() []byte {
	if s.Offline != nil {
		if off, since := s.Offline(); off {
			return s.fallback(placeholder.Offline(s.Name, since, time.Now(), placeholder.ReasonNoDVR))
		}
	}
	idr := s.LastIDR()
	if idr == nil || s.FFmpeg == "" {
		return s.fallback(placeholder.Text{Title: s.Name})
	}
	j, err := s.decode(idr)
	if err != nil {
		s.mu.Lock()
		first := !s.warned
		s.warned = true
		s.mu.Unlock()
		if first {
			s.log().Warn("snapshot decoding failed, serving a placeholder (logged once)", "err", err)
		} else {
			s.log().Debug("snapshot decoding failed", "err", err)
		}
		return s.fallback(placeholder.Text{Title: s.Name})
	}
	return j
}

// decode runs ffmpeg on one access unit: H.264 Annex-B on stdin, one JPEG on stdout.
func (s *Snapshotter) decode(au []byte) ([]byte, error) {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.FFmpeg, "-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "h264", "-i", "pipe:0", "-frames:v", "1", "-f", "mjpeg", "-q:v", "4", "pipe:1")
	cmd.Stdin = bytes.NewReader(au)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return nil, &decodeError{err: err, stderr: strings.TrimSpace(errOut.String())}
	}
	if b := out.Bytes(); len(b) > 2 && b[0] == 0xff && b[1] == 0xd8 {
		return b, nil
	}
	return nil, &decodeError{err: errNoJPEG, stderr: strings.TrimSpace(errOut.String())}
}

type decodeError struct {
	err    error
	stderr string
}

func (e *decodeError) Error() string {
	if e.stderr == "" {
		return e.err.Error()
	}
	const maxLen = 300
	msg := e.stderr
	if len(msg) > maxLen {
		msg = msg[:maxLen] + "..."
	}
	return e.err.Error() + ": " + msg
}

func (e *decodeError) Unwrap() error { return e.err }

var errNoJPEG = errors.New("ffmpeg produced no JPEG")

func (s *Snapshotter) fallback(t placeholder.Text) []byte {
	var w, h int
	if s.Size != nil {
		w, h = s.Size()
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, placeholder.Render(s.Style, w, h, t), &jpeg.Options{Quality: 80})
	return buf.Bytes()
}

// ServeHTTP answers GET and HEAD with the snapshot.
func (s *Snapshotter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	j := s.JPEG()
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(j))
}
