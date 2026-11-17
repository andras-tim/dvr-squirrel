// SPDX-License-Identifier: GPL-3.0-or-later

package webui

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andras-tim/dvr-squirrel/internal/dvrweb"
	"github.com/andras-tim/dvr-squirrel/internal/fakedvr"
	"github.com/andras-tim/dvr-squirrel/internal/logring"
)

func fld(name, value string) fakedvr.Field { return fakedvr.Field{Name: name, Value: value} }

type env struct {
	dvr  *fakedvr.WebServer
	ui   *Server
	srv  *httptest.Server
	now  time.Time
	secr []string
	reqs atomic.Int64 // requests the fake DVR received
}

func (e *env) dvrRequests() int64 { return e.reqs.Load() }

func newEnv(t *testing.T) *env {
	t.Helper()
	admin := fakedvr.WebAccount{Password: "adminpw"}
	for i := range admin.Perms {
		admin.Perms[i] = true
	}
	viewer := fakedvr.WebAccount{Password: "viewpw"}
	viewer.Perms[8] = true
	d := &fakedvr.WebServer{Accounts: map[string]fakedvr.WebAccount{"admin": admin, "viewer": viewer},
		PagePerm: map[string]int{"CgiAuth": 0, "Port": 8}}
	d.SetRecord("Screen", 0, fld("auto", "0"), fld("type", "1"), fld("bord", "0"), fld("md", "1"), fld("vga", "1"))
	d.SetRecord("CgiAuth", 0, fld("uc", "2"), fld("u0", "admin"), fld("p0", "adminpw"), fld("flag0", "1111-11111-11"),
		fld("u1", "viewer"), fld("p1", "viewpw"), fld("flag1", "0000-00010-11"))
	d.SetRecord("Port", 0, fld("p2", "8000"), fld("NUM_CAM", "4"))
	e := &env{dvr: d, now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), secr: []string{"adminpw", "viewpw"}}
	dvrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.reqs.Add(1)
		d.ServeHTTP(w, r)
	}))
	t.Cleanup(dvrSrv.Close)

	ring := logring.New(10, 0)
	slog.New(ring.Handler(slog.DiscardHandler)).Info("rtsp session opened", "remote", "192.0.2.77:4000")
	e.ui = New(Config{
		DVR:      &dvrweb.Client{BaseURL: dvrSrv.URL},
		Channels: 4,
		Status: func() Status {
			return Status{DVR: DVRStatus{Addr: "dvr.example.lan:8000", Online: true, Since: e.now, Channels: 4, LastError: "dial tcp dvr.example.lan:8000: refused"},
				Channels: []ChannelStatus{{Index: 1, Name: "Yard", Width: 352, Height: 288, FPS: 10.7, Clients: []string{"192.0.2.9:5000"}}},
				Events:   ring.Entries()}
		},
		Now: func() time.Time { return e.now },
	})
	e.srv = httptest.NewServer(e.ui)
	t.Cleanup(e.srv.Close)
	return e
}

type browser struct {
	t    *testing.T
	c    *http.Client
	base string
	env  *env
}

func (e *env) browser(t *testing.T) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, c: &http.Client{Jar: jar}, base: e.srv.URL, env: e}
}

func (b *browser) do(method, path string, form url.Values) (int, string) {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, b.base+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	page := string(out)
	for _, s := range b.env.secr {
		if strings.Contains(page, s) {
			b.t.Fatalf("%s %s: response contains a password", method, path)
		}
	}
	return resp.StatusCode, page
}

