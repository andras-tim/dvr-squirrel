// SPDX-License-Identifier: GPL-3.0-or-later

package dvrproto

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Client keeps a logged-in connection to the DVR and reconnects with backoff.
type Client struct {
	Addr     string // host:port
	User     string
	Password string

	// MinBackoff is the first reconnect delay; it doubles after each failed session up to MaxBackoff,
	// and resets once a session has delivered frames.
	MinBackoff time.Duration
	MaxBackoff time.Duration
	// ReadTimeout bounds the silence on an established session; the DVR streams continuously.
	ReadTimeout time.Duration
	DialTimeout time.Duration

	Logger *slog.Logger
	// OnLogin is called after each successful login with the channel count the DVR reports.
	OnLogin func(channels int)
	// OnSessionEnd is called after each failed or ended session, before the reconnect delay.
	OnSessionEnd func(err error)
}

func (c *Client) defaults() {
	if c.MinBackoff <= 0 {
		c.MinBackoff = 2 * time.Second
	}
	if c.MaxBackoff < c.MinBackoff {
		c.MaxBackoff = max(time.Minute, c.MinBackoff)
	}
	if c.ReadTimeout <= 0 {
		c.ReadTimeout = 10 * time.Second
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = 10 * time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
}

// Run connects, logs in and passes every frame to handle until ctx is done, reconnecting on any
// error. handle runs on the reading goroutine and must not block. Run returns ctx.Err().
func (c *Client) Run(ctx context.Context, handle func(*Frame)) error {
	c.defaults()
	backoff := c.MinBackoff
	for {
		frames, err := c.session(ctx, handle)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if c.OnSessionEnd != nil {
			c.OnSessionEnd(err)
		}
		if frames > 0 {
			backoff = c.MinBackoff
		}
		c.Logger.Warn("dvr session ended", "addr", c.Addr, "frames", frames, "err", err, "retry_in", backoff)
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
		backoff = min(backoff*2, c.MaxBackoff)
	}
}

// session runs one connection and returns the number of frames delivered.
func (c *Client) session(ctx context.Context, handle func(*Frame)) (int, error) {
	d := net.Dialer{
		Timeout:         c.DialTimeout,
		KeepAliveConfig: net.KeepAliveConfig{Enable: true, Idle: 15 * time.Second, Interval: 5 * time.Second, Count: 3},
	}
	conn, err := d.DialContext(ctx, "tcp", c.Addr)
	if err != nil {
		return 0, err
	}

	var (
		mu       sync.Mutex
		loggedIn bool
		closed   bool
	)
	// shutdown logs out (to free the DVR's client slot) and closes; it also unblocks a pending read.
	shutdown := func() {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return
		}
		closed = true
		if loggedIn {
			_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
			if _, err := conn.Write(LogoutRequest()); err == nil {
				c.Logger.Info("dvr logout sent", "addr", c.Addr)
			}
		}
		_ = conn.Close()
	}
	stop := context.AfterFunc(ctx, shutdown)
	defer func() {
		stop()
		shutdown()
	}()

	_ = conn.SetWriteDeadline(time.Now().Add(c.ReadTimeout))
	if _, err := conn.Write(LoginRequest(c.User, c.Password)); err != nil {
		return 0, fmt.Errorf("send login: %w", err)
	}
	_ = conn.SetWriteDeadline(time.Time{})

	r := NewReader(conn)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(c.ReadTimeout))
		reply, err := r.ReadLoginReply()
		if err != nil {
			return 0, fmt.Errorf("read login reply: %w", err)
		}
		if reply.Status == StatusWait {
			continue
		}
		if err := reply.Err(); err != nil {
			return 0, err
		}
		mu.Lock()
		loggedIn = !closed
		mu.Unlock()
		c.Logger.Info("dvr logged in", "addr", c.Addr, "channels", reply.Arg)
		if c.OnLogin != nil {
			c.OnLogin(reply.Arg)
		}
		break
	}

	frames := 0
	for {
		_ = conn.SetReadDeadline(time.Now().Add(c.ReadTimeout))
		f, err := r.Next()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				err = fmt.Errorf("no data for %s: %w", c.ReadTimeout, err)
			}
			return frames, err
		}
		frames++
		handle(f)
	}
}
