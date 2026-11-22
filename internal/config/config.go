// SPDX-License-Identifier: GPL-3.0-or-later

// Package config loads and validates the TOML configuration.
package config

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // filler_image formats
	_ "image/png"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Environment variables of the zero-config setup; flags override them, they override the config file.
const (
	EnvHost         = "DVR_HOST"
	EnvPort         = "DVR_PORT"
	EnvUser         = "DVR_USER"
	EnvPassword     = "DVR_PASSWORD"
	EnvPasswordFile = "DVR_PASSWORD_FILE"
	// PasswordEnv is EnvPassword, kept for callers that name it so.
	PasswordEnv = EnvPassword
)

// DefaultPath is the config file read when --config is not given; it is optional.
const DefaultPath = "/etc/dvr-squirrel/config.toml"

// MaxChannels is the highest channel count of the supported DVRs (the 16-channel model).
const MaxChannels = 16

// Config is the resolved configuration.
type Config struct {
	DVR       DVR
	Discovery Discovery
	Snapshot  Snapshot
	Web       Web
	// Channels holds the channels configured in the file, enabled or not, ordered by index. When the file
	// has no [[channel]] block, it is nil and Auto is set: the channels follow the DVR's channel count.
	Channels []Channel
	Auto     bool
	// auto holds MaxChannels channels built from [defaults], for Auto.
	auto []Channel

	// File is the config file read, empty when none was.
	File string
	// Sources tells where each top-level value came from: default, file, env NAME or flag --NAME.
	Sources map[string]string
}

// DVR is the [dvr] table.
type DVR struct {
	Host      string
	Port      int
	User      string
	Password  string        `toml:"-"` // from the environment or a password file
	Reconnect time.Duration // first reconnect delay
	// ReconnectMax caps the doubling delay; short by default, as the DVR is usually on a LAN or VPN.
	ReconnectMax time.Duration
	// WebPort is the DVR's HTTP port, for the settings API used by the web UI.
	WebPort int
}

// Addr is the DVR's host:port.
func (d DVR) Addr() string { return net.JoinHostPort(d.Host, strconv.Itoa(d.Port)) }

// Discovery is the [discovery] table.
type Discovery struct {
	Enabled bool
	// Interface is the network interface for WS-Discovery multicast; empty means the system default.
	Interface string
}

// Web is the [web] table: the admin/status UI.
type Web struct {
	Enabled bool
	Listen  string
}

// Snapshot is the [snapshot] table.
type Snapshot struct {
	// Enabled decodes snapshots with FFmpeg; when off, or FFmpeg is missing, a placeholder is served.
	Enabled bool
	// FFmpeg is "embedded", a name looked up in PATH or a path; empty means the embedded one, else "ffmpeg".
	FFmpeg   string
	CacheFor time.Duration
}

// Channel is one channel with the defaults applied.
type Channel struct {
	Index    int // 1-based, as on the DVR's front panel
	Name     string
	Enabled  bool
	Audio    bool
	ListenIP netip.Addr // invalid (zero) means all addresses

	RTSPPort  int
	RTSPPath  string
	ONVIFPort int
	// User and Password protect both ONVIF and RTSP; they default to the DVR login.
	User     string
	Password string

	// Model is the configured camera model; empty means the one detected from the DVR.
	Model    string
	Serial   string
	Firmware string
	// MAC is the configured mac; nil means the MAC of the listen_ip's interface, or one derived from the serial.
	MAC net.HardwareAddr

	// FillerImage is the decoded filler_image, nil for the generated background.
	FillerImage     image.Image
	FillerImagePath string
	// FillerText draws the channel name and the offline status over the filler picture.
	FillerText bool
}

// Enabled returns the enabled configured channels (none in Auto mode before ChannelsFor).
func (c *Config) Enabled() []Channel {
	return slices.DeleteFunc(slices.Clone(c.Channels), func(ch Channel) bool { return !ch.Enabled })
}

