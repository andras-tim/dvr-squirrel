// SPDX-License-Identifier: GPL-3.0-or-later

package logring

import (
	"log/slog"
	"strings"
	"testing"
)

func TestRing(t *testing.T) {
	var out strings.Builder
	r := New(3, slog.LevelInfo)
	log := slog.New(r.Handler(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})))
	log.Debug("debug only")
	sub := log.With("channel", 2).WithGroup("rtsp")
	for i := range 4 {
		sub.Info("msg", "n", i)
	}
	got := r.Entries()
	if len(got) != 3 || got[0].Attrs != "channel=2 rtsp.n=3" || got[2].Attrs != "channel=2 rtsp.n=1" {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(out.String(), "debug only") || strings.Count(out.String(), "rtsp.n=") != 4 {
		t.Fatalf("next handler output: %s", out.String())
	}
}
