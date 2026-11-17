// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !unix

package discovery

import "syscall"

func reuseAddrPort(_, _ string, _ syscall.RawConn) error { return nil }