// ChannelsFor returns the enabled channels for a DVR with n channels: in Auto mode channels 1..n, otherwise
// the configured ones.
func (c *Config) ChannelsFor(n int) []Channel {
	if !c.Auto {
		return c.Enabled()
	}
	n = min(max(n, 0), len(c.auto))
	return slices.DeleteFunc(slices.Clone(c.auto[:n]), func(ch Channel) bool { return !ch.Enabled })
}

// settings are the keys shared by [defaults] and [[channel]]; nil means unset.
type settings struct {
	Enabled      *bool   `toml:"enabled"`
	Audio        *bool   `toml:"audio"`
	ListenIP     *string `toml:"listen_ip"`
	RTSPPort     *int    `toml:"rtsp_port"`
	RTSPPath     *string `toml:"rtsp_path"`
	ONVIFPort    *int    `toml:"onvif_port"`
	User         *string `toml:"user"`
	PasswordFile *string `toml:"password_file"`
	Model        *string `toml:"model"`
	Serial       *string `toml:"serial"`
	Firmware     *string `toml:"firmware"`
	FillerImage  *string `toml:"filler_image"`
	FillerText   *bool   `toml:"filler_text"`
}

type fileChannel struct {
	Index int    `toml:"index"`
	Name  string `toml:"name"`
	// MAC is per channel only, never in [defaults]: it identifies one camera.
	MAC string `toml:"mac"`
	settings
}

type file struct {
	DVR struct {
		Host             string `toml:"host"`
		Port             int    `toml:"port"`
		User             string `toml:"user"`
		PasswordFile     string `toml:"password_file"`
		ReconnectSeconds int    `toml:"reconnect_seconds"`
		ReconnectMax     int    `toml:"reconnect_max_seconds"`
		WebPort          int    `toml:"web_port"`
	} `toml:"dvr"`
	Discovery struct {
		Enabled   *bool  `toml:"enabled"`
		Interface string `toml:"interface"`
	} `toml:"discovery"`
	Web struct {
		Enabled *bool  `toml:"enabled"`
		Listen  string `toml:"listen"`
	} `toml:"web"`
	Snapshot struct {
		Enabled      *bool  `toml:"enabled"`
		FFmpeg       string `toml:"ffmpeg"`
		CacheSeconds *int   `toml:"cache_seconds"`
	} `toml:"snapshot"`
	Defaults settings      `toml:"defaults"`
	Channel  []fileChannel `toml:"channel"`
}

// builtin are the defaults below [defaults]. The ports are not here: they depend on the listen address.
var builtin = settings{
	Enabled:     ptr(true),
	Audio:       ptr(false),
	ListenIP:    ptr(""),
	RTSPPath:    ptr("/stream"),
	Firmware:    ptr("1.0"),
	FillerImage: ptr(""),
	FillerText:  ptr(true),
}

// Ports of a channel without its own address: channels share the host's, so each gets ports of its own.
const (
	sharedRTSPBase  = 8550 // channel n: 8550+n
	sharedONVIFBase = 8080 // channel n: 8080+n
)

func ptr[T any](v T) *T { return &v }

// merge returns s with the unset keys taken from base.
func merge(s, base settings) settings {
	pick := func(dst **string, src *string) {
		if *dst == nil {
			*dst = src
		}
	}
	if s.Enabled == nil {
		s.Enabled = base.Enabled
	}
	if s.Audio == nil {
		s.Audio = base.Audio
	}
	if s.RTSPPort == nil {
		s.RTSPPort = base.RTSPPort
	}
	if s.ONVIFPort == nil {
		s.ONVIFPort = base.ONVIFPort
	}
	if s.FillerText == nil {
		s.FillerText = base.FillerText
	}
	pick(&s.ListenIP, base.ListenIP)
	pick(&s.RTSPPath, base.RTSPPath)
	pick(&s.User, base.User)
	pick(&s.PasswordFile, base.PasswordFile)
	pick(&s.Model, base.Model)
	pick(&s.Serial, base.Serial)
	pick(&s.Firmware, base.Firmware)
	pick(&s.FillerImage, base.FillerImage)
	return s
}

