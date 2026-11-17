// SPDX-License-Identifier: GPL-3.0-or-later

package scan

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func port(t *testing.T, addr string) int {
	t.Helper()
	_, p, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(p)
	return n
}

func TestScanLocalhost(t *testing.T) {
	media, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	go func() {
		for {
			c, err := media.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	var sawQuery, sawCookie atomic.Bool
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "Boa/0.94.14rc21")
		if r.URL.Path == "/gw/Port.xml" {
			sawQuery.Store(r.URL.RawQuery != "")
			_, c := r.Header["Cookie"]
			sawCookie.Store(c)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><r><lang>0</lang><p2></p2><NUM_CAM>4</NUM_CAM><dMainType>504</dMainType></r>`))
		}
	}))
	defer web.Close()

	hosts := []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("127.0.0.2")}
	res := Scan(t.Context(), hosts, Options{MediaPort: port(t, media.Addr().String()), WebPort: port(t, web.Listener.Addr().String()), Timeout: time.Second})
	if len(res) != 1 {
		t.Fatalf("%+v", res)
	}
	r := res[0]
	if !r.Compatible || r.TypeCode != "504" || r.Channels != "4" || !strings.HasPrefix(r.Server, "Boa") || !sawQuery.Load() || sawCookie.Load() {
		t.Fatalf("%+v query=%t cookie=%t", r, sawQuery.Load(), sawCookie.Load())
	}
	if !strings.Contains(r.Format(), "dMainType=504") {
		t.Fatal(r.Format())
	}
}

func TestHosts(t *testing.T) {
	h, err := Hosts([]netip.Prefix{netip.MustParsePrefix("192.0.2.0/30"), netip.MustParsePrefix("198.51.100.7/32")}, 100)
	if err != nil || len(h) != 3 || h[0].String() != "192.0.2.1" || h[1].String() != "192.0.2.2" || h[2].String() != "198.51.100.7" {
		t.Fatalf("%v %v", h, err)
	}
	if _, err := Hosts([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, 4096); err == nil {
		t.Fatal("no limit")
	}
}
