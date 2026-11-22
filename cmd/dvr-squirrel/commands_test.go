// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andras-tim/dvr-squirrel/internal/config"
	"github.com/andras-tim/dvr-squirrel/internal/fakedvr"
)

// fakeDVR starts a media server and a web server for a DVR with n channels and type code.
func fakeDVR(t *testing.T, n int, code string) (*fakedvr.Server, *config.Config) {
	t.Helper()
	media := &fakedvr.Server{User: "viewer", Password: "viewpw", Channels: n}
	if err := media.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(media.Close)
	acc := fakedvr.WebAccount{Password: "viewpw"}
	acc.Perms[8] = true
	web := &fakedvr.WebServer{Accounts: map[string]fakedvr.WebAccount{"viewer": acc}, PagePerm: map[string]int{"Port": 8}}
	web.SetRecord("Port", 0, fakedvr.Field{Name: "p2", Value: ""}, fakedvr.Field{Name: "NUM_CAM", Value: strconv.Itoa(n)},
		fakedvr.Field{Name: "dMainType", Value: code})
	ws := httptest.NewServer(web)
	t.Cleanup(ws.Close)

	host, port, _ := net.SplitHostPort(media.Addr())
	_, webPort, _ := net.SplitHostPort(ws.Listener.Addr().String())
	env := map[string]string{config.EnvHost: host, config.EnvPort: port, config.EnvUser: "viewer", config.EnvPassword: "viewpw"}
	cfg, err := config.Load(config.Options{
		Path:      filepath.Join(t.TempDir(), "none.toml"),
		LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.DVR.WebPort, _ = strconv.Atoi(webPort)
	return media, cfg
}

func TestGenerateConfig(t *testing.T) {
	for _, tt := range []struct {
		n     int
		code  string
		model string
	}{{4, "504", "DVR 500 series, 4 channels"}, {8, "508", "DVR 500 series, 8 channels"}} {
		media, cfg := fakeDVR(t, tt.n, tt.code)
		out, err := renderConfig(t.Context(), cfg, "/etc/dvr-squirrel/dvr-password")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "viewpw") {
			t.Fatal("the password is in the generated config")
		}
		env := func(k string) (string, bool) { return "x", k == config.EnvPassword }
		gen, err := config.Parse(out, t.TempDir(), env)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		chs := gen.Enabled()
		if len(chs) != tt.n || chs[tt.n-1].Name != "CH"+strconv.Itoa(tt.n) || chs[0].Model != tt.model || gen.DVR.User != "viewer" {
			t.Fatalf("%d channels: %+v\n%s", tt.n, chs, out)
		}
		if !strings.Contains(string(out), "(type "+tt.code+")") {
			t.Fatalf("no type comment:\n%s", out)
		}
		waitLogout(t, media)
	}
}

func waitLogout(t *testing.T, s *fakedvr.Server) {
	t.Helper()
	for i := 0; s.Logouts() != 1; i++ {
		if i == 500 {
			t.Fatal("no logout after generate-config")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWriteNewRefusesOverwrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := writeNew(p, []byte("a\n"), false); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(p, []byte("b\n"), false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("overwrite without --force: %v", err)
	}
	if err := writeNew(p, []byte("c\n"), true); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if b, _ := os.ReadFile(p); string(b) != "c\n" || st.Mode().Perm() != 0o640 {
		t.Fatalf("%q %v", b, st.Mode())
	}
}

// TestAutoChannels runs the bridge without a config file: the channels start after the first login, named CH<n>,
// with the model detected from Port.xml.
func TestAutoChannels(t *testing.T) {
	media, cfg := fakeDVR(t, 2, "504")
	media.Frames = fakedvr.Sequence([]uint8{0, 1}, 50, 10, 0, false)
	cfg.Web.Enabled, cfg.Discovery.Enabled = false, false
	if !cfg.Auto {
		t.Fatal("not in auto mode")
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, slog.New(slog.DiscardHandler), nil) }()
	defer func() {
		cancel()
		<-done
	}()

	url := "http://127.0.0.1:8082/onvif/device_service"
	for i := 0; ; i++ {
		if c, err := net.Dial("tcp", "127.0.0.1:8082"); err == nil {
			c.Close()
			break
		}
		if i == 300 {
			t.Fatal("channel 2 never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	var body string
	for i := 0; ; i++ {
		var status int
		status, body = soap(t, url, "viewer", "viewpw", `<GetScopes xmlns="http://www.onvif.org/ver10/device/wsdl"/>`)
		if status == 200 && strings.Contains(body, "DVR%20500%20series,%202%20channels") {
			break
		}
		if i == 300 {
			t.Fatalf("channel 2 never came up with the detected model: %d %s", status, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, body = soap(t, url, "viewer", "viewpw", `<GetDeviceInformation xmlns="http://www.onvif.org/ver10/device/wsdl"/>`); !strings.Contains(body, "<tds:Manufacturer>CH2</tds:Manufacturer>") {
		t.Fatalf("GetDeviceInformation: %s", body)
	}
}
