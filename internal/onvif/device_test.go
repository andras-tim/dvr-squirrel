// SPDX-License-Identifier: GPL-3.0-or-later

package onvif

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

func newDevice() *Device {
	return &Device{
		Name: "Yard & Gate", Model: "DVR channel", Firmware: "1.0", Serial: "SN-2",
		User: "viewer", Password: "pw",
		ONVIFPort: 8082, RTSPPort: 8552, RTSPPath: "/stream",
		Video: func() VideoInfo { return VideoInfo{Width: 352, Height: 288, FPS: 10.7, GOP: 14} },
		Now:   func() time.Time { return now },
	}
}

type token struct {
	user, pass, nonce, created string
	plain                      bool
}

func digestToken(user, pass, nonce string, created time.Time) token {
	return token{user: user, pass: pass, nonce: nonce, created: created.Format(time.RFC3339)}
}

func (tk token) header() string {
	if tk.user == "" {
		return ""
	}
	pwType, pw := "PasswordText", tk.pass
	if !tk.plain {
		h := sha1.Sum([]byte(tk.nonce + tk.created + tk.pass))
		pwType, pw = "PasswordDigest", base64.StdEncoding.EncodeToString(h[:])
	}
	return fmt.Sprintf(`<s:Header><wsse:Security xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd"`+
		` xmlns:wsu="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd">`+
		`<wsse:UsernameToken><wsse:Username>%s</wsse:Username>`+
		`<wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#%s">%s</wsse:Password>`+
		`<wsse:Nonce>%s</wsse:Nonce><wsu:Created>%s</wsu:Created></wsse:UsernameToken></wsse:Security></s:Header>`,
		tk.user, pwType, pw, base64.StdEncoding.EncodeToString([]byte(tk.nonce)), tk.created)
}

func call(t *testing.T, d *Device, tk token, body string) (int, string) {
	t.Helper()
	env := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl"` +
		` xmlns:trt="http://www.onvif.org/ver10/media/wsdl">` + tk.header() + `<s:Body>` + body + `</s:Body></s:Envelope>`
	req := httptest.NewRequest(http.MethodPost, "http://198.51.100.7:8082/onvif/device_service", strings.NewReader(env))
	rec := httptest.NewRecorder()
	d.ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Result().Body)
	// every answer must be well-formed XML
	dec := xml.NewDecoder(bytes.NewReader(b))
	for {
		if _, err := dec.Token(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("malformed response (%v): %s", err, b)
		}
	}
	return rec.Code, string(b)
}

func TestPreAuthActions(t *testing.T) {
	d := newDevice()
	code, body := call(t, d, token{}, `<tds:GetSystemDateAndTime/>`)
	if code != 200 || !strings.Contains(body, "<tt:Hour>5</tt:Hour>") || !strings.Contains(body, "<tt:Year>2026</tt:Year>") {
		t.Fatalf("%d %s", code, body)
	}
	code, body = call(t, d, token{}, `<tds:GetCapabilities><tds:Category>All</tds:Category></tds:GetCapabilities>`)
	if code != 200 || !strings.Contains(body, "<tt:XAddr>http://198.51.100.7:8082/onvif/media_service</tt:XAddr>") {
		t.Fatalf("%d %s", code, body)
	}
	code, body = call(t, d, token{}, `<tds:GetServices><tds:IncludeCapability>false</tds:IncludeCapability></tds:GetServices>`)
	if code != 200 || strings.Count(body, "<tds:Service>") != 2 {
		t.Fatalf("%d %s", code, body)
	}
}

func TestAuth(t *testing.T) {
	d := newDevice()
	info := `<tds:GetDeviceInformation/>`
	tests := []struct {
		name string
		tk   token
		want int
	}{
		{"none", token{}, 400},
		{"digest", digestToken("viewer", "pw", "nonce-1", now.Add(-time.Minute)), 200},
		{"replay", digestToken("viewer", "pw", "nonce-1", now.Add(-time.Minute)), 400},
		{"same nonce, new created", digestToken("viewer", "pw", "nonce-1", now), 200},
		{"wrong password", digestToken("viewer", "nope", "nonce-2", now), 400},
		{"wrong user", digestToken("admin", "pw", "nonce-3", now), 400},
		{"stale", digestToken("viewer", "pw", "nonce-4", now.Add(-10*time.Minute)), 400},
		{"plain", token{user: "viewer", pass: "pw", plain: true}, 200},
		{"plain wrong", token{user: "viewer", pass: "pw2", plain: true}, 400},
	}
	for _, tt := range tests {
		code, body := call(t, d, tt.tk, info)
		if code != tt.want {
			t.Fatalf("%s: %d, want %d: %s", tt.name, code, tt.want, body)
		}
		if code == 400 && !strings.Contains(body, "ter:NotAuthorized") {
			t.Fatalf("%s: fault %s", tt.name, body)
		}
	}
	_, body := call(t, d, digestToken("viewer", "pw", "nonce-5", now), info)
	for _, want := range []string{"<tds:Manufacturer>Yard &amp; Gate</tds:Manufacturer>", "<tds:SerialNumber>SN-2</tds:SerialNumber>"} {
		if !strings.Contains(body, want) {
			t.Fatalf("lacks %s: %s", want, body)
		}
	}
}

