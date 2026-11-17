// SPDX-License-Identifier: GPL-3.0-or-later

// Package rtsp serves one DVR channel as an RTSP stream: H.264 video and optionally the DVR's AAC audio.
// While the DVR delivers no video, the stream carries a filler picture, so clients stay connected.
package rtsp

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/auth"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtpmpeg4audio"
	"github.com/bluenviron/gortsplib/v5/pkg/liberrors"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/pion/rtp"

	"github.com/andras-tim/dvr-squirrel/internal/dvrproto"
	"github.com/andras-tim/dvr-squirrel/internal/h264pcm"
	"github.com/andras-tim/dvr-squirrel/internal/placeholder"
)

const (
	videoPayloadType = 96
	audioPayloadType = 97

	// maxFrameGapUs is the largest DVR timestamp step between video frames still taken as continuous;
	// anything else (a DVR reconnect, a clock jump) re-anchors the RTP timeline on the wall clock.
	maxFrameGapUs = 5_000_000
)

// defaultAAC describes the audio track in the SDP until the DVR's own ADTS header is seen.
var defaultAAC = mpeg4audio.AudioSpecificConfig{Type: mpeg4audio.ObjectTypeAACLC, SampleRate: 16000, ChannelConfig: 1}

// Config of one channel's RTSP server.
type Config struct {
	Address string // host:port to listen on
	Path    string
	Audio   bool
	// User and Password protect the stream with HTTP digest when User is set.
	User     string
	Password string

	// Name and Filler shape the filler picture.
	Name   string
	Filler placeholder.Style
	// StaleAfter switches to filler when no video came for this long (default 10 s).
	StaleAfter time.Duration
	// FillerInterval is the filler frame period (default 1 s).
	FillerInterval time.Duration
}

// Server is the RTSP server of one channel. Frames go in through WriteFrame.
//
// Every RTSP session gets its own ServerStream, so a session that joins mid-GOP receives nothing
// until the next IDR frame, and its first picture is decodable.
//
// The server starts offline and sends a filler IDR (SPS and PPS in band) every FillerInterval until
// the first DVR IDR; it goes offline again when video stops for StaleAfter or Offline is called.
// RTP timestamps run on one output timeline per channel, continuous across these switches.
type Server struct {
	cfg Config
	log *slog.Logger
	srv *gortsplib.Server

	mu       sync.Mutex
	sps, pps []byte
	aac      *mpeg4audio.AudioSpecificConfig
	info     VideoInfo
	lastIDR  []byte // Annex-B payload of the latest IDR frame, for snapshots
	lastUs   int64
	avgGapUs float64
	sinceIDR int
	describe *gortsplib.ServerStream // answers DESCRIBE only, never written to
	readers  map[*gortsplib.ServerSession]*reader
	videoEnc *rtph264.Encoder
	audioEnc *rtpmpeg4audio.Encoder
	clock    clock

	offline      bool
	offlineSince time.Time
	lastLive     time.Time
	// output timeline in microseconds: live video maps to offset + DVR time, filler to wall time
	lastOut      int64
	lastOutWall  time.Time
	offset       int64
	anchor       bool
	lastVideoExt int64
	filler       struct {
		key string
		au  [2][][]byte // alternate idr_pic_id
		n   int
	}

	stats struct {
		frames      uint64 // live video frames from the DVR
		bytes       uint64
		lastIDR     time.Time
		windowStart time.Time
		windowBytes uint64
		kbps        float64
	}

	stop chan struct{}
	wg   sync.WaitGroup
}

// Stats are the channel's counters for the status page.
type Stats struct {
	Frames  uint64
	Bytes   uint64
	Kbps    float64 // live video bitrate over the last few seconds
	LastIDR time.Time
	Clients []string // remote addresses of the RTSP sessions
}

// Stats returns the current counters.
func (s *Server) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Stats{Frames: s.stats.frames, Bytes: s.stats.bytes, Kbps: s.stats.kbps, LastIDR: s.stats.lastIDR}
	if s.offline {
		st.Kbps = 0
	}
	for ss := range s.readers {
		st.Clients = append(st.Clients, remote(ss))
	}
	return st
}

