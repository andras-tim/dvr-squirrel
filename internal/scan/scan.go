// SPDX-License-Identifier: GPL-3.0-or-later

// Package scan finds compatible DVRs on a network, read-only and without logging in: a TCP connect to the
// media port, then the web server's banner and its unauthenticated /gw/Port.xml answer.
package scan

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andras-tim/dvr-squirrel/internal/dvrweb"
)

// Options of a scan.
type Options struct {
	MediaPort int // default 8000
	WebPort   int // default 80
	// InFlight is the number of hosts probed at once (default 20).
	InFlight int
	Timeout  time.Duration // per connection (default 800 ms)
}

// Result is one host with the DVR's media port open.
type Result struct {
	Addr       netip.Addr
	MediaPort  int
	WebPort    int    // 0 when the web server did not answer
	Server     string // the HTTP Server header
	Compatible bool   // the /gw API answered in the DVR's way
	TypeCode   string // dMainType, when readable without a login
	Channels   string // NUM_CAM, when readable without a login
}

func (o *Options) defaults() {
	if o.MediaPort == 0 {
		o.MediaPort = 8000
	}
	if o.WebPort == 0 {
		o.WebPort = 80
	}
	if o.InFlight <= 0 {
		o.InFlight = 20
	}
	if o.Timeout <= 0 {
		o.Timeout = 800 * time.Millisecond
	}
}

// Hosts lists the host addresses of the prefixes (without network and broadcast addresses for IPv4 /30 and
// wider), at most limit of them.
func Hosts(prefixes []netip.Prefix, limit int) ([]netip.Addr, error) {
	var out []netip.Addr
	for _, p := range prefixes {
		p = p.Masked()
		if !p.Addr().Is4() {
			return nil, fmt.Errorf("%s: only IPv4 prefixes are scanned", p)
		}
		bits := p.Bits()
		first, last := p.Addr(), lastAddr(p)
		if bits <= 30 {
			first, last = first.Next(), last.Prev()
		}
		for a := first; a.IsValid() && a.Compare(last) <= 0; a = a.Next() {
			if len(out) >= limit {
				return nil, fmt.Errorf("more than %d addresses; scan smaller networks", limit)
			}
			out = append(out, a)
		}
	}
	return out, nil
}

func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Addr().As4()
	host := uint32(1)<<(32-p.Bits()) - 1
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]) | host
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

// LocalPrefixes returns the IPv4 networks of the host's interfaces, without loopback and link-local ones.
func LocalPrefixes() ([]netip.Prefix, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Prefix
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		p, err := netip.ParsePrefix(ipn.String())
		if err != nil || !p.Addr().Is4() || p.Addr().IsLoopback() || p.Addr().IsLinkLocalUnicast() {
			continue
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// Scan probes the hosts and returns those with the media port open, in address order.
func Scan(ctx context.Context, hosts []netip.Addr, o Options) []Result {
	o.defaults()
	results := make([]*Result, len(hosts))
	sem := make(chan struct{}, o.InFlight)
	var wg sync.WaitGroup
	for i, h := range hosts {
		select {
		case <-ctx.Done():
		case sem <- struct{}{}:
			wg.Go(func() {
				defer func() { <-sem }()
				results[i] = probe(ctx, h, o)
			})
		}
	}
	wg.Wait()
	var out []Result
	for _, r := range results {
		if r != nil {
			out = append(out, *r)
		}
	}
	return out
}

func open(ctx context.Context, addr netip.Addr, port int, timeout time.Duration) bool {
	d := net.Dialer{Timeout: timeout}
	c, err := d.DialContext(ctx, "tcp", netip.AddrPortFrom(addr, uint16(port)).String())
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func probe(ctx context.Context, addr netip.Addr, o Options) *Result {
	if !open(ctx, addr, o.MediaPort, o.Timeout) {
		return nil
	}
	r := &Result{Addr: addr, MediaPort: o.MediaPort}
	if !open(ctx, addr, o.WebPort, o.Timeout) {
		return r
	}
	r.WebPort = o.WebPort
	base := "http://" + netip.AddrPortFrom(addr, uint16(o.WebPort)).String()
	hc := &http.Client{Timeout: 3 * o.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if resp, err := get(ctx, hc, base+"/"); err == nil {
		r.Server = resp.Header.Get("Server")
		resp.Body.Close()
	}
	resp, err := get(ctx, hc, base+"/gw/Port.xml?_="+strconv.FormatInt(time.Now().UnixMilli(), 10))
	if err != nil {
		return r
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	rec, code, err := dvrweb.ParseReply(body)
	switch {
	case err != nil:
	case rec == nil:
		r.Compatible = code == dvrweb.CodeNoQuery || code == dvrweb.CodeDenied
	default:
		_, hasPort := rec.Get("p2")
		_, hasLang := rec.Get("lang")
		r.Compatible = hasPort || hasLang
		r.TypeCode, _ = rec.Get("dMainType")
		r.Channels, _ = rec.Get("NUM_CAM")
	}
	return r
}

func get(ctx context.Context, hc *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "dvr-squirrel-discover")
	return hc.Do(req)
}

// Format is a result as one line.
func (r Result) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s media=%d", r.Addr, r.MediaPort)
	if r.WebPort != 0 {
		fmt.Fprintf(&b, " web=%d", r.WebPort)
	}
	if r.Server != "" {
		fmt.Fprintf(&b, " server=%q", r.Server)
	}
	fmt.Fprintf(&b, " compatible=%t", r.Compatible)
	if r.TypeCode != "" {
		fmt.Fprintf(&b, " dMainType=%s", r.TypeCode)
	}
	if r.Channels != "" {
		fmt.Fprintf(&b, " channels=%s", r.Channels)
	}
	return b.String()
}