func TestNoUserRejects(t *testing.T) {
	d := newDevice()
	d.User, d.Password = "", ""
	if code, _ := call(t, d, token{user: "", pass: ""}, `<tds:GetDeviceInformation/>`); code != 400 {
		t.Fatalf("got %d", code)
	}
}

func TestMediaActions(t *testing.T) {
	d := newDevice()
	n := 0
	auth := func() token { n++; return digestToken("viewer", "pw", fmt.Sprint("n", n), now) }
	tests := []struct {
		body string
		want []string
	}{
		{`<trt:GetProfiles/>`, []string{`<trt:Profiles token="main" fixed="true">`, "<tt:Name>Yard &amp; Gate</tt:Name>",
			"<tt:Width>352</tt:Width>", "<tt:FrameRateLimit>11</tt:FrameRateLimit>", "<tt:GovLength>14</tt:GovLength>"}},
		{`<trt:GetProfile><trt:ProfileToken>main</trt:ProfileToken></trt:GetProfile>`, []string{`<trt:Profile token="main"`}},
		{`<trt:GetVideoSources/>`, []string{`<trt:VideoSources token="video_src">`, "<tt:Height>288</tt:Height>"}},
		{`<trt:GetVideoSourceConfigurations/>`, []string{`<trt:Configurations token="video_src_cfg">`}},
		{`<trt:GetVideoEncoderConfigurations/>`, []string{`<trt:Configurations token="video_enc_cfg">`, "<tt:Encoding>H264</tt:Encoding>"}},
		{`<trt:GetVideoEncoderConfiguration><trt:ConfigurationToken>video_enc_cfg</trt:ConfigurationToken></trt:GetVideoEncoderConfiguration>`,
			[]string{`<trt:Configuration token="video_enc_cfg">`}},
		{`<trt:GetVideoEncoderConfigurationOptions/>`, []string{"<tt:H264ProfilesSupported>Baseline</tt:H264ProfilesSupported>"}},
		{`<trt:GetStreamUri><trt:StreamSetup/><trt:ProfileToken>main</trt:ProfileToken></trt:GetStreamUri>`,
			[]string{"<tt:Uri>rtsp://198.51.100.7:8552/stream</tt:Uri>"}},
		{`<trt:GetServiceCapabilities/>`, []string{`<trt:Capabilities SnapshotUri="true"`, `RTP_RTSP_TCP="true"`}},
		{`<tds:GetServiceCapabilities/>`, []string{"<tds:Capabilities>", `UsernameToken="true"`}},
		{`<tds:GetScopes/>`, []string{"onvif://www.onvif.org/name/DVR%20channel<", "onvif://www.onvif.org/Profile/Streaming", "onvif://www.onvif.org/hardware/DVR%20channel<"}},
		{`<tds:GetNetworkInterfaces/>`, []string{"<tt:HwAddress>" + d.MAC().String() + "</tt:HwAddress>", "<tt:Address>198.51.100.7</tt:Address>"}},
		{`<tds:GetHostname/>`, []string{"<tt:Name>yard-gate</tt:Name>"}},
		{`<trt:GetAudioSources/>`, []string{"<trt:GetAudioSourcesResponse/>"}},
	}
	for _, tt := range tests {
		code, body := call(t, d, auth(), tt.body)
		if code != 200 {
			t.Fatalf("%s: %d %s", tt.body, code, body)
		}
		for _, w := range tt.want {
			if !strings.Contains(body, w) {
				t.Errorf("%s: lacks %s:\n%s", tt.body, w, body)
			}
		}
	}

	code, body := call(t, d, auth(), `<trt:GetStreamUri><trt:ProfileToken>other</trt:ProfileToken></trt:GetStreamUri>`)
	if code != 400 || !strings.Contains(body, "ter:InvalidArgVal") {
		t.Fatalf("unknown profile: %d %s", code, body)
	}
	code, body = call(t, d, auth(), `<trt:GetSnapshotUri><trt:ProfileToken>main</trt:ProfileToken></trt:GetSnapshotUri>`)
	if code != 200 || !strings.Contains(body, "<tt:Uri>http://198.51.100.7:8082/snapshot.jpg</tt:Uri>") {
		t.Fatalf("GetSnapshotUri: %d %s", code, body)
	}
	code, body = call(t, d, auth(), `<trt:GetOSDs/>`)
	if code != 500 || !strings.Contains(body, "ter:ActionNotSupported") {
		t.Fatalf("unsupported action: %d %s", code, body)
	}
}

