// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/andras-tim/dvr-squirrel/internal/config"
	"github.com/andras-tim/dvr-squirrel/internal/devtype"
	"github.com/andras-tim/dvr-squirrel/internal/discovery"
	"github.com/andras-tim/dvr-squirrel/internal/dvrproto"
	"github.com/andras-tim/dvr-squirrel/internal/dvrweb"
	"github.com/andras-tim/dvr-squirrel/internal/ffmpeg"
	"github.com/andras-tim/dvr-squirrel/internal/httpauth"
	"github.com/andras-tim/dvr-squirrel/internal/logring"
	"github.com/andras-tim/dvr-squirrel/internal/onvif"
	"github.com/andras-tim/dvr-squirrel/internal/placeholder"
	"github.com/andras-tim/dvr-squirrel/internal/rtsp"
	"github.com/andras-tim/dvr-squirrel/internal/snapshot"
	"github.com/andras-tim/dvr-squirrel/internal/webui"
)

type channel struct {
	cfg   config.Channel
	rtsp  *rtsp.Server
	onvif *onvif.Device
	http  *http.Server
	ln    net.Listener
}

// dvrState is the DVR media session as shown on the status page.
type dvrState struct {
	mu       sync.Mutex
	online   bool
	since    time.Time
	channels int
	lastErr  string
	typeCode string
	typeOK   bool // typeCode was read (it may be empty)
}

func (d *dvrState) login(n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.online, d.since, d.channels = true, time.Now(), n
}

func (d *dvrState) end(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.online || d.since.IsZero() {
		d.since = time.Now()
	}
	d.online = false
	if err != nil {
		d.lastErr = err.Error()
	}
}

// bridge runs the channels, the DVR client, discovery and the web UI.
type bridge struct {
	cfg    *config.Config
	log    *slog.Logger
	ring   *logring.Ring
	ffmpeg string
	web    *dvrweb.Client

	ctx    context.Context
	cancel context.CancelCauseFunc
	wg     sync.WaitGroup
	state  dvrState

	mu      sync.RWMutex
	chans   []*channel
	byIndex map[int]*channel
	started bool
}

// ring may be nil (no recent events on the status page).
func run(ctx context.Context, cfg *config.Config, log *slog.Logger, ring *logring.Ring) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	b := &bridge{cfg: cfg, log: log, ring: ring, ctx: ctx, cancel: cancel, byIndex: map[int]*channel{},
		web: &dvrweb.Client{BaseURL: cfg.DVR.WebURL()}}
	b.state.since = time.Now()
	defer b.closeChannels()

	if cfg.Snapshot.Enabled {
		p, note := ffmpeg.Resolve(cfg.Snapshot.FFmpeg)
		if p == "" {
			log.Warn("no ffmpeg, snapshots are placeholders", "ffmpeg", note)
		} else {
			log.Info("snapshot decoder", "ffmpeg", p, "source", note)
		}
		b.ffmpeg = p
	}

	if !cfg.Auto {
		if err := b.startChannels(cfg.Enabled()); err != nil {
			return err
		}
	} else {
		log.Info("no [[channel]] configured: the channels start once the DVR reports its channel count")
	}

	var web *http.Server
	if cfg.Web.Enabled {
		ln, err := net.Listen("tcp", cfg.Web.Listen)
		if err != nil {
			return fmt.Errorf("web ui: %w", err)
		}
		ui := webui.New(webui.Config{
			DVR:      b.web,
			Channels: config.MaxChannels,
			Status:   b.status,
			Log:      log.With("component", "web"),
		})
		web = &http.Server{Handler: ui, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second}
		b.wg.Go(func() {
			if err := web.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				cancel(fmt.Errorf("web ui: %w", err))
			}
		})
		log.Info("web ui ready", "listen", ln.Addr().String())
	}

	client := &dvrproto.Client{
		Addr: cfg.DVR.Addr(), User: cfg.DVR.User, Password: cfg.DVR.Password,
		MinBackoff:  cfg.DVR.Reconnect,
		MaxBackoff:  cfg.DVR.ReconnectMax,
		ReadTimeout: dvrReadTimeout,
		Logger:      log.With("component", "dvr"),
		OnSessionEnd: func(err error) {
			b.state.end(err)
			for _, c := range b.channels() {
				c.rtsp.Offline()
			}
		},
		OnLogin: b.onLogin,
	}
	b.wg.Go(func() { _ = client.Run(ctx, b.route) })

	log.Info("running", "version", versionString(), "channels", len(b.channels()))
	<-ctx.Done()
	log.Info("shutting down", "reason", context.Cause(ctx))

	shutdownCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if web != nil {
		_ = web.Shutdown(shutdownCtx)
	}
	for _, c := range b.channels() {
		_ = c.http.Shutdown(shutdownCtx)
	}
	b.wg.Wait()

	if err := context.Cause(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func (b *bridge) channels() []*channel {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.chans
}

func (b *bridge) closeChannels() {
	for _, c := range b.channels() {
		c.rtsp.Close()
	}
}

// route passes a DVR frame to its channel; audio goes to every channel (each drops it unless audio is on).
func (b *bridge) route(f *dvrproto.Frame) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if f.IsVideo() {
		if c := b.byIndex[int(f.Channel)+1]; c != nil {
			c.rtsp.WriteFrame(f)
		}
		return
	}
	for _, c := range b.chans {
		c.rtsp.WriteFrame(f)
	}
}