// countLive updates the counters with a live frame; called with s.mu held.
func (s *Server) countLive(n int, idr bool, now time.Time) {
	st := &s.stats
	st.frames++
	st.bytes += uint64(n)
	if idr {
		st.lastIDR = now
	}
	if st.windowStart.IsZero() {
		st.windowStart = now
	}
	st.windowBytes += uint64(n)
	if d := now.Sub(st.windowStart); d >= 5*time.Second {
		st.kbps = float64(st.windowBytes) * 8 / 1000 / d.Seconds()
		st.windowStart, st.windowBytes = now, 0
	}
}

type reader struct {
	stream  *gortsplib.ServerStream
	video   *description.Media
	audio   *description.Media
	started bool // has received an IDR frame
}

// New returns a server; Start begins listening.
func New(cfg Config, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if cfg.StaleAfter <= 0 {
		cfg.StaleAfter = 10 * time.Second
	}
	if cfg.FillerInterval <= 0 {
		cfg.FillerInterval = time.Second
	}
	s := &Server{cfg: cfg, log: log, readers: map[*gortsplib.ServerSession]*reader{}}
	if cfg.Audio {
		aac := defaultAAC
		s.aac = &aac
	}
	s.srv = &gortsplib.Server{
		Handler:     (*handler)(s),
		RTSPAddress: cfg.Address,
		AuthMethods: []auth.Method{auth.MethodDigestMD5},
	}
	return s
}

// Start listens on the configured address. Only TCP (interleaved) transport is offered.
func (s *Server) Start() error {
	s.videoEnc = &rtph264.Encoder{PayloadType: videoPayloadType, PacketizationMode: 1}
	if err := s.videoEnc.Init(); err != nil {
		return err
	}
	s.audioEnc = &rtpmpeg4audio.Encoder{PayloadType: audioPayloadType, SizeLength: 13, IndexLength: 3, IndexDeltaLength: 3}
	if err := s.audioEnc.Init(); err != nil {
		return err
	}
	if err := s.srv.Start(); err != nil {
		return err
	}
	now := time.Now()
	s.mu.Lock()
	s.goOffline(now)
	s.tickLocked(now)
	s.mu.Unlock()
	s.stop = make(chan struct{})
	s.wg.Go(s.run)
	return nil
}

func (s *Server) run() {
	t := time.NewTicker(s.cfg.FillerInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case now := <-t.C:
			s.mu.Lock()
			s.tickLocked(now)
			s.mu.Unlock()
		}
	}
}

// tickLocked detects stale video and sends a filler frame while offline.
func (s *Server) tickLocked(now time.Time) {
	if !s.offline && now.Sub(s.lastLive) > s.cfg.StaleAfter {
		s.goOffline(s.lastLive)
	}
	if !s.offline {
		return
	}
	au := s.fillerAU(now)
	s.updateParams(au)
	s.sendVideo(au, s.advance(s.nextOut(now), now), true, now)
}

// Offline switches to filler at once, e.g. when the DVR session ended.
func (s *Server) Offline() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.offline {
		s.goOffline(s.lastLive)
		s.tickLocked(time.Now())
	}
}

// Status reports whether the stream shows filler, and since when.
func (s *Server) Status() (offline bool, since time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.offline, s.offlineSince
}

func (s *Server) goOffline(since time.Time) {
	if !s.lastLive.IsZero() {
		s.log.Warn("no video from the DVR, sending filler", "since", since.Format(time.TimeOnly))
	}
	s.offline, s.offlineSince = true, since
}

// fillerAU returns the filler access unit, rendered again only when its text (once a minute) or size changes.
func (s *Server) fillerAU(now time.Time) [][]byte {
	var text placeholder.Text
	if s.cfg.Filler.Text {
		text = placeholder.Offline(s.cfg.Name, s.offlineSince, now, placeholder.ReasonNoDVR)
	}
	w, h := s.info.Width, s.info.Height
	key := fmt.Sprintf("%dx%d %q", w, h, text)
	f := &s.filler
	if f.key != key {
		img := placeholder.Render(s.cfg.Filler, w, h, text)
		for i := range f.au {
			var au h264.AnnexB
			_ = au.Unmarshal(h264pcm.Encode(img, uint(i)))
			f.au[i] = au
		}
		f.key = key
	}
	f.n++
	return f.au[f.n%2]
}