func TestConfiguredIPInURIs(t *testing.T) {
	d := newDevice()
	d.IP = netip.MustParseAddr("192.0.2.12")
	_, body := call(t, d, digestToken("viewer", "pw", "x", now), `<trt:GetStreamUri><trt:ProfileToken>main</trt:ProfileToken></trt:GetStreamUri>`)
	if !strings.Contains(body, "rtsp://192.0.2.12:8552/stream") {
		t.Fatal(body)
	}
}

func TestMalformedAndMethod(t *testing.T) {
	d := newDevice()
	rec := httptest.NewRecorder()
	d.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/onvif/device_service", strings.NewReader("<nope")))
	if rec.Code != 400 {
		t.Fatalf("malformed: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	d.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/onvif/device_service", nil))
	if rec.Code != 405 {
		t.Fatalf("GET: %d", rec.Code)
	}
}

func TestStableIdentity(t *testing.T) {
	a, b := newDevice(), newDevice()
	b.Serial = "SN-3"
	if a.EndpointUUID() != newDevice().EndpointUUID() || a.EndpointUUID() == b.EndpointUUID() {
		t.Fatal("endpoint UUID not stable per serial")
	}
	if !strings.HasPrefix(a.EndpointUUID(), "urn:uuid:") {
		t.Fatal(a.EndpointUUID())
	}
	if mac := a.MAC(); mac[0]&0x03 != 0x02 {
		t.Fatalf("MAC %s is not locally administered unicast", mac)
	}
	// the interface's MAC replaces the derived one, so the NVR sees the same MAC as on the wire
	a.HwAddr = net.HardwareAddr{0x02, 0x00, 0x5e, 0x00, 0x53, 0x01}
	if a.MAC().String() != "02:00:5e:00:53:01" {
		t.Fatalf("MAC %s, want the HwAddr", a.MAC())
	}
	_, body := call(t, a, digestToken("viewer", "pw", "mac-1", now), `<tds:GetNetworkInterfaces/>`)
	if !strings.Contains(body, "<tt:HwAddress>02:00:5e:00:53:01</tt:HwAddress>") {
		t.Fatalf("GetNetworkInterfaces: %s", body)
	}
}

func TestScopeEscape(t *testing.T) {
	for in, want := range map[string]string{
		"DVR 500 series, 4 channels": "DVR%20500%20series,%204%20channels",
		"A/B?#%":                     "A%2FB%3F%23%25",
		"Cam \u00e9":                 "Cam%20%C3%A9",
		"x-._~!$&'()*+;=:@":          "x-._~!$&'()*+;=:@",
	} {
		if got := scopeEscape(in); got != want {
			t.Errorf("scopeEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNVRNameMapping pins what UniFi Protect was observed to show: the name is "<manufacturer> <model>" from
// GetDeviceInformation, the model column is the name scope. The channel name must come out as the name and the
// configured model as the model.
func TestNVRNameMapping(t *testing.T) {
	d := newDevice()
	d.Name, d.Model = "Front door", "Dome camera"
	_, body := call(t, d, digestToken("viewer", "pw", "map-1", now), `<tds:GetDeviceInformation/>`)
	var info struct {
		Manufacturer string `xml:"Body>GetDeviceInformationResponse>Manufacturer"`
		Model        string `xml:"Body>GetDeviceInformationResponse>Model"`
	}
	if err := xml.Unmarshal([]byte(body), &info); err != nil {
		t.Fatal(err)
	}
	if shown := strings.TrimSpace(info.Manufacturer + " " + info.Model); shown != "Front door" {
		t.Fatalf("NVR name would be %q", shown)
	}
	_, body = call(t, d, digestToken("viewer", "pw", "map-2", now), `<tds:GetScopes/>`)
	if !strings.Contains(body, "onvif://www.onvif.org/name/Dome%20camera") || !strings.Contains(body, "onvif://www.onvif.org/hardware/Dome%20camera") {
		t.Fatalf("scopes: %s", body)
	}

	// the detected model replaces the initial one everywhere
	d.SetModel("DVR 500 series, 4 channels")
	if sc := strings.Join(d.Scopes(), " "); !strings.Contains(sc, "name/DVR%20500%20series,%204%20channels") {
		t.Fatalf("scopes after SetModel: %s", sc)
	}
}
