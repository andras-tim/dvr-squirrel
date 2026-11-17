// SPDX-License-Identifier: GPL-3.0-or-later

// Package devtype names the DVR from its type code (Port.xml dMainType) and channel count.
package devtype

import (
	"context"
	"fmt"
	"strconv"

	"github.com/andras-tim/dvr-squirrel/internal/dvrweb"
)

// Known type codes: the vendor's PC Viewer recognizes exactly these, all of the same "500" platform.
var known = map[string]int{"504": 4, "508": 8, "516": 16}

// Known reports whether code is a recognized type code, and its channel count.
func Known(code string) (channels int, ok bool) {
	channels, ok = known[code]
	return channels, ok
}

// Model is the default camera model of the channels: "DVR 500 series, 4 channels", or for an unknown code
// "DVR, 4 channels (type 999)", or without a code "DVR, 4 channels".
func Model(code string, channels int) string {
	switch {
	case code == "":
		return "DVR, " + count(channels)
	case isKnown(code):
		return "DVR 500 series, " + count(channels)
	default:
		return fmt.Sprintf("DVR, %s (type %s)", count(channels), code)
	}
}

func count(n int) string {
	if n == 1 {
		return "1 channel"
	}
	return fmt.Sprintf("%d channels", n)
}

// Describe is the DVR's description on the status page: "DVR 500 series, 4 channels (type 504)".
func Describe(code string, channels int) string {
	switch {
	case code == "":
		return fmt.Sprintf("DVR, %s (type unknown)", count(channels))
	case isKnown(code):
		return fmt.Sprintf("DVR 500 series, %s (type %s)", count(channels), code)
	default:
		return fmt.Sprintf("DVR, %s (type %s, not a known one)", count(channels), code)
	}
}

func isKnown(code string) bool { _, ok := known[code]; return ok }

// Detect reads the type code and channel count from Port.xml, which a view-only account may read.
func Detect(ctx context.Context, c *dvrweb.Client, cred dvrweb.Credentials) (code string, channels int, err error) {
	rec, err := c.Read(ctx, cred, "Port", 0)
	if err != nil {
		return "", 0, err
	}
	code, _ = rec.Get("dMainType")
	if v, ok := rec.Get("NUM_CAM"); ok {
		channels, _ = strconv.Atoi(v)
	}
	return code, channels, nil
}