// nextOut is the output time that continues the timeline by the wall time elapsed since its last frame.
func (s *Server) nextOut(now time.Time) int64 {
	if s.lastOutWall.IsZero() {
		return 0
	}
	return s.lastOut + now.Sub(s.lastOutWall).Microseconds()
}

// advance records out as the latest output time, keeping the timeline strictly increasing.
func (s *Server) advance(out int64, now time.Time) int64 {
	if !s.lastOutWall.IsZero() && out <= s.lastOut {
		out = s.lastOut + 1
	}
	s.lastOut, s.lastOutWall = out, now
	return out
}

// Addr returns the listen address, useful with port 0.
func (s *Server) Addr() string { return s.srv.NetListener().Addr().String() }

// Close stops the server and drops every session.
func (s *Server) Close() {
	if s.stop != nil {
		close(s.stop)
		s.wg.Wait()
	}
	s.srv.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.describe != nil {
		s.describe.Close()
		s.describe = nil
	}
}

// VideoInfo describes the stream as seen so far; fields stay zero until known.
type VideoInfo struct {
	Width, Height int
	FPS           float64 // smoothed from the frame timestamps
	GOP           int     // frames in the last complete IDR interval
}

// LastIDR returns the Annex-B payload (SPS, PPS, IDR slice) of the latest IDR frame, nil before the
// first one. The caller must not modify it.
func (s *Server) LastIDR() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastIDR
}

// VideoInfo returns the current stream parameters.
func (s *Server) VideoInfo() VideoInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// WriteFrame feeds a DVR frame of this channel, or a DVR audio frame. It does not block on clients.
func (s *Server) WriteFrame(f *dvrproto.Frame) {
	switch {
	case f.IsVideo():
		s.writeVideo(f)
	case f.Kind == dvrproto.KindAudio && s.cfg.Audio:
		s.writeAudio(f)
	}
}

func (s *Server) writeVideo(f *dvrproto.Frame) {
	var au h264.AnnexB
	if err := au.Unmarshal(f.Payload); err != nil {
		s.log.Debug("dropping video frame", "err", err)
		return
	}
	idr := f.Kind == dvrproto.KindIDR || h264.IsRandomAccess(au)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.offline {
		if !idr {
			return
		}
		if !s.lastLive.IsZero() {
			s.log.Info("video from the DVR again, live", "offline_for", now.Sub(s.offlineSince).Round(time.Second))
		}
		s.offline, s.anchor = false, true
		s.clock, s.lastUs = clock{}, 0 // the DVR counter may have jumped
	}
	s.lastLive = now
	s.countLive(len(f.Payload), idr, now)
	ext := s.clock.micros(f.Micros)
	s.updateInfo(f, ext, idr)
	if idr {
		s.updateParams(au)
		s.lastIDR = f.Payload
	}
	if d := ext - s.lastVideoExt; s.anchor || d <= 0 || d > maxFrameGapUs {
		s.offset = s.nextOut(now) - ext
		s.anchor = false
	}
	s.lastVideoExt = ext
	s.sendVideo(au, s.advance(s.offset+ext, now), idr, now)
}

// sendVideo packetizes an access unit at output time out (microseconds); called with s.mu held.
func (s *Server) sendVideo(au [][]byte, out int64, idr bool, now time.Time) {
	if len(s.readers) == 0 {
		return
	}
	pkts, err := s.videoEnc.Encode(au)
	if err != nil {
		s.log.Warn("h264 packetization failed", "err", err)
		return
	}
	ts := uint32(out * 90_000 / 1_000_000)
	for _, p := range pkts {
		p.Timestamp = ts
	}
	for ss, r := range s.readers {
		if !r.started && !idr {
			continue
		}
		s.writeTo(r.stream, r.video, pkts, now)
		// A packet counts only once the session is active (after PLAY), so this marks the first IDR it got.
		if !r.started && r.stream.Stats().OutboundRTPPackets > 0 {
			r.started = true
			s.log.Debug("rtsp reader started at an IDR frame", "remote", remote(ss))
		}
	}
}

