// SPDX-License-Identifier: GPL-3.0-or-later

package dvrproto_test

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andras-tim/dvr-squirrel/internal/dvrproto"
	"github.com/andras-tim/dvr-squirrel/internal/fakedvr"
)

func startServer(t *testing.T, s *fakedvr.Server) {
	t.Helper()
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestClientStreamsAndLogsOut(t *testing.T) {
	frames := fakedvr.Sequence([]uint8{0, 1, 2, 3}, 15, 5, 0, true)
	srv := &fakedvr.Server{
		User: "admin", Password: "secret", Channels: 4,
		Replies: []dvrproto.LoginReply{{Status: dvrproto.StatusWait}},
		Frames:  frames,
	}
	startServer(t, srv)

	var mu sync.Mutex
	var got []*dvrproto.Frame
	var channels atomic.Int32
	c := &dvrproto.Client{
		Addr: srv.Addr(), User: "admin", Password: "secret",
		OnLogin: func(n int) { channels.Store(int32(n)) },
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- c.Run(ctx, func(f *dvrproto.Frame) {
			mu.Lock()
			got = append(got, f)
			mu.Unlock()
		})
	}()

	waitFor(t, "all frames", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == len(frames) })
	if channels.Load() != 4 {
		t.Fatalf("OnLogin channels = %d", channels.Load())
	}
	for i := range frames {
		if got[i].Micros != frames[i].Micros || got[i].Kind != frames[i].Kind {
			t.Fatalf("frame %d differs", i)
		}
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	waitFor(t, "logout", func() bool { return srv.Logouts() == 1 })
}

func TestClientReconnects(t *testing.T) {
	srv := &fakedvr.Server{
		User: "u", Password: "p", Channels: 4,
		Frames:           fakedvr.Sequence([]uint8{0}, 3, 3, 0, false),
		CloseAfterFrames: true,
	}
	startServer(t, srv)

	var n, ends atomic.Int32
	c := &dvrproto.Client{Addr: srv.Addr(), User: "u", Password: "p", MinBackoff: 20 * time.Millisecond,
		OnSessionEnd: func(error) { ends.Add(1) }}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = c.Run(ctx, func(*dvrproto.Frame) { n.Add(1) }) }()

	waitFor(t, "three sessions", func() bool { return srv.Logins() >= 3 })
	waitFor(t, "session end callbacks", func() bool { return ends.Load() >= 2 })
	if n.Load() < 6 {
		t.Fatalf("got %d frames over %d sessions", n.Load(), srv.Logins())
	}
}

func TestClientWrongPasswordBacksOff(t *testing.T) {
	srv := &fakedvr.Server{User: "u", Password: "right", Channels: 4}
	startServer(t, srv)

	var logins atomic.Int32
	c := &dvrproto.Client{
		Addr: srv.Addr(), User: "u", Password: "wrong",
		MinBackoff: 50 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
		OnLogin: func(int) { logins.Add(1) },
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	err := c.Run(ctx, func(*dvrproto.Frame) {})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run returned %v", err)
	}
	if logins.Load() != 0 || srv.Logins() != 0 || srv.Logouts() != 0 {
		t.Fatalf("logins %d/%d, logouts %d", logins.Load(), srv.Logins(), srv.Logouts())
	}
}

func TestClientCancelWhileWaitingForReply(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			defer c.Close()
			_, _ = io.Copy(io.Discard, c) // never replies to the login
		}
	}()

	c := &dvrproto.Client{Addr: ln.Addr().String(), User: "u", Password: "p", ReadTimeout: time.Minute}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := c.Run(ctx, func(*dvrproto.Frame) {}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run returned %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Run took %s", d)
	}
}

func TestProbe(t *testing.T) {
	srv := &fakedvr.Server{User: "u", Password: "p", Channels: 8, Frames: fakedvr.Sequence([]uint8{0}, 5, 5, 0, false)}
	startServer(t, srv)
	n, err := dvrproto.Probe(t.Context(), srv.Addr(), "u", "p", 2*time.Second)
	if err != nil || n != 8 {
		t.Fatalf("Probe: %d %v", n, err)
	}
	waitFor(t, "logout", func() bool { return srv.Logouts() == 1 })
	var le *dvrproto.LoginError
	if _, err := dvrproto.Probe(t.Context(), srv.Addr(), "u", "wrong", 2*time.Second); !errors.As(err, &le) {
		t.Fatalf("wrong password: %v", err)
	}
}
