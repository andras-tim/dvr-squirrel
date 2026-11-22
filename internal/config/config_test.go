// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"image"
	"image/png"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func noEnv(string) (string, bool) { return "", false }

func writeSecrets(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, v := range map[string]string{"dvr-pw": "dvrsecret\n", "onvif-pw": "onvifsecret\n", "empty": "\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const base = `
[dvr]
host = "dvr.example.lan"
user = "admin"
password_file = "dvr-pw"
`

func TestParseDefaultsAndOverrides(t *testing.T) {
	dir := writeSecrets(t)
	cfg, err := Parse([]byte(base+`
[defaults]
user = "viewer"
password_file = "onvif-pw"
model = "Model X"

[[channel]]
index = 2
name = "Yard"
listen_ip = "192.0.2.12"
audio = true

[[channel]]
index = 1
listen_ip = "192.0.2.11"
rtsp_port = 8554
model = "Model Y"
mac = "02-00-5E-00-53-01"

[[channel]]
index = 4
enabled = false
`), dir, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DVR.Password != "dvrsecret" || cfg.DVR.Port != 8000 || cfg.DVR.Reconnect != 5*time.Second || cfg.DVR.ReconnectMax != 10*time.Second {
		t.Fatalf("dvr: %+v", cfg.DVR)
	}
	if cfg.Web != (Web{Enabled: true, Listen: ":8090"}) || cfg.DVR.WebURL() != "http://dvr.example.lan:80" {
		t.Fatalf("web: %+v %s", cfg.Web, cfg.DVR.WebURL())
	}
	if cfg.Snapshot != (Snapshot{Enabled: true, FFmpeg: "", CacheFor: 5 * time.Second}) {
		t.Fatalf("snapshot: %+v", cfg.Snapshot)
	}
	if cfg.DVR.Addr() != "dvr.example.lan:8000" || !cfg.Discovery.Enabled {
		t.Fatalf("addr %s, discovery %+v", cfg.DVR.Addr(), cfg.Discovery)
	}
	if len(cfg.Channels) != 3 || len(cfg.Enabled()) != 2 {
		t.Fatalf("channels %d, enabled %d", len(cfg.Channels), len(cfg.Enabled()))
	}
	c1, c2 := cfg.Channels[0], cfg.Channels[1]
	if c1.Index != 1 || c1.Name != "CH1" || c1.RTSPPort != 8554 || c1.Model != "Model Y" || c1.Audio {
		t.Fatalf("channel 1: %+v", c1)
	}
	if c2.Name != "Yard" || c2.RTSPPort != 554 || c2.ONVIFPort != 80 || c2.Model != "Model X" || !c2.Audio {
		t.Fatalf("channel 2: %+v", c2)
	}
	if c2.ListenIP != netip.MustParseAddr("192.0.2.12") || c2.User != "viewer" || c2.Password != "onvifsecret" {
		t.Fatalf("channel 2: %+v", c2)
	}
	if c1.Serial == c2.Serial || !strings.HasSuffix(c2.Serial, "-CH2") {
		t.Fatalf("serials %q %q", c1.Serial, c2.Serial)
	}
	if c1.MAC.String() != "02:00:5e:00:53:01" || c2.MAC != nil {
		t.Fatalf("macs %v %v", c1.MAC, c2.MAC)
	}
	if !c1.FillerText || c1.FillerImage != nil {
		t.Fatalf("filler defaults: %+v", c1)
	}
	if cfg.Channels[2].Enabled || cfg.Channels[2].Password != "dvrsecret" {
		t.Fatalf("channel 4: %+v", cfg.Channels[2])
	}
}

func TestPasswordEnv(t *testing.T) {
	env := func(k string) (string, bool) {
		if k == PasswordEnv {
			return "fromenv", true
		}
		return "", false
	}
	cfg, err := Parse([]byte(`
[dvr]
host = "dvr.example.lan"
user = "admin"
[[channel]]
index = 1
`), t.TempDir(), env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DVR.Password != "fromenv" || cfg.Channels[0].User != "admin" || cfg.Channels[0].Password != "fromenv" {
		t.Fatalf("dvr %+v, channel %+v", cfg.DVR, cfg.Channels[0])
	}
}

func TestParseErrors(t *testing.T) {
	dir := writeSecrets(t)
	tests := map[string]struct {
		conf string
		want string
	}{
		"unknown key":   {base + "[[channel]]\nindex = 1\ncolour = 1\n", "unknown keys: channel.colour"},
		"all disabled":  {base + "[[channel]]\nindex = 1\nenabled = false\n", "no enabled channel"},
		"index":         {base + "[[channel]]\nindex = 17\n", "index 17 out of range"},
		"duplicate":     {base + "[[channel]]\nindex = 1\nlisten_ip = \"192.0.2.1\"\n[[channel]]\nindex = 1\n", "configured twice"},
		"bad ip":        {base + "[[channel]]\nindex = 1\nlisten_ip = \"dvr.example.lan\"\n", "not an IP address"},
		"port clash":    {base + "[[channel]]\nindex = 1\nlisten_ip = \"192.0.2.2\"\n[[channel]]\nindex = 2\nlisten_ip = \"192.0.2.2\"\n", "both listen on port 554"},
		"rtsp=onvif":    {base + "[[channel]]\nindex = 1\nlisten_ip = \"192.0.2.1\"\nonvif_port = 554\n", "both listen on port 554"},
		"bad mac":       {base + "[[channel]]\nindex = 1\nmac = \"02:00:5e\"\n", "not a 6-byte MAC"},
		"multicast mac": {base + "[[channel]]\nindex = 1\nmac = \"01:00:5e:00:00:01\"\n", "not a unicast"},
		"same mac": {base + "[[channel]]\nindex = 1\nlisten_ip = \"192.0.2.1\"\nmac = \"02:00:5e:00:53:01\"\n" +
			"[[channel]]\nindex = 2\nlisten_ip = \"192.0.2.2\"\nmac = \"02:00:5e:00:53:01\"\n", "both have mac"},
		"mac in defaults": {base + "[defaults]\nmac = \"02:00:5e:00:53:01\"\n[[channel]]\nindex = 1\n", "unknown keys: defaults.mac"},
		"path":            {base + "[[channel]]\nindex = 1\nrtsp_path = \"stream\"\n", "rtsp_path"},
		"empty secret":    {base + "[[channel]]\nindex = 1\npassword_file = \"empty\"\n", "is empty"},
		"missing file":    {strings.Replace(base, "dvr-pw", "nope", 1) + "[[channel]]\nindex = 1\n", "dvr.password_file"},
		"no host":         {strings.Replace(base, `host = "dvr.example.lan"`, "", 1) + "[[channel]]\nindex = 1\n", "dvr.host is required"},
		"syntax":          {"[dvr\n", "toml"},
		"reconnect max": {strings.Replace(base, "[dvr]", "[dvr]\nreconnect_seconds = 20\nreconnect_max_seconds = 10", 1) + "[[channel]]\nindex = 1\n",
			"reconnect_max_seconds 10"},
		"cache":      {base + "[snapshot]\ncache_seconds = 0\n[[channel]]\nindex = 1\n", "snapshot.cache_seconds"},
		"web listen": {base + "[web]\nlisten = \"nope\"\n[[channel]]\nindex = 1\n", "web.listen"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tt.conf), dir, noEnv)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

func TestPasswordFileExpandsEnv(t *testing.T) {
	dir := writeSecrets(t)
	env := func(k string) (string, bool) {
		if k == "CREDENTIALS_DIRECTORY" {
			return dir, true
		}
		return "", false
	}
	cfg, err := Parse([]byte(`
[dvr]
host = "dvr.example.lan"
user = "admin"
password_file = "${CREDENTIALS_DIRECTORY}/dvr-pw"
[[channel]]
index = 1
`), "/nonexistent", env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DVR.Password != "dvrsecret" {
		t.Fatalf("password %q", cfg.DVR.Password)
	}
}

func TestDistinctIPsSharePorts(t *testing.T) {
	_, err := Parse([]byte(base+`
[[channel]]
index = 1
listen_ip = "192.0.2.1"
[[channel]]
index = 2
listen_ip = "192.0.2.2"
`), writeSecrets(t), noEnv)
	if err != nil {
		t.Fatal(err)
	}
}

func TestExampleConfig(t *testing.T) {
	data, err := os.ReadFile("../../config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	// the example's password file does not exist here; the environment takes precedence over it
	env := func(k string) (string, bool) { return "x", k == EnvPassword }
	cfg, err := Parse(data, t.TempDir(), env)
	if err != nil {
		t.Fatal(err)
	}
	chs := cfg.Enabled()
	if cfg.Auto || len(chs) != 2 || chs[0].Name != "Front door" || chs[1].Model != "Bullet camera" {
		t.Fatalf("%+v", chs)
	}
}

func TestListenAddr(t *testing.T) {
	if got := ListenAddr(netip.Addr{}, 80); got != ":80" {
		t.Fatal(got)
	}
	if got := ListenAddr(netip.MustParseAddr("192.0.2.1"), 554); got != "192.0.2.1:554" {
		t.Fatal(got)
	}
}

func TestReconnectMax(t *testing.T) {
	dir := writeSecrets(t)
	parse := func(extra string) DVR {
		t.Helper()
		cfg, err := Parse([]byte(strings.Replace(base, "[dvr]", "[dvr]\n"+extra, 1)+"[[channel]]\nindex = 1\n"), dir, noEnv)
		if err != nil {
			t.Fatal(err)
		}
		return cfg.DVR
	}
	if d := parse("reconnect_seconds = 2\nreconnect_max_seconds = 30"); d.Reconnect != 2*time.Second || d.ReconnectMax != 30*time.Second {
		t.Fatalf("%+v", d)
	}
	// the default cap never undercuts the first delay
	if d := parse("reconnect_seconds = 20"); d.ReconnectMax != 20*time.Second {
		t.Fatalf("%+v", d)
	}
}

func TestFillerImage(t *testing.T) {
	dir := writeSecrets(t)
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	f, err := os.Create(filepath.Join(dir, "offline.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.WriteFile(filepath.Join(dir, "broken.png"), []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Parse([]byte(base+`
[defaults]
filler_image = "offline.png"
filler_text = false
[[channel]]
index = 1
listen_ip = "192.0.2.1"
[[channel]]
index = 2
listen_ip = "192.0.2.2"
filler_image = ""
filler_text = true
`), dir, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	c1, c2 := cfg.Channels[0], cfg.Channels[1]
	if c1.FillerImage == nil || c1.FillerImage.Bounds().Dx() != 4 || c1.FillerText {
		t.Fatalf("channel 1: %+v", c1)
	}
	if c2.FillerImage != nil || !c2.FillerText {
		t.Fatalf("channel 2: %+v", c2)
	}

	for name, file := range map[string]string{"missing": "nope.png", "broken": "broken.png"} {
		_, err := Parse([]byte(base+"[[channel]]\nindex = 1\nfiller_image = \""+file+"\"\n"), dir, noEnv)
		if err == nil || !strings.Contains(err.Error(), "filler_image") {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func TestAutoChannels(t *testing.T) {
	cfg, err := Parse([]byte(base), writeSecrets(t), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Auto || len(cfg.Enabled()) != 0 || cfg.Sources["channels"] != "auto (from the DVR)" {
		t.Fatalf("auto %t, sources %v", cfg.Auto, cfg.Sources)
	}
	chs := cfg.ChannelsFor(8)
	if len(chs) != 8 {
		t.Fatalf("%d channels", len(chs))
	}
	c3 := chs[2]
	// a shared address: each channel gets ports of its own
	if c3.Index != 3 || c3.Name != "CH3" || c3.RTSPPort != 8553 || c3.ONVIFPort != 8083 || c3.Model != "" || c3.ListenIP.IsValid() {
		t.Fatalf("channel 3: %+v", c3)
	}
	if len(cfg.ChannelsFor(99)) != MaxChannels {
		t.Fatal("ChannelsFor does not cap at MaxChannels")
	}
}

func TestEnvAndFlagsPrecedence(t *testing.T) {
	dir := writeSecrets(t)
	env := map[string]string{EnvHost: "env-host.example.lan", EnvPort: "9000", EnvUser: "envuser", EnvPasswordFile: dir + "/onvif-pw"}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }

	// environment only: no config file at all
	cfg, err := parse(nil, dir, Options{LookupEnv: lookup})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DVR.Host != "env-host.example.lan" || cfg.DVR.Port != 9000 || cfg.DVR.User != "envuser" || cfg.DVR.Password != "onvifsecret" {
		t.Fatalf("env: %+v", cfg.DVR)
	}
	if cfg.Sources["dvr.host"] != "env DVR_HOST" || cfg.Sources["dvr.password"] != "env DVR_PASSWORD_FILE" || !cfg.Auto || !cfg.Web.Enabled {
		t.Fatalf("sources %v", cfg.Sources)
	}

	// file < env < flag
	env[EnvPassword] = "frompw"
	cfg, err = parse([]byte(base), dir, Options{LookupEnv: lookup, Flags: map[string]string{"dvr.host": "flag-host.example.lan"}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DVR.Host != "flag-host.example.lan" || cfg.Sources["dvr.host"] != "flag --dvr-host" || cfg.DVR.User != "envuser" || cfg.DVR.Password != "frompw" {
		t.Fatalf("precedence: %+v %v", cfg.DVR, cfg.Sources)
	}
	cfg, _ = parse([]byte(base), dir, Options{LookupEnv: noEnv})
	if cfg.Sources["dvr.host"] != "file" || cfg.Sources["dvr.port"] != "default" || cfg.Sources["dvr.password"] != "file (password_file)" {
		t.Fatalf("file sources: %v", cfg.Sources)
	}

	if _, err := parse(nil, dir, Options{LookupEnv: noEnv}); err == nil || !strings.Contains(err.Error(), "DVR_HOST") {
		t.Fatalf("missing host: %v", err)
	}
}

func TestLoadOptionalFile(t *testing.T) {
	dir := writeSecrets(t)
	env := func(k string) (string, bool) {
		v, ok := map[string]string{EnvHost: "dvr.example.lan", EnvUser: "u", EnvPassword: "p"}[k]
		return v, ok
	}
	cfg, err := Load(Options{Path: dir + "/missing.toml", LookupEnv: env})
	if err != nil || cfg.File != "" {
		t.Fatalf("missing default file: %v %+v", err, cfg)
	}
	if _, err := Load(Options{Path: dir + "/missing.toml", PathExplicit: true, LookupEnv: env}); err == nil {
		t.Fatal("missing explicit file accepted")
	}
}
