// SPDX-License-Identifier: GPL-3.0-or-later

package rtsp

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"
	"github.com/bluenviron/gortsplib/v5/pkg/liberrors"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/pion/rtp"

	"github.com/andras-tim/dvr-squirrel/internal/dvrproto"
	"github.com/andras-tim/dvr-squirrel/internal/fakedvr"
	"github.com/andras-tim/dvr-squirrel/internal/placeholder"
)

func startServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	cfg.Address = "127.0.0.1:0"
	if cfg.Path == "" {
		cfg.Path = "/stream"
	}
	s := New(cfg, nil)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// feed writes the frames of channel 0 (and audio) in a loop, about 50 frames per second.
func feed(ctx context.Context, s *Server, frames []*dvrproto.Frame) {
	for i := 0; ctx.Err() == nil; i++ {
		s.WriteFrame(frames[i%len(frames)])
		time.Sleep(5 * time.Millisecond)
	}
}

type au struct {
	nalus [][]byte
	ts    uint32
}

// play connects, records the first n video access units and the count of audio packets.
func play(t *testing.T, rawURL, user, pass string, n int) ([]au, int, *format.H264) {
	t.Helper()
	u, err := base.ParseURL(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	if user != "" {
		u.User = url.UserPassword(user, pass)
	}
	tcp := gortsplib.ProtocolTCP
	c := gortsplib.Client{Scheme: u.Scheme, Host: u.Host, Protocol: &tcp}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	d, _, err := c.Describe(u)
	for i := 0; err != nil && i < 100; i++ {
		var e liberrors.ErrClientBadStatusCode
		if !errors.As(err, &e) || e.Code != base.StatusServiceUnavailable {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
		d, _, err = c.Describe(u)
	}
	if err != nil {
		t.Fatal(err)
	}
	var h *format.H264
	vm := d.FindFormat(&h)
	if vm == nil {
		t.Fatal("no H264 media")
	}
	if err := c.SetupAll(d.BaseURL, d.Medias); err != nil {
		t.Fatal(err)
	}

	dec, err := h.CreateDecoder()
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var aus []au
	audio := 0
	done := make(chan struct{})
	c.OnPacketRTPAny(func(m *description.Media, _ format.Format, pkt *rtp.Packet) {
		mu.Lock()
		defer mu.Unlock()
		if m != vm {
			audio++
			return
		}
		nalus, err := dec.Decode(pkt)
		if err != nil {
			if !errors.Is(err, rtph264.ErrMorePacketsNeeded) {
				t.Errorf("decode: %v", err)
			}
			return
		}
		if len(aus) < n {
			aus = append(aus, au{nalus, pkt.Timestamp})
			if len(aus) == n {
				close(done)
			}
		}
	})
	if _, err := c.Play(nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for video")
	}
	mu.Lock()
	defer mu.Unlock()
	return aus, audio, h
}

func TestLateJoinerStartsAtIDR(t *testing.T) {
	s := startServer(t, Config{})
	frames := fakedvr.Sequence([]uint8{0}, 40, 10, 4_293_000_000, false) // wraps the microsecond counter
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go feed(ctx, s, frames)
	waitLive(t, s)

	// Join several times, at different points of the GOP.
	for range 3 {
		aus, _, h := play(t, "rtsp://"+s.Addr()+"/stream", "", "", 15)
		if !bytes.Equal(h.SPS, fakedvr.SPS()) || !bytes.Equal(h.PPS, fakedvr.PPS()) {
			t.Fatal("SDP lacks the stream's SPS/PPS")
		}
		if !h264.IsRandomAccess(aus[0].nalus) {
			t.Fatalf("first access unit is not an IDR: NALU type %v", h264.NALUType(aus[0].nalus[0][0]&0x1f))
		}
		regular := 0
		for i := 1; i < len(aus); i++ {
			// 93.333 ms at 90 kHz, except where the frame loop restarts and the timeline re-anchors
			d := aus[i].ts - aus[i-1].ts
			if int32(d) <= 0 {
				t.Fatalf("access unit %d: timestamp step %d", i, int32(d))
			}
			if d == 8399 || d == 8400 {
				regular++
			}
		}
		if regular < len(aus)-3 {
			t.Fatalf("only %d of %d timestamp steps are one frame", regular, len(aus)-1)
		}
		time.Sleep(37 * time.Millisecond)
	}
	if idr := s.LastIDR(); !h264IsIDRPayload(idr) {
		t.Fatal("LastIDR is not an IDR payload")
	}
	if v := s.VideoInfo(); v.Width != fakedvr.Width || v.Height != fakedvr.Height || v.GOP != 10 || v.FPS < 10 || v.FPS > 11 {
		t.Fatalf("VideoInfo %+v", v)
	}
}

func TestAudioTrack(t *testing.T) {
	s := startServer(t, Config{Audio: true})
	frames := fakedvr.Sequence([]uint8{0}, 40, 10, 0, true)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go feed(ctx, s, frames)

	_, audio, _ := play(t, "rtsp://"+s.Addr()+"/stream", "", "", 30)
	if audio == 0 {
		t.Fatal("no audio packets")
	}
}

func waitLive(t *testing.T, s *Server) {
	t.Helper()
	for i := 0; ; i++ {
		if off, _ := s.Status(); !off {
			return
		}
		if i == 500 {
			t.Fatal("never went live")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func isFiller(a au) bool {
	return h264.NALUType(a.nalus[0][0]&0x1f) == h264.NALUTypeSPS && !bytes.Equal(a.nalus[0], fakedvr.SPS())
}

func TestStartupServesFiller(t *testing.T) {
	s := startServer(t, Config{Name: "Yard", Filler: placeholder.Style{Text: true}, FillerInterval: 50 * time.Millisecond})
	aus, _, h := play(t, "rtsp://"+s.Addr()+"/stream", "", "", 3)
	var sps h264.SPS
	if err := sps.Unmarshal(h.SPS); err != nil || sps.Width() != 352 || sps.Height() != 288 {
		t.Fatalf("SDP SPS: %v %dx%d", err, sps.Width(), sps.Height())
	}
	for i, a := range aus {
		if !isFiller(a) || !h264.IsRandomAccess(a.nalus) {
			t.Fatalf("access unit %d is not a filler IDR", i)
		}
		if i > 0 && int32(a.ts-aus[i-1].ts) <= 0 {
			t.Fatalf("access unit %d: timestamp step %d", i, int32(a.ts-aus[i-1].ts))
		}
	}
	if off, _ := s.Status(); !off {
		t.Fatal("Status reports live without a DVR")
	}
}

// TestOutageKeepsSession keeps one RTSP session open while the DVR stops and comes back with a jumped
// microsecond counter: the session sees live, filler, live, with increasing timestamps.
func TestOutageKeepsSession(t *testing.T) {
	s := startServer(t, Config{Name: "Yard", Filler: placeholder.Style{Text: true},
		StaleAfter: 300 * time.Millisecond, FillerInterval: 50 * time.Millisecond})
	feedFor := func(start uint32, d time.Duration) {
		frames := fakedvr.Sequence([]uint8{0}, 1000, 10, start, false)
		end := time.Now().Add(d)
		for i := 0; time.Now().Before(end); i++ {
			s.WriteFrame(frames[i])
			time.Sleep(10 * time.Millisecond)
		}
	}
	go func() {
		feedFor(0, 600*time.Millisecond)
		time.Sleep(800 * time.Millisecond) // outage: filler after StaleAfter
		feedFor(3_000_000_000, 2*time.Second)
	}()
	waitLive(t, s)

	aus, _, _ := play(t, "rtsp://"+s.Addr()+"/stream", "", "", 120)
	var phases []string
	for i, a := range aus {
		kind := "live"
		if isFiller(a) {
			kind = "filler"
		}
		if len(phases) == 0 || phases[len(phases)-1] != kind {
			phases = append(phases, kind)
			if !h264.IsRandomAccess(a.nalus) {
				t.Fatalf("access unit %d starts the %s phase without an IDR", i, kind)
			}
		}
		if i > 0 {
			if d := int32(a.ts - aus[i-1].ts); d <= 0 || d > 90_000 {
				t.Fatalf("access unit %d (%s): timestamp step %d", i, kind, d)
			}
		}
	}
	if strings.Join(phases, ",") != "live,filler,live" {
		t.Fatalf("phases %v", phases)
	}
}

func TestAuthAndPath(t *testing.T) {
	s := startServer(t, Config{User: "viewer", Password: "pw"})
	frames := fakedvr.Sequence([]uint8{0}, 20, 10, 0, false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go feed(ctx, s, frames)

	describe := func(rawURL string) error {
		u, _ := base.ParseURL(rawURL)
		c := gortsplib.Client{Scheme: u.Scheme, Host: u.Host}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_, _, err := c.Describe(u)
		return err
	}
	var e liberrors.ErrClientBadStatusCode
	if err := describe("rtsp://" + s.Addr() + "/stream"); !errors.As(err, &e) || e.Code != base.StatusUnauthorized {
		t.Fatalf("no credentials: got %v, want 401", err)
	}
	if err := describe("rtsp://viewer:wrong@" + s.Addr() + "/stream"); !errors.As(err, &e) || e.Code != base.StatusUnauthorized {
		t.Fatalf("wrong password: got %v, want 401", err)
	}
	if err := describe("rtsp://viewer:pw@" + s.Addr() + "/other"); !errors.As(err, &e) || e.Code != base.StatusNotFound {
		t.Fatalf("wrong path: got %v, want 404", err)
	}
	if aus, _, _ := play(t, "rtsp://"+s.Addr()+"/stream", "viewer", "pw", 3); len(aus) != 3 {
		t.Fatal("authorized play failed")
	}
}

func TestClockWrap(t *testing.T) {
	var c clock
	if c.micros(4_294_967_000) != 0 {
		t.Fatal("first value not zero")
	}
	if got := c.micros(704); got != 1000 {
		t.Fatalf("across the wrap: %d", got)
	}
	if got := c.micros(504); got != 800 {
		t.Fatalf("small step back: %d", got)
	}
}

func h264IsIDRPayload(b []byte) bool {
	var au h264.AnnexB
	return au.Unmarshal(b) == nil && h264.IsRandomAccess(au)
}

func TestFillerCounterRerendersPerMinute(t *testing.T) {
	s := New(Config{Name: "Yard", Filler: placeholder.Style{Text: true}}, nil)
	t0 := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
	s.offlineSince = t0
	s.fillerAU(t0.Add(10 * time.Second))
	k1 := s.filler.key
	s.fillerAU(t0.Add(50 * time.Second))
	if s.filler.key != k1 {
		t.Fatal("re-rendered within the same minute")
	}
	s.fillerAU(t0.Add(61 * time.Second))
	if s.filler.key == k1 || !strings.Contains(s.filler.key, "1 min") {
		t.Fatalf("not re-rendered after a minute: %s", s.filler.key)
	}
}
