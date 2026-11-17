// SPDX-License-Identifier: GPL-3.0-or-later

//go:build unix

package discovery

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// reuseAddrPort lets the socket share UDP 3702 with other WS-Discovery responders on the host (e.g. wsdd).
func reuseAddrPort(_, _ string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
		if serr == nil {
			serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
		}
	})
	if err != nil {
		return err
	}
	return serr
}