// Options select the config sources.
type Options struct {
	// Path is the config file; empty means DefaultPath.
	Path string
	// PathExplicit makes a missing file an error (it was given with --config).
	PathExplicit bool
	// Flags are the command line values that override the environment and the file, by key ("dvr.host",
	// "dvr.port", "dvr.user", "dvr.password_file").
	Flags map[string]string
	// LookupEnv reads the environment; nil means os.LookupEnv.
	LookupEnv func(string) (string, bool)
}

// Load reads the optional config file and resolves it with the environment and the flags.
func Load(o Options) (*Config, error) {
	if o.LookupEnv == nil {
		o.LookupEnv = os.LookupEnv
	}
	path := o.Path
	if path == "" {
		path = DefaultPath
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist) && !o.PathExplicit:
		data, path = nil, ""
	default:
		return nil, err
	}
	baseDir := "."
	if path != "" {
		baseDir = filepath.Dir(path)
	}
	cfg, err := parse(data, baseDir, o)
	if err != nil {
		if path != "" {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return nil, err
	}
	cfg.File = path
	return cfg, nil
}

// Parse resolves and validates a config file with the environment. Relative file paths are taken from baseDir.
func Parse(data []byte, baseDir string, lookupEnv func(string) (string, bool)) (*Config, error) {
	return parse(data, baseDir, Options{LookupEnv: lookupEnv})
}