// onLogin runs on the DVR client's goroutine after each login.
func (b *bridge) onLogin(n int) {
	b.state.login(n)
	b.mu.RLock()
	started := b.started
	b.mu.RUnlock()
	if !started {
		// The channels need the model before NVRs see them, so the first detection is waited for (bounded).
		b.detect(n)
		if err := b.startChannels(b.cfg.ChannelsFor(n)); err != nil {
			b.cancel(err)
		}
		return
	}
	for _, c := range b.channels() {
		if c.cfg.Index > n {
			b.log.Warn("channel beyond the DVR's channel count", "channel", c.cfg.Index, "dvr_channels", n)
		}
	}
	b.state.mu.Lock()
	known := b.state.typeOK
	b.state.mu.Unlock()
	if !known {
		go b.detect(n)
	}
}

// detect reads the DVR's type from Port.xml and sets the model of the channels without a configured one.
func (b *bridge) detect(n int) {
	ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
	defer cancel()
	code, _, err := devtype.Detect(ctx, b.web, dvrweb.Credentials{User: b.cfg.DVR.User, Password: b.cfg.DVR.Password})
	if err != nil {
		b.log.Warn("cannot read the DVR's type (Port.xml)", "err", err)
		return
	}
	b.state.mu.Lock()
	b.state.typeCode, b.state.typeOK = code, true
	b.state.mu.Unlock()
	b.log.Info("dvr type", "type", devtype.Describe(code, n))
	model := devtype.Model(code, n)
	for _, c := range b.channels() {
		if c.cfg.Model == "" {
			c.onvif.SetModel(model)
		}
	}
}

// defaultModel is the model of channels without a configured one until the DVR's type is read.
func (b *bridge) defaultModel() string {
	b.state.mu.Lock()
	defer b.state.mu.Unlock()
	return devtype.Model(b.state.typeCode, max(b.state.channels, len(b.cfg.Enabled())))
}

func (b *bridge) startChannels(ccs []config.Channel) error {
	var started []*channel
	for _, cc := range ccs {
		c, err := b.startChannel(cc, b.log.With("channel", cc.Index))
		if err != nil {
			for _, s := range started {
				s.rtsp.Close()
				_ = s.ln.Close()
			}
			return fmt.Errorf("channel %d: %w", cc.Index, err)
		}
		started = append(started, c)
	}
	b.mu.Lock()
	b.chans = append(b.chans, started...)
	for _, c := range started {
		b.byIndex[c.cfg.Index] = c
	}
	b.started = true
	b.mu.Unlock()

	for _, c := range started {
		b.wg.Go(func() {
			if err := c.http.Serve(c.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				b.cancel(fmt.Errorf("channel %d onvif: %w", c.cfg.Index, err))
			}
		})
	}
	if b.cfg.Discovery.Enabled && len(started) > 0 {
		r := &discovery.Responder{Interface: b.cfg.Discovery.Interface, Log: b.log.With("component", "discovery")}
		for _, c := range started {
			r.Devices = append(r.Devices, discovery.Device{
				EndpointUUID: c.onvif.EndpointUUID(), Scopes: c.onvif.Scopes, IP: c.cfg.ListenIP, Port: c.cfg.ONVIFPort,
			})
		}
		b.wg.Go(func() {
			if err := r.Run(b.ctx); err != nil {
				b.cancel(fmt.Errorf("discovery: %w", err))
			}
		})
	}
	return nil
}