func (b *browser) login(user, pass string) (int, string) {
	return b.do("POST", "/login", url.Values{"user": {user}, "password": {pass}})
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (b *browser) csrf(page string) string {
	m := csrfRe.FindStringSubmatch(page)
	if m == nil {
		b.t.Fatal("no CSRF token in the page")
	}
	return m[1]
}

func TestLoginAndStatus(t *testing.T) {
	e := newEnv(t)
	b := e.browser(t)
	if code, page := b.do("GET", "/", nil); code != 200 || !strings.Contains(page, "Log in") {
		t.Fatalf("unauthenticated status: %d", code)
	}
	if code, page := b.login("admin", "nope"); code != 401 || !strings.Contains(page, "login failed") {
		t.Fatalf("wrong password: %d", code)
	}
	code, page := b.login("admin", "adminpw")
	if code != 200 || !strings.Contains(page, "Yard") || !strings.Contains(page, "352x288") || !strings.Contains(page, "192.0.2.9:5000") {
		t.Fatalf("status after login: %d\n%s", code, page)
	}

	// logout needs the CSRF token, then the session is gone
	if code, _ := b.do("POST", "/logout", url.Values{}); code != 403 {
		t.Fatalf("logout without token: %d", code)
	}
	b.do("POST", "/logout", url.Values{"csrf": {b.csrf(page)}})
	if _, page := b.do("GET", "/", nil); !strings.Contains(page, "Log in") {
		t.Fatal("still logged in after logout")
	}
}

func TestSessionExpires(t *testing.T) {
	e := newEnv(t)
	b := e.browser(t)
	b.login("viewer", "viewpw")
	e.now = e.now.Add(31 * time.Minute)
	if _, page := b.do("GET", "/", nil); !strings.Contains(page, "Log in") {
		t.Fatal("session survived its idle timeout")
	}
}

func TestViewerIsReadOnly(t *testing.T) {
	e := newEnv(t)
	b := e.browser(t)
	_, status := b.login("viewer", "viewpw")
	_, page := b.do("GET", "/settings/Screen", nil)
	if !strings.Contains(page, "denied") || strings.Contains(page, "<select") {
		t.Fatalf("viewer page:\n%s", page)
	}
	code, _ := b.do("POST", "/settings/Screen", url.Values{"csrf": {b.csrf(status)}, "bord": {"1"}})
	if code != 403 || len(e.dvr.Writes()) != 0 {
		t.Fatalf("viewer write: %d, writes %v", code, e.dvr.Writes())
	}
	if _, page := b.do("GET", "/settings/Port", nil); !strings.Contains(page, "8000") {
		t.Fatal("viewer cannot read a page its right allows")
	}
}

func TestAdminWritesWithDiff(t *testing.T) {
	e := newEnv(t)
	b := e.browser(t)
	b.login("admin", "adminpw")
	_, page := b.do("GET", "/settings/Screen", nil)
	if !strings.Contains(page, `<select name="bord">`) {
		t.Fatalf("no form:\n%s", page)
	}
	token := b.csrf(page)
	form := url.Values{"auto": {"0"}, "type": {"1"}, "bord": {"1"}, "md": {"1"}, "vga": {"1"}}
	if code, _ := b.do("POST", "/settings/Screen", form); code != 403 {
		t.Fatalf("write without CSRF token: %d", code)
	}
	form.Set("csrf", token)
	code, page := b.do("POST", "/settings/Screen", form)
	if code != 200 || !strings.Contains(page, "Saved to the DVR and read back: 1 field changed.") || !regexp.MustCompile(`Screen Border.*?</td><td>0</td><td>1</td>`).MatchString(page) {
		t.Fatalf("write: %d\n%s", code, page)
	}
	if w := e.dvr.Writes(); len(w) != 1 || w[0] != "auto=0&type=1&bord=1&md=1&vga=1" {
		t.Fatalf("writes %v", w)
	}
	// an unchanged form writes nothing
	if _, page := b.do("POST", "/settings/Screen", form); !strings.Contains(page, "nothing changed") || len(e.dvr.Writes()) != 1 {
		t.Fatal("an unchanged form was written")
	}
	// invalid values are refused before reaching the DVR
	form.Set("vga", "9")
	if _, page := b.do("POST", "/settings/Screen", form); !strings.Contains(page, "not one of the options") || len(e.dvr.Writes()) != 1 {
		t.Fatal("an invalid value was written")
	}
}

func TestAccountsNeverShowPasswords(t *testing.T) {
	e := newEnv(t)
	b := e.browser(t)
	b.login("admin", "adminpw")
	_, page := b.do("GET", "/settings/CgiAuth", nil)
	if !strings.Contains(page, "viewer") || !strings.Contains(page, "(secret, not shown)") || strings.Contains(page, "<form method=\"post\" action=\"/settings/CgiAuth") {
		t.Fatalf("accounts page:\n%s", page)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t)
	b := e.browser(t)
	for i := range limitPerClient {
		if code, _ := b.login("admin", "wrong"); code != 401 {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	before := e.dvrRequests()
	if code, page := b.login("admin", "adminpw"); code != 429 || !strings.Contains(page, "too many") {
		t.Fatalf("limited login: %d", code)
	}
	if e.dvrRequests() != before {
		t.Fatal("a rate-limited login reached the DVR")
	}
	e.now = e.now.Add(limitWindow)
	if code, _ := b.login("admin", "adminpw"); code != 200 {
		t.Fatalf("after the window: %d", code)
	}
}

func TestLoginRateLimitTotal(t *testing.T) {
	e := newEnv(t)
	for i := range limitTotal {
		e.ui.failed(fmt.Sprintf("192.0.2.%d", i))
	}
	if code, _ := e.browser(t).login("admin", "adminpw"); code != 429 {
		t.Fatalf("global limit: %d", code)
	}
}

func TestPublicStatus(t *testing.T) {
	e := newEnv(t)
	b := e.browser(t)
	code, page := b.do("GET", "/", nil)
	if code != 200 || !strings.Contains(page, "Yard") || !strings.Contains(page, "352x288") || !strings.Contains(page, "online") {
		t.Fatalf("anonymous status: %d\n%s", code, page)
	}
	for _, private := range []string{"rtsp session opened", "192.0.2.77", "192.0.2.9", "dvr.example.lan", "<h2>Recent events</h2>"} {
		if strings.Contains(page, private) {
			t.Fatalf("anonymous status shows %q", private)
		}
	}
	if !regexp.MustCompile(`<span class="clients">1</span>`).MatchString(page) {
		t.Fatal("anonymous status lacks the RTSP client count")
	}
	if code, _ := b.do("GET", "/settings", nil); code != 200 || len(e.dvr.Writes()) != 0 {
		t.Fatal("settings reachable without login")
	}
	if _, page := b.do("GET", "/settings", nil); !strings.Contains(page, `name="password"`) {
		t.Fatal("settings did not redirect to the login form")
	}

	_, page = b.login("admin", "adminpw")
	for _, want := range []string{"<h2>Recent events</h2>", "rtsp session opened", "192.0.2.9:5000", "dvr.example.lan:8000"} {
		if !strings.Contains(page, want) {
			t.Fatalf("logged-in status lacks %q", want)
		}
	}
}

// csp is the policy every page carries; the redesign must not loosen it.
const csp = "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; form-action 'self'"

var externalRef = regexp.MustCompile(`(?i)(src|href|action)\s*=\s*["']?\s*(https?:)?//|url\(\s*["']?(https?:)?//|@import`)

func TestPagesSelfContained(t *testing.T) {
	e := newEnv(t)
	b := e.browser(t)
	check := func(path string) string {
		t.Helper()
		resp, err := b.c.Get(b.base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if got := resp.Header.Get("Content-Security-Policy"); got != csp {
			t.Fatalf("%s: CSP %q", path, got)
		}
		if m := externalRef.Find(body); m != nil {
			t.Fatalf("%s: external reference %q", path, m)
		}
		if strings.Contains(string(body), "<script") {
			t.Fatalf("%s: has a script", path)
		}
		return string(body)
	}
	status := check("/")
	check("/login")
	b.login("admin", "adminpw")
	for _, p := range []string{"/", "/settings", "/settings/Screen", "/settings/CgiAuth", "/settings/ChannSet?ch=2"} {
		check(p)
	}

	// both themes: the light palette by default, the dark one for prefers-color-scheme: dark
	for _, want := range []string{"--ground: #f4f1ec", "--accent: #a14a22", "@media (prefers-color-scheme: dark)", "--ground: #11151c", "--accent: #e8936a", `class="facets"`} {
		if !strings.Contains(status, want) {
			t.Errorf("status page lacks %q", want)
		}
	}
}

func TestSettingsSidebarAndSquirrel(t *testing.T) {
	e := newEnv(t)
	b := e.browser(t)
	b.login("admin", "adminpw")
	_, page := b.do("GET", "/settings/Screen", nil)
	for _, want := range []string{`aria-label="Settings pages"`, `href="/settings/Screen" aria-current="page"`, `class="squirrel" aria-hidden="true"`,
		">Channels</span>", ">Recording</span>", ">Detection</span>", ">Network</span>", ">System</span>", "per channel", "read only"} {
		if !strings.Contains(page, want) {
			t.Errorf("settings page lacks %q", want)
		}
	}
}
