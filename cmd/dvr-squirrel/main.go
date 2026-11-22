// SPDX-License-Identifier: GPL-3.0-or-later

// Command dvr-squirrel republishes the channels of a HiSilicon-based DVR as ONVIF cameras.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/andras-tim/dvr-squirrel/internal/config"
	"github.com/andras-tim/dvr-squirrel/internal/ffmpeg"
	"github.com/andras-tim/dvr-squirrel/internal/logring"
)

// dvrReadTimeout ends a silent DVR session; the channels switch to filler after the same time.
const dvrReadTimeout = 10 * time.Second

// Set at build time by the Makefile and GoReleaser: -ldflags '-X main.version=... -X main.commit=... -X main.date=...'.
var (
	version = ""
	commit  = ""
	date    = ""
)

const usage = `Usage:
  dvr-squirrel [flags]                    run the bridge
  dvr-squirrel generate-config [flags]    print a config file for the DVR (channels, detected model)
  dvr-squirrel discover [CIDR...]         find compatible DVRs, read-only, without logging in

The DVR is given by flags, the environment (DVR_HOST, DVR_PORT, DVR_USER, DVR_PASSWORD or DVR_PASSWORD_FILE)
or the config file, in this order of precedence.

Flags:
`

// dvrFlags are the flags shared by the bridge and generate-config.
type dvrFlags struct {
	fs                               *flag.FlagSet
	config, host, port, user, pwFile *string
}

func newDVRFlags(fs *flag.FlagSet) *dvrFlags {
	return &dvrFlags{
		fs:     fs,
		config: fs.String("config", config.DefaultPath, "config file; optional unless given explicitly"),
		host:   fs.String("dvr-host", "", "DVR host name or address (env DVR_HOST)"),
		port:   fs.String("dvr-port", "", "DVR media port, default 8000 (env DVR_PORT)"),
		user:   fs.String("dvr-user", "", "DVR account, a view-only one is enough (env DVR_USER)"),
		pwFile: fs.String("dvr-password-file", "", "file holding the DVR password (env DVR_PASSWORD_FILE or DVR_PASSWORD)"),
	}
}

// options turns the flags given on the command line into config options.
func (f *dvrFlags) options() config.Options {
	o := config.Options{Path: *f.config, Flags: map[string]string{}}
	keys := map[string]string{"dvr-host": "dvr.host", "dvr-port": "dvr.port", "dvr-user": "dvr.user", "dvr-password-file": "dvr.password_file"}
	f.fs.Visit(func(fl *flag.Flag) {
		if fl.Name == "config" {
			o.PathExplicit = true
		}
		if k, ok := keys[fl.Name]; ok {
			o.Flags[k] = fl.Value.String()
		}
	})
	return o
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "generate-config":
			os.Exit(generateConfig(os.Args[2:]))
		case "discover":
			os.Exit(discover(os.Args[2:]))
		}
	}
	fs := flag.NewFlagSet("dvr-squirrel", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), usage); fs.PrintDefaults() }
	df := newDVRFlags(fs)
	checkConfig := fs.Bool("check-config", false, "print the effective config and where each value came from, then exit")
	logLevel := fs.String("log-level", "info", "log level: debug, info, warn or error")
	showVersion := fs.Bool("version", false, "print the version and exit")
	_ = fs.Parse(os.Args[1:])

	if *showVersion {
		fmt.Println(versionString())
		return
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		fmt.Fprintln(os.Stderr, "invalid --log-level:", err)
		os.Exit(2)
	}
	ring := logring.New(200, slog.LevelInfo)
	log := slog.New(ring.Handler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	opts := df.options()
	cfg, err := config.Load(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}
	if *checkConfig {
		printConfig(os.Stdout, cfg)
		return
	}
	if cfg.File == "" {
		log.Info("no config file, using the environment and the defaults", "looked_for", opts.Path)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, log, ring); err != nil {
		log.Error("exiting", "err", err)
		os.Exit(1)
	}
}

// printConfig shows the effective config and where its values came from, without secrets.
func printConfig(w io.Writer, cfg *config.Config) {
	file := cfg.File
	if file == "" {
		file = "(none)"
	}
	src := func(k string) string { return cfg.Sources[k] }
	fmt.Fprintf(w, "config OK, file %s\n", file)
	fmt.Fprintf(w, "dvr.host     = %s  [%s]\n", cfg.DVR.Host, src("dvr.host"))
	fmt.Fprintf(w, "dvr.port     = %d  [%s]\n", cfg.DVR.Port, src("dvr.port"))
	fmt.Fprintf(w, "dvr.user     = %s  [%s]\n", cfg.DVR.User, src("dvr.user"))
	fmt.Fprintf(w, "dvr.password = (set)  [%s]\n", src("dvr.password"))
	fmt.Fprintf(w, "dvr.reconnect = %s up to %s, web_port = %d\n", cfg.DVR.Reconnect, cfg.DVR.ReconnectMax, cfg.DVR.WebPort)
	fmt.Fprintf(w, "web          = enabled=%t listen=%q  [%s]\n", cfg.Web.Enabled, cfg.Web.Listen, src("web"))
	fmt.Fprintf(w, "discovery    = enabled=%t interface=%q  [%s]\n", cfg.Discovery.Enabled, cfg.Discovery.Interface, src("discovery"))
	ff := cfg.Snapshot.FFmpeg
	if ff == "" {
		ff = "ffmpeg from PATH"
		if ffmpeg.Available() {
			ff = "embedded"
		}
	}
	fmt.Fprintf(w, "snapshot     = enabled=%t ffmpeg=%q cache=%s  [%s]\n", cfg.Snapshot.Enabled, ff, cfg.Snapshot.CacheFor, src("snapshot"))
	fmt.Fprintf(w, "channels     = [%s]\n", src("channels"))
	chs := cfg.Channels
	if cfg.Auto {
		fmt.Fprintf(w, "  every channel the DVR reports; channel n gets these defaults (n = 1 shown):\n")
		chs = cfg.ChannelsFor(1)
	}
	sort.Slice(chs, func(i, j int) bool { return chs[i].Index < chs[j].Index })
	for _, ch := range chs {
		model := ch.Model
		if model == "" {
			model = "(detected from the DVR)"
		}
		mac, macSource, err := channelMAC(ch)
		macText := "(" + macSource + ")"
		if mac != nil {
			macText = mac.String() + " (" + macSource + ")"
		}
		if err != nil {
			macText += " [" + err.Error() + "]"
		}
		fmt.Fprintf(w, "  channel %d %q enabled=%t model=%q onvif=%s rtsp=%s%s user=%q serial=%q mac=%s\n",
			ch.Index, ch.Name, ch.Enabled, model, config.ListenAddr(ch.ListenIP, ch.ONVIFPort),
			config.ListenAddr(ch.ListenIP, ch.RTSPPort), ch.RTSPPath, ch.User, ch.Serial, macText)
	}
}
