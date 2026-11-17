// SPDX-License-Identifier: GPL-3.0-or-later

package genconfig

import (
	"strings"
	"testing"

	"github.com/andras-tim/dvr-squirrel/internal/config"
)

func TestWriteParses(t *testing.T) {
	for _, n := range []int{4, 8} {
		var b strings.Builder
		err := Write(&b, Input{Host: "dvr.example.lan", Port: 8000, User: "viewer", PasswordFile: "/run/secret/pw",
			Channels: n, Model: "DVR 500 series, 4 channels", TypeDesc: "DVR 500 series, 4 channels (type 504)"})
		if err != nil {
			t.Fatal(err)
		}
		out := b.String()
		if strings.Contains(out, "secretvalue") {
			t.Fatal("password in the output")
		}
		env := func(k string) (string, bool) { return "secretvalue", k == config.EnvPassword }
		cfg, err := config.Parse([]byte(out), t.TempDir(), env)
		if err != nil {
			t.Fatalf("%d channels: %v\n%s", n, err, out)
		}
		chs := cfg.Enabled()
		if len(chs) != n || chs[n-1].Name != "CH"+string(rune('0'+n)) || chs[0].Model != "DVR 500 series, 4 channels" {
			t.Fatalf("%d channels: %+v", n, chs)
		}
		// shared address: distinct ports per channel
		if chs[1].ONVIFPort != 8082 || chs[1].RTSPPort != 8552 {
			t.Fatalf("ports %+v", chs[1])
		}
	}
}