func parse(data []byte, baseDir string, o Options) (*Config, error) {
	var f file
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return nil, err
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("unknown keys: %s", strings.Join(keys, ", "))
	}
	lookupEnv := o.LookupEnv
	if lookupEnv == nil {
		lookupEnv = func(string) (string, bool) { return "", false }
	}

	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	// ${VAR} lets a path point into e.g. systemd's $CREDENTIALS_DIRECTORY.
	resolve := func(p string) string {
		p = os.Expand(p, func(k string) string { v, _ := lookupEnv(k); return v })
		if !filepath.IsAbs(p) {
			p = filepath.Join(baseDir, p)
		}
		return p
	}
	images := map[string]image.Image{}
	readImage := func(what, p string) (image.Image, string) {
		p = resolve(p)
		if img, ok := images[p]; ok {
			return img, p
		}
		f, err := os.Open(p)
		if err != nil {
			fail("%s: %w", what, err)
			return nil, p
		}
		defer f.Close()
		img, _, err := image.Decode(f)
		if err != nil {
			fail("%s: %s: %w", what, p, err)
			return nil, p
		}
		images[p] = img
		return img, p
	}
	readSecret := func(what, p string) string {
		p = resolve(p)
		b, err := os.ReadFile(p)
		if err != nil {
			fail("%s: %w", what, err)
			return ""
		}
		s := strings.TrimRight(string(b), "\r\n")
		if s == "" {
			fail("%s: %s is empty", what, p)
		}
		return s
	}

	cfg := &Config{Sources: map[string]string{}}
	// value picks a string setting by precedence: flag, environment, file, default.
	value := func(key, flag, env, fromFile, def string) string {
		if v, ok := o.Flags[key]; ok && flag != "" {
			cfg.Sources[key] = "flag --" + flag
			return v
		}
		if env != "" {
			if v, ok := lookupEnv(env); ok && v != "" {
				cfg.Sources[key] = "env " + env
				return v
			}
		}
		if fromFile != "" {
			cfg.Sources[key] = "file"
			return fromFile
		}
		cfg.Sources[key] = "default"
		return def
	}
	fileInt := func(v int) string {
		if v == 0 {
			return ""
		}
		return strconv.Itoa(v)
	}

	d := f.DVR
	cfg.DVR.Host = value("dvr.host", "dvr-host", EnvHost, d.Host, "")
	port := value("dvr.port", "dvr-port", EnvPort, fileInt(d.Port), "8000")
	cfg.DVR.Port = atoi(port)
	cfg.DVR.User = value("dvr.user", "dvr-user", EnvUser, d.User, "")
	if cfg.DVR.Host == "" {
		fail("dvr.host is required (or set %s)", EnvHost)
	}
	if cfg.DVR.User == "" {
		fail("dvr.user is required (or set %s)", EnvUser)
	}
	if !validPort(cfg.DVR.Port) {
		fail("dvr.port %q out of range", port)
	}

	// The password: --dvr-password-file, DVR_PASSWORD, DVR_PASSWORD_FILE, then the file's password_file.
	switch pwFile, isFlag := o.Flags["dvr.password_file"]; {
	case isFlag:
		cfg.Sources["dvr.password"] = "flag --dvr-password-file"
		cfg.DVR.Password = readSecret("--dvr-password-file", pwFile)
	default:
		if pw, ok := lookupEnv(EnvPassword); ok && pw != "" {
			cfg.Sources["dvr.password"] = "env " + EnvPassword
			cfg.DVR.Password = pw
		} else if p, ok := lookupEnv(EnvPasswordFile); ok && p != "" {
			cfg.Sources["dvr.password"] = "env " + EnvPasswordFile
			cfg.DVR.Password = readSecret(EnvPasswordFile, p)
		} else if d.PasswordFile != "" {
			cfg.Sources["dvr.password"] = "file (password_file)"
			cfg.DVR.Password = readSecret("dvr.password_file", d.PasswordFile)
		} else {
			fail("the DVR password is required: set %s or %s, or dvr.password_file", EnvPassword, EnvPasswordFile)
		}
	}

	cfg.DVR.Reconnect = time.Duration(d.ReconnectSeconds) * time.Second
	if d.ReconnectSeconds == 0 {
		cfg.DVR.Reconnect = 5 * time.Second
	}
	cfg.DVR.ReconnectMax = time.Duration(d.ReconnectMax) * time.Second
	if d.ReconnectMax == 0 {
		cfg.DVR.ReconnectMax = max(10*time.Second, cfg.DVR.Reconnect)
	}
	if d.ReconnectSeconds < 0 || d.ReconnectSeconds > 3600 {
		fail("dvr.reconnect_seconds %d out of range 1-3600", d.ReconnectSeconds)
	}
	if d.ReconnectMax < 0 || d.ReconnectMax > 3600 || d.ReconnectMax != 0 && cfg.DVR.ReconnectMax < cfg.DVR.Reconnect {
		fail("dvr.reconnect_max_seconds %d out of range reconnect_seconds-3600", d.ReconnectMax)
	}
	cfg.DVR.WebPort = d.WebPort
	if cfg.DVR.WebPort == 0 {
		cfg.DVR.WebPort = 80
	}
	if !validPort(cfg.DVR.WebPort) {
		fail("dvr.web_port %d out of range", cfg.DVR.WebPort)
	}

	fromFile := func(set bool) string {
		if set {
			return "file"
		}
		return "default"
	}
	cfg.Discovery = Discovery{Enabled: f.Discovery.Enabled == nil || *f.Discovery.Enabled, Interface: f.Discovery.Interface}
	cfg.Sources["discovery"] = fromFile(f.Discovery.Enabled != nil || f.Discovery.Interface != "")
	cfg.Web = Web{Enabled: f.Web.Enabled == nil || *f.Web.Enabled, Listen: f.Web.Listen}
	cfg.Sources["web"] = fromFile(f.Web.Enabled != nil || f.Web.Listen != "")
	if cfg.Web.Listen == "" {
		cfg.Web.Listen = ":8090"
	}
	if _, port, err := net.SplitHostPort(cfg.Web.Listen); err != nil || !validPort(atoi(port)) {
		fail("web.listen %q is not host:port", cfg.Web.Listen)
	}

	sn := f.Snapshot
	cfg.Snapshot = Snapshot{Enabled: sn.Enabled == nil || *sn.Enabled, FFmpeg: sn.FFmpeg, CacheFor: 5 * time.Second}
	cfg.Sources["snapshot"] = fromFile(sn.Enabled != nil || sn.FFmpeg != "" || sn.CacheSeconds != nil)
	if sn.CacheSeconds != nil {
		if *sn.CacheSeconds < 1 || *sn.CacheSeconds > 3600 {
			fail("snapshot.cache_seconds %d out of range 1-3600", *sn.CacheSeconds)
		}
		cfg.Snapshot.CacheFor = time.Duration(*sn.CacheSeconds) * time.Second
	}

	build := func(fc fileChannel, where string) Channel {
		s := merge(fc.settings, f.Defaults)
		shared := s.ListenIP == nil || *s.ListenIP == "" || *s.ListenIP == "0.0.0.0" || *s.ListenIP == "::"
		if s.RTSPPort == nil {
			s.RTSPPort = ptr(554)
			if shared {
				s.RTSPPort = ptr(sharedRTSPBase + fc.Index)
			}
		}
		if s.ONVIFPort == nil {
			s.ONVIFPort = ptr(80)
			if shared {
				s.ONVIFPort = ptr(sharedONVIFBase + fc.Index)
			}
		}
		s = merge(s, builtin)
		ch := Channel{
			Index:      fc.Index,
			Name:       fc.Name,
			Enabled:    *s.Enabled,
			Audio:      *s.Audio,
			RTSPPort:   *s.RTSPPort,
			RTSPPath:   *s.RTSPPath,
			ONVIFPort:  *s.ONVIFPort,
			Firmware:   *s.Firmware,
			FillerText: *s.FillerText,
		}
		if s.Model != nil {
			ch.Model = *s.Model
		}
		if *s.FillerImage != "" {
			ch.FillerImage, ch.FillerImagePath = readImage(where+": filler_image", *s.FillerImage)
		}
		if ch.Name == "" {
			ch.Name = fmt.Sprintf("CH%d", ch.Index)
		}
		if s.Serial != nil && *s.Serial != "" {
			ch.Serial = *s.Serial
		} else {
			ch.Serial = defaultSerial(cfg.DVR.Addr(), ch.Index)
		}
		if fc.MAC != "" {
			mac, err := net.ParseMAC(fc.MAC)
			switch {
			case err != nil || len(mac) != 6:
				fail("%s: mac %q is not a 6-byte MAC address", where, fc.MAC)
			case mac[0]&1 != 0 || slices.Equal(mac, make(net.HardwareAddr, 6)):
				fail("%s: mac %q is not a unicast address", where, fc.MAC)
			default:
				ch.MAC = mac
			}
		}
		if ip := *s.ListenIP; ip != "" {
			a, err := netip.ParseAddr(ip)
			if err != nil || !a.Is4() && !a.Is6() {
				fail("%s: listen_ip %q is not an IP address", where, ip)
			} else if !a.IsUnspecified() {
				ch.ListenIP = a
			}
		}
		if !validPort(ch.RTSPPort) {
			fail("%s: rtsp_port %d out of range", where, ch.RTSPPort)
		}
		if !validPort(ch.ONVIFPort) {
			fail("%s: onvif_port %d out of range", where, ch.ONVIFPort)
		}
		if !strings.HasPrefix(ch.RTSPPath, "/") || strings.ContainsAny(ch.RTSPPath, " ?#") {
			fail("%s: rtsp_path %q must start with / and have no spaces, ? or #", where, ch.RTSPPath)
		}
		ch.User, ch.Password = cfg.DVR.User, cfg.DVR.Password
		if s.User != nil && *s.User != "" {
			ch.User = *s.User
		}
		if s.PasswordFile != nil && *s.PasswordFile != "" && ch.Enabled {
			ch.Password = readSecret(where+": password_file", *s.PasswordFile)
		}
		return ch
	}

	if len(f.Channel) == 0 {
		cfg.Auto = true
		cfg.Sources["channels"] = "auto (from the DVR)"
		for i := 1; i <= MaxChannels; i++ {
			cfg.auto = append(cfg.auto, build(fileChannel{Index: i}, fmt.Sprintf("channel %d", i)))
		}
		errs = append(errs, checkEndpoints(cfg.auto)...)
	} else {
		cfg.Sources["channels"] = "file"
		seen := map[int]bool{}
		for i, fc := range f.Channel {
			where := fmt.Sprintf("channel[%d]", i)
			if fc.Index < 1 || fc.Index > MaxChannels {
				fail("%s: index %d out of range 1-%d", where, fc.Index, MaxChannels)
				continue
			}
			where = fmt.Sprintf("channel %d", fc.Index)
			if seen[fc.Index] {
				fail("%s: configured twice", where)
				continue
			}
			seen[fc.Index] = true
			cfg.Channels = append(cfg.Channels, build(fc, where))
		}
		slices.SortFunc(cfg.Channels, func(a, b Channel) int { return a.Index - b.Index })
		if len(cfg.Enabled()) == 0 {
			fail("no enabled channel")
		}
		errs = append(errs, checkEndpoints(cfg.Enabled())...)
		errs = append(errs, checkMACs(cfg.Enabled())...)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return cfg, nil
}

