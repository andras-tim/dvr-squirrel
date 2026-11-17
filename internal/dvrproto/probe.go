// SPDX-License-Identifier: GPL-3.0-or-later

package dvrproto

import (
	"context"
	"fmt"
	"net"
	"time"
)

// Probe logs in once, returns the channel count from the login reply, and logs out again.
func Probe(ctx context.Context, addr, user, password string, timeout time.Duration) (int, error) {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(LoginRequest(user, password)); err != nil {
		return 0, fmt.Errorf("send login: %w", err)
	}
	r := NewReader(conn)
	for {
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
		_, _ = conn.Write(LogoutRequest())
		return reply.Arg, nil
	}
}