func (s *Server) writeAudio(f *dvrproto.Frame) {
	var adts mpeg4audio.ADTSPackets
	if err := adts.Unmarshal(f.Payload); err != nil {
		s.log.Debug("dropping audio frame", "err", err)
		return
	}
	cfg := &mpeg4audio.AudioSpecificConfig{Type: adts[0].Type, SampleRate: adts[0].SampleRate, ChannelConfig: adts[0].ChannelConfig}
	aus := make([][]byte, len(adts))
	for i, p := range adts {
		aus[i] = p.AU
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.offline || s.anchor {
		return // no audio without live video: its timeline follows the video's
	}
	out := s.offset + s.clock.micros(f.Micros)
	if s.aac == nil || *s.aac != *cfg {
		s.log.Info("audio config", "type", cfg.Type, "rate", cfg.SampleRate, "channel_config", cfg.ChannelConfig)
		s.aac = cfg
		s.resetDescribe()
	}
	if len(s.readers) == 0 {
		return
	}
	pkts, err := s.audioEnc.Encode(aus)
	if err != nil {
		s.log.Warn("aac packetization failed", "err", err)
		return
	}
	base := uint32(out * int64(cfg.SampleRate) / 1_000_000)
	for _, p := range pkts {
		p.Timestamp += base
	}
	now := time.Now()
	for _, r := range s.readers {
		if r.started && r.audio != nil {
			s.writeTo(r.stream, r.audio, pkts, now)
		}
	}
}

func (s *Server) writeTo(st *gortsplib.ServerStream, m *description.Media, pkts []*rtp.Packet, ntp time.Time) {
	for _, p := range pkts {
		if err := st.WritePacketRTPWithNTP(m, p, ntp); err != nil {
			if !errors.As(err, &liberrors.ErrServerStreamClosed{}) {
				s.log.Debug("rtsp write failed", "err", err)
			}
			return
		}
	}
}

// updateInfo tracks resolution, frame rate and GOP length; called with s.mu held.
func (s *Server) updateInfo(f *dvrproto.Frame, us int64, idr bool) {
	s.info.Width, s.info.Height = int(f.Width), int(f.Height)
	// Averaging the gaps, not the rates: the DVR's frame gaps vary a lot, and 1/gap would overweight the short ones.
	if d := us - s.lastUs; s.lastUs != 0 && d > 0 && d < 2_000_000 {
		if s.avgGapUs == 0 {
			s.avgGapUs = float64(d)
		} else {
			s.avgGapUs += (float64(d) - s.avgGapUs) / 32
		}
		s.info.FPS = 1e6 / s.avgGapUs
	}
	s.lastUs = us
	if idr {
		if s.sinceIDR > 0 {
			s.info.GOP = s.sinceIDR
		}
		s.sinceIDR = 0
	}
	s.sinceIDR++
}

// updateParams takes SPS and PPS from an IDR access unit; called with s.mu held.
func (s *Server) updateParams(au [][]byte) {
	var sps, pps []byte
	for _, n := range au {
		switch h264.NALUType(n[0] & 0x1f) {
		case h264.NALUTypeSPS:
			sps = n
		case h264.NALUTypePPS:
			pps = n
		}
	}
	if sps == nil || pps == nil || bytes.Equal(sps, s.sps) && bytes.Equal(pps, s.pps) {
		return
	}
	s.log.Info("h264 parameters", "first", s.sps == nil, "sps_bytes", len(sps), "pps_bytes", len(pps))
	s.sps, s.pps = bytes.Clone(sps), bytes.Clone(pps)
	s.resetDescribe()
}

// resetDescribe drops the DESCRIBE stream so the next DESCRIBE shows the new parameters.
func (s *Server) resetDescribe() {
	if s.describe != nil {
		s.describe.Close()
		s.describe = nil
	}
}

func (s *Server) ready() bool { return s.sps != nil && (!s.cfg.Audio || s.aac != nil) }

// newStream builds a stream from the current parameters; called with s.mu held.
func (s *Server) newStream() (*gortsplib.ServerStream, *description.Media, *description.Media, error) {
	video := &description.Media{
		Type:    description.MediaTypeVideo,
		Formats: []format.Format{&format.H264{PayloadTyp: videoPayloadType, SPS: s.sps, PPS: s.pps, PacketizationMode: 1}},
	}
	desc := &description.Session{Medias: []*description.Media{video}}
	var audio *description.Media
	if s.cfg.Audio {
		cfg := *s.aac
		audio = &description.Media{
			Type: description.MediaTypeAudio,
			Formats: []format.Format{&format.MPEG4Audio{
				PayloadTyp: audioPayloadType, Config: &cfg, SizeLength: 13, IndexLength: 3, IndexDeltaLength: 3,
			}},
		}
		desc.Medias = append(desc.Medias, audio)
	}
	st := &gortsplib.ServerStream{Server: s.srv, Desc: desc}
	if err := st.Initialize(); err != nil {
		return nil, nil, nil, err
	}
	return st, video, audio, nil
}

func (s *Server) pathMatches(p string) bool {
	return strings.Trim(p, "/") == strings.Trim(s.cfg.Path, "/")
}

// handler keeps the gortsplib callbacks off the Server's exported method set.
type handler Server

func (h *handler) authorized(conn *gortsplib.ServerConn, req *base.Request) bool {
	return h.cfg.User == "" || conn.VerifyCredentials(req, h.cfg.User, h.cfg.Password)
}

func (h *handler) OnDescribe(ctx *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	s := (*Server)(h)
	if !h.authorized(ctx.Conn, ctx.Request) {
		return &base.Response{StatusCode: base.StatusUnauthorized}, nil, liberrors.ErrServerAuth{}
	}
	if !s.pathMatches(ctx.Path) {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ready() {
		// No SDP without SPS/PPS: clients retry, and get a correct one after the next IDR.
		return &base.Response{StatusCode: base.StatusServiceUnavailable}, nil, nil
	}
	if s.describe == nil {
		st, _, _, err := s.newStream()
		if err != nil {
			return &base.Response{StatusCode: base.StatusInternalServerError}, nil, err
		}
		s.describe = st
	}
	return &base.Response{StatusCode: base.StatusOK}, s.describe, nil
}

func (h *handler) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	s := (*Server)(h)
	if !h.authorized(ctx.Conn, ctx.Request) {
		return &base.Response{StatusCode: base.StatusUnauthorized}, nil, liberrors.ErrServerAuth{}
	}
	if ctx.Session.State() == gortsplib.ServerSessionStatePreRecord {
		return &base.Response{StatusCode: base.StatusMethodNotAllowed}, nil, nil
	}
	if !s.pathMatches(ctx.Path) {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.readers[ctx.Session]; ok {
		return &base.Response{StatusCode: base.StatusOK}, r.stream, nil
	}
	if !s.ready() {
		return &base.Response{StatusCode: base.StatusServiceUnavailable}, nil, nil
	}
	st, video, audio, err := s.newStream()
	if err != nil {
		return &base.Response{StatusCode: base.StatusInternalServerError}, nil, err
	}
	s.readers[ctx.Session] = &reader{stream: st, video: video, audio: audio}
	return &base.Response{StatusCode: base.StatusOK}, st, nil
}

func (h *handler) OnPlay(*gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	return &base.Response{StatusCode: base.StatusOK}, nil
}

func (h *handler) OnAnnounce(*gortsplib.ServerHandlerOnAnnounceCtx) (*base.Response, error) {
	return &base.Response{StatusCode: base.StatusMethodNotAllowed}, nil
}

func (h *handler) OnSessionOpen(ctx *gortsplib.ServerHandlerOnSessionOpenCtx) {
	h.log.Info("rtsp session opened", "remote", ctx.Conn.NetConn().RemoteAddr().String())
}

func remote(ss *gortsplib.ServerSession) string {
	for _, c := range ss.Conns() {
		return c.NetConn().RemoteAddr().String()
	}
	return ""
}

func (h *handler) OnSessionClose(ctx *gortsplib.ServerHandlerOnSessionCloseCtx) {
	s := (*Server)(h)
	s.mu.Lock()
	r, ok := s.readers[ctx.Session]
	delete(s.readers, ctx.Session)
	s.mu.Unlock()
	if ok {
		r.stream.Close()
	}
	h.log.Info("rtsp session closed", "remote", remote(ctx.Session), "err", ctx.Error)
}

// clock extends the DVR's 32-bit microsecond timestamps to 64 bits across wraps. Video and audio
// share it, as the DVR stamps both from the same clock. Small backward steps (audio arriving behind
// video) are kept as negative deltas.
type clock struct {
	started bool
	last    uint32
	ext     int64
}

func (c *clock) micros(m uint32) int64 {
	if !c.started {
		c.started, c.last = true, m
		return 0
	}
	c.ext += int64(int32(m - c.last))
	c.last = m
	return c.ext
}
