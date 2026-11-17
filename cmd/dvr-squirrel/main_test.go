// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/pion/rtp"

	"github.com/andras-tim/dvr-squirrel/internal/config"
	"github.com/andras-tim/dvr-squirrel/internal/fakedvr"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func soap(t *testing.T, endpoint, user, pass, body string) (int, string) {
	t.Helper()
	header := ""
	if user != "" {
		nonce := []byte(fmt.Sprintf("nonce-%d", time.Now().UnixNano()))
		created := time.Now().UTC().Format(time.RFC3339)
		h := sha1.Sum(append(append(nonce, created...), pass...))
		header = fmt.Sprintf(`<s:Header><Security xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd">`+
			`<UsernameToken><Username>%s</Username>`+
			`<Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">%s</Password>`+
			`<Nonce>%s</Nonce><Created xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd">%s</Created>`+
			`</UsernameToken></Security></s:Header>`,
			user, base64.StdEncoding.EncodeToString(h[:]), base64.StdEncoding.EncodeToString(nonce), created)
	}
	env := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">` + header + `<s:Body>` + body + `</s:Body></s:Envelope>`
	resp, err := http.Post(endpoint, "application/soap+xml", bytes.NewBufferString(env))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestEndToEnd runs the bridge against a fake DVR on localhost: ONVIF finds the stream URI,
// RTSP with digest auth plays it from an IDR frame, and shutdown logs out of the DVR.
func TestEndToEnd(t *testing.T) {
	dvr := &fakedvr.Server{
		User: "admin", Password: "dvrpass", Channels: 4,
		Frames:   fakedvr.Sequence([]uint8{0, 1, 2, 3}, 400, 10, 0, true),
		Interval: 2 * time.Millisecond,
	}
	if err := dvr.Start(); err != nil {
		t.Fatal(err)
	}
	defer dvr.Close()
	host, port, _ := net.SplitHostPort(dvr.Addr())

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dvr-password"), []byte("dvrpass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	onvifPort, rtspPort := freePort(t), freePort(t)
	conf := fmt.Sprintf(`
[dvr]
host = %q
port = %s
user = "admin"
password_file = "dvr-password"
reconnect_seconds = 1
web_port = %d
[discovery]
enabled = false
[web]
enabled = false
[defaults]
listen_ip = "127.0.0.1"
[[channel]]
index = 2
name = "Yard"
onvif_port = %d
rtsp_port = %d
audio = true
`, host, port, freePort(t), onvifPort, rtspPort)
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.Options{Path: path, PathExplicit: true, LookupEnv: func(string) (string, bool) { return "", false }})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, slog.New(slog.DiscardHandler), nil) }()

	onvifURL := fmt.Sprintf("http://127.0.0.1:%d/onvif/device_service", onvifPort)
	var status int
	var body string
	for range 100 {
		resp, err := http.Get(onvifURL)
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status, body = soap(t, onvifURL, "", "", `<GetSystemDateAndTime xmlns="http://www.onvif.org/ver10/device/wsdl"/>`); status != 200 {
		t.Fatalf("GetSystemDateAndTime without auth: %d %s", status, body)
	}
	status, body = soap(t, onvifURL, "admin", "dvrpass", `<GetDeviceInformation xmlns="http://www.onvif.org/ver10/device/wsdl"/>`)
	if status != 200 || !strings.Contains(body, "<tds:Manufacturer>Yard</tds:Manufacturer>") {
		t.Fatalf("GetDeviceInformation: %d %s", status, body)
	}
	getURI := `<GetStreamUri xmlns="http://www.onvif.org/ver10/media/wsdl"><ProfileToken>main</ProfileToken></GetStreamUri>`
	if status, _ = soap(t, onvifURL, "", "", getURI); status != 400 {
		t.Fatalf("GetStreamUri without auth: %d", status)
	}
	if status, _ = soap(t, onvifURL, "admin", "wrong", getURI); status != 400 {
		t.Fatalf("GetStreamUri with a wrong password: %d", status)
	}
	status, body = soap(t, onvifURL, "admin", "dvrpass", getURI)
	m := regexp.MustCompile(`<tt:Uri>([^<]+)</tt:Uri>`).FindStringSubmatch(body)
	if status != 200 || m == nil {
		t.Fatalf("GetStreamUri: %d %s", status, body)
	}
	want := fmt.Sprintf("rtsp://127.0.0.1:%d/stream", rtspPort)
	if m[1] != want {
		t.Fatalf("stream URI %s, want %s", m[1], want)
	}

	snapURL := fmt.Sprintf("http://127.0.0.1:%d/snapshot.jpg", onvifPort)
	status, body = soap(t, onvifURL, "admin", "dvrpass",
		`<GetSnapshotUri xmlns="http://www.onvif.org/ver10/media/wsdl"><ProfileToken>main</ProfileToken></GetSnapshotUri>`)
	if status != 200 || !strings.Contains(body, "<tt:Uri>"+snapURL+"</tt:Uri>") {
		t.Fatalf("GetSnapshotUri: %d %s", status, body)
	}
	if resp, err := http.Get(snapURL); err != nil || resp.StatusCode != 401 {
		t.Fatalf("snapshot without credentials: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}
	req, _ := http.NewRequest(http.MethodGet, snapURL, nil)
	req.SetBasicAuth("admin", "dvrpass")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(resp.Body)
	resp.Body.Close()
	if err != nil || img.Bounds().Dx() != fakedvr.Width {
		t.Fatalf("snapshot: %v", err)
	}

	u, _ := base.ParseURL(m[1])
	c := gortsplib.Client{Scheme: u.Scheme, Host: u.Host}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, _, err := c.Describe(u); err == nil {
		t.Fatal("DESCRIBE without credentials succeeded")
	}
	u.User = url.UserPassword("admin", "dvrpass")
	var desc *description.Session
	for range 100 {
		if desc, _, err = c.Describe(u); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	var h *format.H264
	vm := desc.FindFormat(&h)
	var aac *format.MPEG4Audio
	if vm == nil || desc.FindFormat(&aac) == nil {
		t.Fatalf("medias: %+v", desc.Medias)
	}
	if err := c.SetupAll(desc.BaseURL, desc.Medias); err != nil {
		t.Fatal(err)
	}
	dec, _ := h.CreateDecoder()
	first := make(chan [][]byte, 1)
	c.OnPacketRTP(vm, h, func(pkt *rtp.Packet) {
		au, err := dec.Decode(pkt)
		if err == nil {
			select {
			case first <- au:
			default:
			}
		} else if !errors.Is(err, rtph264.ErrMorePacketsNeeded) {
			t.Errorf("decode: %v", err)
		}
	})
	if _, err := c.Play(nil); err != nil {
		t.Fatal(err)
	}
	select {
	case au := <-first:
		if !h264.IsRandomAccess(au) {
			t.Fatal("first access unit is not an IDR")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no video")
	}
	c.Close()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return")
	}
	// the fake DVR counts the logout on its own goroutine
	for i := 0; dvr.Logouts() != 1; i++ {
		if i == 200 {
			t.Fatalf("logouts %d", dvr.Logouts())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
