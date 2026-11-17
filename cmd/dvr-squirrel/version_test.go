// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"strings"
	"testing"
)

func TestVersionStamped(t *testing.T) {
	old := [3]string{version, commit, date}
	defer func() { version, commit, date = old[0], old[1], old[2] }()
	version, commit, date = "v1.2.3", "0123456789abcdef0123", "2026-11-17T10:00:00Z"
	if got := versionString(); got != "dvr-squirrel v1.2.3 (0123456789ab, 2026-11-17T10:00:00Z)" {
		t.Fatal(got)
	}
}

func TestVersionUnstamped(t *testing.T) {
	old := [3]string{version, commit, date}
	defer func() { version, commit, date = old[0], old[1], old[2] }()
	version, commit, date = "", "", ""
	// a test binary has no module version and no VCS stamp: it reports a devel build
	if got := versionString(); !strings.HasPrefix(got, "dvr-squirrel devel") {
		t.Fatal(got)
	}
}
