// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"runtime/debug"
	_ "time/tzdata" // the TZ variable works in minimal images without a zoneinfo database
)

// buildInfo returns version, commit and date: the stamped values, else what the Go toolchain recorded
// (the module version for `go install ...@v1.2.3`, the VCS revision for a build from a checkout).
func buildInfo() (v, c, d string) {
	v, c, d = version, commit, date
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return orDefault(v, "devel"), c, d
	}
	if v == "" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		v = info.Main.Version
	}
	dirty := false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if c == "" {
				c = s.Value
			}
		case "vcs.time":
			if d == "" {
				d = s.Value
			}
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if v == "" {
		v = "devel"
		if dirty {
			v += "+dirty"
		}
	}
	return v, c, d
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// versionString is the one-line version for --version and the startup log.
func versionString() string {
	v, c, d := buildInfo()
	out := "dvr-squirrel " + v
	if c != "" {
		if len(c) > 12 {
			c = c[:12]
		}
		out += " (" + c
		if d != "" {
			out += ", " + d
		}
		out += ")"
	}
	return out
}
