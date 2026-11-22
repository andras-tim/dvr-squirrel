// SPDX-License-Identifier: GPL-3.0-or-later

// Package onvif serves a minimal ONVIF Profile S device (device and media services) for one channel.
package onvif

import (
	"crypto/sha1"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// VideoInfo describes the channel's current video stream.
type VideoInfo struct {
	Width, Height int
	FPS           float64
	GOP           int // frames from one IDR to the next
}

// Fallbacks before the first frame: CIF at the DVR's usual rate.
var defaultVideo = VideoInfo{Width: 352, Height: 288, FPS: 11, GOP: 14}

// Device is one ONVIF device.
//
// NVRs show a camera by its device information and scopes. UniFi Protect, as observed, names a camera
// "<manufacturer> <model>" from GetDeviceInformation and shows the name scope in its model column, so the device
// reports the channel name as the manufacturer with an empty model, and the model in the name and hardware scopes.
type Device struct {
	Name     string
	Model    string // initial model; SetModel replaces it
	Firmware string
	Serial   string

	// User and Password are required (WS-UsernameToken) for every action outside preAuth.
	User     string
	Password string

	// IP is advertised in service and stream URIs; when invalid, the Host of each request is used.
	IP        netip.Addr
	ONVIFPort int
	RTSPPort  int
	RTSPPath  string

	// Video returns the current stream parameters; nil or zero fields fall back to defaults.
	Video func() VideoInfo

	Log *slog.Logger
	// Now is the clock, replaceable in tests.
	Now func() time.Time

	authOnce sync.Once
	auth     authenticator
	model    atomic.Pointer[string]
}

// SetModel replaces the model, e.g. once the DVR's type is known.
func (d *Device) SetModel(m string) { d.model.Store(&m) }

// CurrentModel is the model reported now.
func (d *Device) CurrentModel() string {
	if m := d.model.Load(); m != nil {
		return *m
	}
	return d.Model
}

// SnapshotPath is where the ONVIF HTTP server must serve the snapshot JPEG.
const SnapshotPath = "/snapshot.jpg"

const (
	profileToken       = "main"
	videoSourceToken   = "video_src"
	videoSourceCfgTok  = "video_src_cfg"
	videoEncoderCfgTok = "video_enc_cfg"
	maxBody            = 1 << 20
)

// EndpointUUID is the stable WS-Addressing endpoint reference of the device, derived from the serial.
// The "dvr-onvif-bridge:" seed is the project's former name; keep it, or NVRs see new devices and need re-adoption.
func (d *Device) EndpointUUID() string {
	return "urn:uuid:" + uuid.NewSHA1(uuid.NameSpaceURL, []byte("dvr-onvif-bridge:"+d.Serial)).String()
}

// MAC is a stable, locally administered MAC address derived from the serial, reported by
// GetNetworkInterfaces since there is no real interface per channel.
func (d *Device) MAC() net.HardwareAddr {
	sum := sha1.Sum([]byte(d.Serial))
	mac := net.HardwareAddr(sum[:6])
	mac[0] = mac[0]&0xfe | 0x02
	return mac
}

// Scopes are the ONVIF scopes, shared by GetScopes and WS-Discovery.
func (d *Device) Scopes() []string {
	return []string{
		"onvif://www.onvif.org/type/video_encoder",
		"onvif://www.onvif.org/type/Network_Video_Transmitter",
		"onvif://www.onvif.org/Profile/Streaming",
		"onvif://www.onvif.org/name/" + scopeEscape(d.CurrentModel()),
		"onvif://www.onvif.org/hardware/" + scopeEscape(d.CurrentModel()),
	}
}

// scopeEscape percent-encodes what a scope's last path segment cannot hold (RFC 3986 pchar). Unlike url.PathEscape it
// keeps the sub-delims: UniFi Protect shows a scope as is, so "a, b" would read "a%2C%20b" there with a %2C.
func scopeEscape(s string) string {
	const keep = "-._~!$&'()*+,;=:@"
	var b strings.Builder
	for _, c := range []byte(s) {
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte(keep, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// DeviceServiceURL is the XAddr for the given host (IP or IP:port-less name).
func (d *Device) DeviceServiceURL(host string) string {
	return "http://" + net.JoinHostPort(host, strconv.Itoa(d.ONVIFPort)) + "/onvif/device_service"
}

func (d *Device) video() VideoInfo {
	v := VideoInfo{}
	if d.Video != nil {
		v = d.Video()
	}
	if v.Width == 0 || v.Height == 0 {
		v.Width, v.Height = defaultVideo.Width, defaultVideo.Height
	}
	if v.FPS <= 0 {
		v.FPS = defaultVideo.FPS
	}
	if v.GOP <= 0 {
		v.GOP = defaultVideo.GOP
	}
	return v
}

// ServeHTTP answers SOAP requests on any path; the device and media services share one endpoint.
func (d *Device) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "ONVIF SOAP endpoint, POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log := d.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	req, err := parseRequest(body)
	if err != nil {
		log.Debug("onvif malformed request", "remote", r.RemoteAddr, "err", err)
		writeFault(w, faultMalformed)
		return
	}
	action := req.Body.Action.XMLName.Local
	if !d.authorized(action, req) {
		log.Info("onvif unauthorized", "remote", r.RemoteAddr, "action", action)
		writeFault(w, faultNotAuthorized)
		return
	}
	resp, f := d.handle(action, req, d.host(r))
	if f != nil {
		log.Debug("onvif fault", "remote", r.RemoteAddr, "action", action, "fault", f.subcode)
		writeFault(w, *f)
		return
	}
	log.Debug("onvif request", "remote", r.RemoteAddr, "action", action)
	w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
	_, _ = w.Write(resp)
}

func writeFault(w http.ResponseWriter, f fault) {
	w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
	w.WriteHeader(f.status)
	_, _ = w.Write(f.body())
}

// preAuth are the actions served without credentials: clients read the clock for the digest, and
// many NVRs probe the services first.
var preAuth = map[string]bool{
	"GetSystemDateAndTime": true,
	"GetCapabilities":      true,
	"GetServices":          true,
}

func (d *Device) authorized(action string, req *request) bool {
	if preAuth[action] {
		return true
	}
	if d.User == "" {
		return false
	}
	d.authOnce.Do(func() { d.auth = authenticator{user: d.User, password: d.Password, now: d.now} })
	return d.auth.check(req.Header.Security.UsernameToken)
}

func (d *Device) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// host is the address put into URIs: the configured IP, or the host the client connected to.
func (d *Device) host(r *http.Request) string {
	if d.IP.IsValid() {
		return d.IP.String()
	}
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		return h
	}
	return r.Host
}

func (d *Device) handle(action string, req *request, host string) ([]byte, *fault) {
	ns := req.Body.Action.XMLName.Space
	switch action {
	// device service
	case "GetSystemDateAndTime":
		return d.systemDateAndTime(), nil
	case "GetDeviceInformation":
		return newEnvelope().printf(`<tds:GetDeviceInformationResponse><tds:Manufacturer>%s</tds:Manufacturer>`+
			`<tds:Model>%s</tds:Model><tds:FirmwareVersion>%s</tds:FirmwareVersion><tds:SerialNumber>%s</tds:SerialNumber>`+
			`<tds:HardwareId>%s</tds:HardwareId></tds:GetDeviceInformationResponse>`,
			esc(d.Name), "", esc(d.Firmware), esc(d.Serial), esc(d.CurrentModel())).bytes(), nil
	case "GetCapabilities":
		return d.capabilities(host), nil
	case "GetServices":
		return d.services(host), nil
	case "GetServiceCapabilities":
		if ns == nsMedia {
			return mediaServiceCapabilities(), nil
		}
		return deviceServiceCapabilities(), nil
	case "GetScopes":
		e := newEnvelope().printf(`<tds:GetScopesResponse>`)
		for _, s := range d.Scopes() {
			e.printf(`<tds:Scopes><tt:ScopeDef>Fixed</tt:ScopeDef><tt:ScopeItem>%s</tt:ScopeItem></tds:Scopes>`, esc(s))
		}
		return e.printf(`</tds:GetScopesResponse>`).bytes(), nil
	case "GetHostname":
		return newEnvelope().printf(`<tds:GetHostnameResponse><tds:HostnameInformation><tt:FromDHCP>false</tt:FromDHCP>`+
			`<tt:Name>%s</tt:Name></tds:HostnameInformation></tds:GetHostnameResponse>`, esc(hostname(d.Name))).bytes(), nil
	case "GetNetworkInterfaces":
		return d.networkInterfaces(host), nil
	case "GetNetworkProtocols":
		return newEnvelope().printf(`<tds:GetNetworkProtocolsResponse>`+
			`<tds:NetworkProtocols><tt:Name>HTTP</tt:Name><tt:Enabled>true</tt:Enabled><tt:Port>%d</tt:Port></tds:NetworkProtocols>`+
			`<tds:NetworkProtocols><tt:Name>RTSP</tt:Name><tt:Enabled>true</tt:Enabled><tt:Port>%d</tt:Port></tds:NetworkProtocols>`+
			`</tds:GetNetworkProtocolsResponse>`, d.ONVIFPort, d.RTSPPort).bytes(), nil
	case "GetDiscoveryMode":
		return newEnvelope().printf(`<tds:GetDiscoveryModeResponse><tds:DiscoveryMode>Discoverable</tds:DiscoveryMode>` +
			`</tds:GetDiscoveryModeResponse>`).bytes(), nil

	// media service
	case "GetProfiles":
		return d.profiles("GetProfilesResponse", "Profiles"), nil
	case "GetProfile":
		if req.Body.Action.ProfileToken != profileToken {
			return nil, &faultNoProfile
		}
		return d.profiles("GetProfileResponse", "Profile"), nil
	case "GetVideoSources":
		v := d.video()
		return newEnvelope().printf(`<trt:GetVideoSourcesResponse><trt:VideoSources token="%s"><tt:Framerate>%.2f</tt:Framerate>`+
			`<tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution></trt:VideoSources>`+
			`</trt:GetVideoSourcesResponse>`, videoSourceToken, v.FPS, v.Width, v.Height).bytes(), nil
	case "GetVideoSourceConfigurations":
		return d.wrap("GetVideoSourceConfigurationsResponse", d.videoSourceConfig("Configurations")), nil
	case "GetVideoSourceConfiguration":
		if req.Body.Action.ConfigurationToken != videoSourceCfgTok {
			return nil, &faultNoConfig
		}
		return d.wrap("GetVideoSourceConfigurationResponse", d.videoSourceConfig("Configuration")), nil
	case "GetVideoEncoderConfigurations":
		return d.wrap("GetVideoEncoderConfigurationsResponse", d.videoEncoderConfig("Configurations")), nil
	case "GetVideoEncoderConfiguration":
		if req.Body.Action.ConfigurationToken != videoEncoderCfgTok {
			return nil, &faultNoConfig
		}
		return d.wrap("GetVideoEncoderConfigurationResponse", d.videoEncoderConfig("Configuration")), nil
	case "GetVideoEncoderConfigurationOptions":
		return d.videoEncoderOptions(), nil
	case "GetStreamUri":
		if req.Body.Action.ProfileToken != profileToken {
			return nil, &faultNoProfile
		}
		uri := "rtsp://" + net.JoinHostPort(host, strconv.Itoa(d.RTSPPort)) + d.RTSPPath
		return newEnvelope().printf(`<trt:GetStreamUriResponse><trt:MediaUri><tt:Uri>%s</tt:Uri>`+
			`<tt:InvalidAfterConnect>false</tt:InvalidAfterConnect><tt:InvalidAfterReboot>false</tt:InvalidAfterReboot>`+
			`<tt:Timeout>PT0S</tt:Timeout></trt:MediaUri></trt:GetStreamUriResponse>`, esc(uri)).bytes(), nil
	case "GetSnapshotUri":
		if req.Body.Action.ProfileToken != profileToken {
			return nil, &faultNoProfile
		}
		uri := "http://" + net.JoinHostPort(host, strconv.Itoa(d.ONVIFPort)) + SnapshotPath
		return newEnvelope().printf(`<trt:GetSnapshotUriResponse><trt:MediaUri><tt:Uri>%s</tt:Uri>`+
			`<tt:InvalidAfterConnect>false</tt:InvalidAfterConnect><tt:InvalidAfterReboot>false</tt:InvalidAfterReboot>`+
			`<tt:Timeout>PT0S</tt:Timeout></trt:MediaUri></trt:GetSnapshotUriResponse>`, esc(uri)).bytes(), nil
	case "GetAudioSources", "GetAudioSourceConfigurations", "GetAudioEncoderConfigurations":
		return newEnvelope().printf(`<trt:%sResponse/>`, action).bytes(), nil
	}
	return nil, &faultNotSupported
}

func (d *Device) wrap(tag, inner string) []byte {
	return newEnvelope().printf(`<trt:%s>%s</trt:%s>`, tag, inner, tag).bytes()
}

func (d *Device) systemDateAndTime() []byte {
	t := d.now().UTC()
	return newEnvelope().printf(`<tds:GetSystemDateAndTimeResponse><tds:SystemDateAndTime>`+
		`<tt:DateTimeType>NTP</tt:DateTimeType><tt:DaylightSavings>false</tt:DaylightSavings>`+
		`<tt:TimeZone><tt:TZ>UTC0</tt:TZ></tt:TimeZone>`+
		`<tt:UTCDateTime><tt:Time><tt:Hour>%d</tt:Hour><tt:Minute>%d</tt:Minute><tt:Second>%d</tt:Second></tt:Time>`+
		`<tt:Date><tt:Year>%d</tt:Year><tt:Month>%d</tt:Month><tt:Day>%d</tt:Day></tt:Date></tt:UTCDateTime>`+
		`</tds:SystemDateAndTime></tds:GetSystemDateAndTimeResponse>`,
		t.Hour(), t.Minute(), t.Second(), t.Year(), int(t.Month()), t.Day()).bytes()
}

func (d *Device) xaddr(host, service string) string {
	return "http://" + net.JoinHostPort(host, strconv.Itoa(d.ONVIFPort)) + "/onvif/" + service
}

func (d *Device) capabilities(host string) []byte {
	return newEnvelope().printf(`<tds:GetCapabilitiesResponse><tds:Capabilities>`+
		`<tt:Device><tt:XAddr>%s</tt:XAddr></tt:Device>`+
		`<tt:Media><tt:XAddr>%s</tt:XAddr><tt:StreamingCapabilities><tt:RTPMulticast>false</tt:RTPMulticast>`+
		`<tt:RTP_TCP>false</tt:RTP_TCP><tt:RTP_RTSP_TCP>true</tt:RTP_RTSP_TCP></tt:StreamingCapabilities></tt:Media>`+
		`</tds:Capabilities></tds:GetCapabilitiesResponse>`,
		esc(d.xaddr(host, "device_service")), esc(d.xaddr(host, "media_service"))).bytes()
}

func (d *Device) services(host string) []byte {
	e := newEnvelope().printf(`<tds:GetServicesResponse>`)
	for _, s := range []struct{ ns, path string }{{nsDevice, "device_service"}, {nsMedia, "media_service"}} {
		e.printf(`<tds:Service><tds:Namespace>%s</tds:Namespace><tds:XAddr>%s</tds:XAddr>`+
			`<tds:Version><tt:Major>2</tt:Major><tt:Minor>5</tt:Minor></tds:Version></tds:Service>`, s.ns, esc(d.xaddr(host, s.path)))
	}
	return e.printf(`</tds:GetServicesResponse>`).bytes()
}

func deviceServiceCapabilities() []byte {
	return newEnvelope().printf(`<tds:GetServiceCapabilitiesResponse><tds:Capabilities>` +
		`<tds:Network IPFilter="false" ZeroConfiguration="false" IPVersion6="false" DynDNS="false"/>` +
		`<tds:Security UsernameToken="true" HttpDigest="false"/>` +
		`<tds:System DiscoveryResolve="false" DiscoveryBye="true" RemoteDiscovery="false" SystemBackup="false" FirmwareUpgrade="false"/>` +
		`</tds:Capabilities></tds:GetServiceCapabilitiesResponse>`).bytes()
}

func mediaServiceCapabilities() []byte {
	return newEnvelope().printf(`<trt:GetServiceCapabilitiesResponse>` +
		`<trt:Capabilities SnapshotUri="true" Rotation="false" VideoSourceMode="false" OSD="false">` +
		`<trt:ProfileCapabilities MaximumNumberOfProfiles="1"/>` +
		`<trt:StreamingCapabilities RTPMulticast="false" RTP_TCP="false" RTP_RTSP_TCP="true"/>` +
		`</trt:Capabilities></trt:GetServiceCapabilitiesResponse>`).bytes()
}

func (d *Device) networkInterfaces(host string) []byte {
	ip := ""
	if a, err := netip.ParseAddr(host); err == nil && a.Is4() {
		ip = a.String()
	}
	e := newEnvelope().printf(`<tds:GetNetworkInterfacesResponse><tds:NetworkInterfaces token="eth0">`+
		`<tt:Enabled>true</tt:Enabled><tt:Info><tt:Name>eth0</tt:Name><tt:HwAddress>%s</tt:HwAddress><tt:MTU>1500</tt:MTU></tt:Info>`,
		d.MAC())
	if ip != "" {
		e.printf(`<tt:IPv4><tt:Enabled>true</tt:Enabled><tt:Config><tt:Manual><tt:Address>%s</tt:Address>`+
			`<tt:PrefixLength>24</tt:PrefixLength></tt:Manual><tt:DHCP>false</tt:DHCP></tt:Config></tt:IPv4>`, ip)
	}
	return e.printf(`</tds:NetworkInterfaces></tds:GetNetworkInterfacesResponse>`).bytes()
}

func (d *Device) videoSourceConfig(tag string) string {
	v := d.video()
	return fmt.Sprintf(`<trt:%s token="%s"><tt:Name>VideoSourceConfig</tt:Name><tt:UseCount>1</tt:UseCount>`+
		`<tt:SourceToken>%s</tt:SourceToken><tt:Bounds x="0" y="0" width="%d" height="%d"/></trt:%s>`,
		tag, videoSourceCfgTok, videoSourceToken, v.Width, v.Height, tag)
}

func (d *Device) videoEncoderConfig(tag string) string {
	v := d.video()
	return fmt.Sprintf(`<trt:%s token="%s"><tt:Name>VideoEncoderConfig</tt:Name><tt:UseCount>1</tt:UseCount>`+
		`<tt:Encoding>H264</tt:Encoding><tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution>`+
		`<tt:Quality>5</tt:Quality><tt:RateControl><tt:FrameRateLimit>%d</tt:FrameRateLimit>`+
		`<tt:EncodingInterval>1</tt:EncodingInterval><tt:BitrateLimit>512</tt:BitrateLimit></tt:RateControl>`+
		`<tt:H264><tt:GovLength>%d</tt:GovLength><tt:H264Profile>Baseline</tt:H264Profile></tt:H264>`+
		`<tt:Multicast><tt:Address><tt:Type>IPv4</tt:Type><tt:IPv4Address>0.0.0.0</tt:IPv4Address></tt:Address>`+
		`<tt:Port>0</tt:Port><tt:TTL>0</tt:TTL><tt:AutoStart>false</tt:AutoStart></tt:Multicast>`+
		`<tt:SessionTimeout>PT60S</tt:SessionTimeout></trt:%s>`,
		tag, videoEncoderCfgTok, v.Width, v.Height, int(v.FPS+0.5), v.GOP, tag)
}

func (d *Device) videoEncoderOptions() []byte {
	v := d.video()
	fps := int(v.FPS + 0.5)
	return newEnvelope().printf(`<trt:GetVideoEncoderConfigurationOptionsResponse><trt:Options>`+
		`<tt:QualityRange><tt:Min>5</tt:Min><tt:Max>5</tt:Max></tt:QualityRange><tt:H264>`+
		`<tt:ResolutionsAvailable><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:ResolutionsAvailable>`+
		`<tt:GovLengthRange><tt:Min>%d</tt:Min><tt:Max>%d</tt:Max></tt:GovLengthRange>`+
		`<tt:FrameRateRange><tt:Min>%d</tt:Min><tt:Max>%d</tt:Max></tt:FrameRateRange>`+
		`<tt:EncodingIntervalRange><tt:Min>1</tt:Min><tt:Max>1</tt:Max></tt:EncodingIntervalRange>`+
		`<tt:H264ProfilesSupported>Baseline</tt:H264ProfilesSupported></tt:H264>`+
		`</trt:Options></trt:GetVideoEncoderConfigurationOptionsResponse>`,
		v.Width, v.Height, v.GOP, v.GOP, fps, fps).bytes()
}

func (d *Device) profiles(respTag, tag string) []byte {
	return newEnvelope().printf(`<trt:%s><trt:%s token="%s" fixed="true"><tt:Name>%s</tt:Name>%s%s</trt:%s></trt:%s>`,
		respTag, tag, profileToken, esc(d.Name),
		d.videoSourceConfig("VideoSourceConfiguration"), d.videoEncoderConfig("VideoEncoderConfiguration"),
		tag, respTag).bytes()
}

// hostname turns a display name into a host name label.
func hostname(name string) string {
	b := []byte{}
	for _, c := range []byte(name) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b = append(b, c)
		case c >= 'A' && c <= 'Z':
			b = append(b, c+'a'-'A')
		case len(b) > 0 && b[len(b)-1] != '-':
			b = append(b, '-')
		}
	}
	for len(b) > 0 && b[len(b)-1] == '-' {
		b = b[:len(b)-1]
	}
	if len(b) == 0 {
		return "camera"
	}
	return string(b)
}
