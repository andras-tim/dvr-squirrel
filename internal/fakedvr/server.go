// SPDX-License-Identifier: GPL-3.0-or-later

package fakedvr

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/andras-tim/dvr-squirrel/internal/dvrproto"
)

// Server is a fake DVR on localhost: it checks the login digest, replies, then writes Frames.
type Server struct {
	User, Password string
	Channels       int
	// Replies are sent before the final reply, e.g. a StatusWait.
	Replies []dvrproto.LoginReply
	Frames  []*dvrproto.Frame
	// Interval paces the frames; zero sends them at once.
	Interval time.Duration
	// CloseAfterFrames closes the connection after the frames instead of idling.
	CloseAfterFrames bool

	ln     net.Listener
	mu     sync.Mutex
	logins int
	logout int
	conns  map[net.Conn]struct{}
	wg     sync.WaitGroup
}

// Start listens on a random localhost port.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.ln = ln
	s.wg.Go(s.accept)
	return nil
}

// Addr is the listen address.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Close stops the listener, drops the connections and waits for their goroutines.
func (s *Server) Close() {
	_ = s.ln.Close()
	s.mu.Lock()
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// Logins and Logouts count the accepted logins and the received logout messages.
func (s *Server) Logins() int  { s.mu.Lock(); defer s.mu.Unlock(); return s.logins }
func (s *Server) Logouts() int { s.mu.Lock(); defer s.mu.Unlock(); return s.logout }

func (s *Server) accept() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.conns == nil {
			s.conns = map[net.Conn]struct{}{}
		}
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		s.wg.Go(func() { s.serve(c) })
	}
}

func reply(status, arg int) []byte {
	b := append([]byte{}, dvrproto.Magic...)
	b = fmt.Appendf(b, "%04X%04X%04X", dvrproto.LoginReplySize, status, arg)
	return append(b, make([]byte, 16)...)
}

func (s *Server) serve(c net.Conn) {
	defer c.Close()
	req := make([]byte, 44)
	if _, err := io.ReadFull(c, req); err != nil {
		return
	}
	for _, r := range s.Replies {
		_, _ = c.Write(reply(r.Status, r.Arg))
	}
	if !bytes.Equal(req, dvrproto.LoginRequest(s.User, s.Password)) {
		_, _ = c.Write(reply(dvrproto.StatusError, dvrproto.CodeIncorrectPassword))
		return
	}
	s.mu.Lock()
	s.logins++
	s.mu.Unlock()
	_, _ = c.Write(reply(dvrproto.StatusOK, s.Channels))

	// Count a logout whenever it arrives, also while frames are still being written.
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, 12)
		if _, err := io.ReadFull(c, b); err == nil && bytes.Equal(b, dvrproto.LogoutRequest()) {
			s.mu.Lock()
			s.logout++
			s.mu.Unlock()
		}
	}()

	for _, f := range s.Frames {
		if _, err := c.Write(f.Marshal()); err != nil {
			return
		}
		if s.Interval > 0 {
			select {
			case <-done:
				return
			case <-time.After(s.Interval):
			}
		}
	}
	if !s.CloseAfterFrames {
		<-done
	}
}