func (b *bridge) status() webui.Status {
	b.state.mu.Lock()
	st := webui.Status{DVR: webui.DVRStatus{Addr: b.cfg.DVR.Addr(), Online: b.state.online, Since: b.state.since,
		Channels: b.state.channels, LastError: b.state.lastErr}}
	if b.state.typeOK && b.state.channels > 0 {
		st.DVR.Type = devtype.Describe(b.state.typeCode, b.state.channels)
	}
	b.state.mu.Unlock()
	for _, c := range b.channels() {
		v, s := c.rtsp.VideoInfo(), c.rtsp.Stats()
		off, since := c.rtsp.Status()
		st.Channels = append(st.Channels, webui.ChannelStatus{
			Index: c.cfg.Index, Name: c.cfg.Name, Offline: off, OfflineSince: since,
			Width: v.Width, Height: v.Height, FPS: v.FPS, Kbps: s.Kbps, Frames: s.Frames, LastIDR: s.LastIDR, Clients: s.Clients,
		})
	}
	if b.ring != nil {
		st.Events = b.ring.Entries()
	}
	return st
}

func (b *bridge) startChannel(cc config.Channel, log *slog.Logger) (*channel, error) {
	style := placeholder.Style{Base: cc.FillerImage, Text: cc.FillerText}
	rs := rtsp.New(rtsp.Config{
		Address:    config.ListenAddr(cc.ListenIP, cc.RTSPPort),
		Path:       cc.RTSPPath,
		Audio:      cc.Audio,
		User:       cc.User,
		Password:   cc.Password,
		Name:       cc.Name,
		Filler:     style,
		StaleAfter: dvrReadTimeout,
	}, log.With("component", "rtsp"))
	if err := rs.Start(); err != nil {
		return nil, fmt.Errorf("rtsp: %w", err)
	}

	model := cc.Model
	if model == "" {
		model = b.defaultModel()
	}
	dev := &onvif.Device{
		Name: cc.Name, Model: model, Firmware: cc.Firmware, Serial: cc.Serial,
		User: cc.User, Password: cc.Password,
		IP: cc.ListenIP, ONVIFPort: cc.ONVIFPort, RTSPPort: cc.RTSPPort, RTSPPath: cc.RTSPPath,
		Video: func() onvif.VideoInfo {
			v := rs.VideoInfo()
			return onvif.VideoInfo{Width: v.Width, Height: v.Height, FPS: v.FPS, GOP: v.GOP}
		},
		Log: log.With("component", "onvif"),
	}
	snap := &snapshot.Snapshotter{
		Name: cc.Name, FFmpeg: b.ffmpeg, CacheFor: b.cfg.Snapshot.CacheFor, LastIDR: rs.LastIDR,
		Size:    func() (int, int) { v := rs.VideoInfo(); return v.Width, v.Height },
		Offline: rs.Status,
		Style:   style,
		Log:     log.With("component", "snapshot"),
	}
	mux := http.NewServeMux()
	mux.Handle(onvif.SnapshotPath, httpauth.New(cc.User, cc.Password, "dvr-squirrel", snap))
	mux.Handle("/", dev)

	addr := config.ListenAddr(cc.ListenIP, cc.ONVIFPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		rs.Close()
		return nil, fmt.Errorf("onvif: %w", err)
	}
	log.Info("channel ready", "name", cc.Name, "model", model, "onvif", addr, "rtsp", rs.Addr()+cc.RTSPPath, "audio", cc.Audio)
	return &channel{
		cfg: cc, rtsp: rs, onvif: dev, ln: ln,
		http: &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second},
	}, nil
}
