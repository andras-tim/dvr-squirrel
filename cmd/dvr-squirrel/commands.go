// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"time"

	"github.com/andras-tim/dvr-squirrel/internal/config"
	"github.com/andras-tim/dvr-squirrel/internal/devtype"
	"github.com/andras-tim/dvr-squirrel/internal/dvrproto"
	"github.com/andras-tim/dvr-squirrel/internal/dvrweb"
	"github.com/andras-tim/dvr-squirrel/internal/genconfig"
	"github.com/andras-tim/dvr-squirrel/internal/scan"
)

// generateConfig logs in once with the configured account, reads the channel count and the DVR's type, and
// writes a config file with one [[channel]] block per channel.
func generateConfig(args []string) int {
	fs := flag.NewFlagSet("dvr-squirrel generate-config", flag.ContinueOnError)
	df := newDVRFlags(fs)
	output := fs.String("output", "", "write the config to this file instead of standard output")
	force := fs.Bool("force", false, "overwrite an existing --output file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(df.options())
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}
	out, err := renderConfig(context.Background(), cfg, passwordFileFor(cfg, df))
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate-config:", err)
		return 1
	}
	if *output == "" {
		_, _ = os.Stdout.Write(out)
		return 0
	}
	if err := writeNew(*output, out, *force); err != nil {
		fmt.Fprintln(os.Stderr, "generate-config:", err)
		return 1
	}
	fmt.Fprintln(os.Stderr, "wrote", *output)
	return 0
}

// passwordFileFor is the password_file written into the config: the file the password came from, else a
// placeholder to create.
func passwordFileFor(cfg *config.Config, df *dvrFlags) string {
	if *df.pwFile != "" {
		return *df.pwFile
	}
	if p, ok := os.LookupEnv(config.EnvPasswordFile); ok && p != "" {
		return p
	}
	return "/etc/dvr-squirrel/dvr-password"
}

func renderConfig(ctx context.Context, cfg *config.Config, passwordFile string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	n, err := dvrproto.Probe(ctx, cfg.DVR.Addr(), cfg.DVR.User, cfg.DVR.Password, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("login to %s: %w", cfg.DVR.Addr(), err)
	}
	web := &dvrweb.Client{BaseURL: cfg.DVR.WebURL()}
	code, _, err := devtype.Detect(ctx, web, dvrweb.Credentials{User: cfg.DVR.User, Password: cfg.DVR.Password})
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: cannot read the DVR's type (Port.xml):", err)
	}
	var b bytes.Buffer
	err = genconfig.Write(&b, genconfig.Input{
		Host: cfg.DVR.Host, Port: cfg.DVR.Port, User: cfg.DVR.User, PasswordFile: passwordFile,
		Channels: n, Model: devtype.Model(code, n), TypeDesc: devtype.Describe(code, n),
	})
	return b.Bytes(), err
}

// writeNew writes a new file with mode 0640, refusing to replace an existing one unless force is set.
func writeNew(path string, data []byte, force bool) error {
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o640)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s exists; use --force to overwrite it", path)
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// discover scans networks for compatible DVRs without logging in.
func discover(args []string) int {
	fs := flag.NewFlagSet("dvr-squirrel discover", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: dvr-squirrel discover [flags] [CIDR...]\n\nFinds compatible DVRs, read-only and without logging in. Default: the host's IPv4 networks.")
		fs.PrintDefaults()
	}
	inFlight := fs.Int("parallel", 20, "hosts probed at once")
	maxHosts := fs.Int("max-hosts", 4096, "refuse to scan more addresses than this")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var prefixes []netip.Prefix
	for _, a := range fs.Args() {
		p, err := netip.ParsePrefix(a)
		if err != nil {
			addr, aerr := netip.ParseAddr(a)
			if aerr != nil {
				fmt.Fprintf(os.Stderr, "discover: %q is not a CIDR or an address\n", a)
				return 2
			}
			p = netip.PrefixFrom(addr, addr.BitLen())
		}
		prefixes = append(prefixes, p)
	}
	if len(prefixes) == 0 {
		var err error
		if prefixes, err = scan.LocalPrefixes(); err != nil {
			fmt.Fprintln(os.Stderr, "discover:", err)
			return 1
		}
	}
	hosts, err := scan.Hosts(prefixes, *maxHosts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "discover:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "scanning %d addresses in %v\n", len(hosts), prefixes)
	results := scan.Scan(context.Background(), hosts, scan.Options{InFlight: *inFlight})
	for _, r := range results {
		fmt.Println(r.Format())
	}
	if len(results) == 0 {
		fmt.Fprintln(os.Stderr, "no host with the DVR media port (8000) open")
	}
	return 0
}