func validPort(p int) bool { return p > 0 && p < 65536 }

// defaultSerial is stable per DVR and channel, so NVRs keep recognizing the camera.
func defaultSerial(dvrAddr string, index int) string {
	sum := sha1.Sum([]byte(dvrAddr))
	return fmt.Sprintf("DVR%s-CH%d", strings.ToUpper(hex.EncodeToString(sum[:4])), index)
}

type endpoint struct {
	ip   netip.Addr
	port int
	what string
}

// checkEndpoints rejects two listeners on the same port where the addresses overlap.
func checkEndpoints(chs []Channel) []error {
	var eps []endpoint
	for _, ch := range chs {
		eps = append(eps,
			endpoint{ch.ListenIP, ch.RTSPPort, fmt.Sprintf("channel %d rtsp", ch.Index)},
			endpoint{ch.ListenIP, ch.ONVIFPort, fmt.Sprintf("channel %d onvif", ch.Index)})
	}
	var errs []error
	for i := range eps {
		for j := i + 1; j < len(eps); j++ {
			a, b := eps[i], eps[j]
			if a.port == b.port && (a.ip == b.ip || !a.ip.IsValid() || !b.ip.IsValid()) {
				errs = append(errs, fmt.Errorf("%s and %s both listen on port %d of the same address", a.what, b.what, a.port))
			}
		}
	}
	return errs
}

// checkMACs rejects two channels with the same configured mac: NVRs would see one camera.
func checkMACs(chs []Channel) []error {
	var errs []error
	seen := map[string]int{}
	for _, ch := range chs {
		if ch.MAC == nil {
			continue
		}
		if other, ok := seen[ch.MAC.String()]; ok {
			errs = append(errs, fmt.Errorf("channel %d and channel %d both have mac %s", other, ch.Index, ch.MAC))
			continue
		}
		seen[ch.MAC.String()] = ch.Index
	}
	return errs
}

// ListenAddr joins an optional IP and a port; the zero IP listens on all addresses.
func ListenAddr(ip netip.Addr, port int) string {
	if !ip.IsValid() {
		return net.JoinHostPort("", strconv.Itoa(port))
	}
	return netip.AddrPortFrom(ip, uint16(port)).String()
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}

// WebURL is the DVR's web root for the settings API.
func (d DVR) WebURL() string {
	return "http://" + net.JoinHostPort(d.Host, strconv.Itoa(d.WebPort))
}
