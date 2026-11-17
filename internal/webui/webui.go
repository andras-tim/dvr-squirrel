// SPDX-License-Identifier: GPL-3.0-or-later

// Package webui serves the admin/status web UI: the bridge's status and the DVR's settings, read and
// written with the logged-in user's own DVR credentials, which live only in the in-memory session.
package webui

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/andras-tim/dvr-squirrel/internal/dvrweb"
	"github.com/andras-tim/dvr-squirrel/internal/logring"
)

//go:embed assets/favicon.ico assets/logo.png
var assets embed.FS

// Status is what the status page shows.
type Status struct {
	DVR      DVRStatus
	Channels []ChannelStatus
	Events   []logring.Entry
}

// DVRStatus is the state of the DVR media session.
type DVRStatus struct {
	Addr      string
	Online    bool
	Since     time.Time
	Channels  int    // as reported at login
	Type      string // e.g. "DVR 500 series, 4 channels (type 504)"
	LastError string
}

// ChannelStatus is one bridged channel.
type ChannelStatus struct {
	Index        int
	Name         string
	Offline      bool
	OfflineSince time.Time
	Width        int
	Height       int
	FPS          float64
	Kbps         float64
	Frames       uint64
	LastIDR      time.Time
	Clients      []string
	ClientCount  int // set by the status page
}

// Config of the UI.
type Config struct {
	DVR *dvrweb.Client
	// Channels is the channel count offered on per-channel pages.
	Channels int
	Status   func() Status
	Log      *slog.Logger
	// SessionTTL is the idle timeout of a login (default 30 min).
	SessionTTL time.Duration
	Now        func() time.Time
}

// Server is the UI's HTTP handler.
type Server struct {
	cfg  Config
	mux  *http.ServeMux
	tmpl *template.Template

	mu       sync.Mutex
	sessions map[string]*session
	failures []failure // failed logins within the last limitWindow
}

type failure struct {
	at time.Time
	ip string
}

// Failed logins allowed per limitWindow, from one client and in total, before the login answers 429
// without asking the DVR: the UI must not be a way to hammer the DVR's accounts.
const (
	limitWindow    = time.Minute
	limitPerClient = 5
	limitTotal     = 20
)

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limited drops expired failures and reports whether ip may try a login now.
func (s *Server) limited(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	kept := s.failures[:0]
	perIP := 0
	for _, f := range s.failures {
		if now.Sub(f.at) < limitWindow {
			kept = append(kept, f)
			if f.ip == ip {
				perIP++
			}
		}
	}
	s.failures = kept
	return perIP >= limitPerClient || len(kept) >= limitTotal
}

func (s *Server) failed(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, failure{s.now(), ip})
}

type session struct {
	csrf  string
	cred  dvrweb.Credentials
	perms [dvrweb.MaxPerm + 1]bool
	last  time.Time
}

const cookieName = "dvrui_session"

// New returns the UI handler.
func New(cfg Config) *Server {
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 30 * time.Minute
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Channels <= 0 {
		cfg.Channels = 4
	}
	s := &Server{cfg: cfg, sessions: map[string]*session{}, tmpl: template.Must(template.New("ui").Funcs(funcs).Parse(templates))}
	m := http.NewServeMux()
	m.HandleFunc("GET /favicon.ico", serveAsset("assets/favicon.ico", "image/x-icon"))
	m.HandleFunc("GET /logo.png", serveAsset("assets/logo.png", "image/png"))
	m.HandleFunc("GET /login", s.loginForm)
	m.HandleFunc("POST /login", s.login)
	m.HandleFunc("POST /logout", s.auth(s.logout))
	m.HandleFunc("GET /{$}", s.status)
	m.HandleFunc("GET /settings", s.auth(s.settingsIndex))
	m.HandleFunc("GET /settings/{page}", s.auth(s.settingsPage))
	m.HandleFunc("POST /settings/{page}", s.auth(s.settingsWrite))
	s.mux = m
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; form-action 'self'")
	w.Header().Set("Cache-Control", "no-store")
	s.mux.ServeHTTP(w, r)
}

func (s *Server) now() time.Time {
	if s.cfg.Now != nil {
		return s.cfg.Now()
	}
	return time.Now()
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// current returns the request's live session, renewing its idle timer.
func (s *Server) current(r *http.Request) *session {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for id, ss := range s.sessions {
		if now.Sub(ss.last) > s.cfg.SessionTTL {
			delete(s.sessions, id)
		}
	}
	ss := s.sessions[c.Value]
	if ss != nil {
		ss.last = now
	}
	return ss
}

type handler func(w http.ResponseWriter, r *http.Request, ss *session)

// auth requires a session, and on POST a matching CSRF token.
func (s *Server) auth(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ss := s.current(r)
		if ss == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost {
			if subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(ss.csrf)) != 1 {
				http.Error(w, "invalid or missing CSRF token", http.StatusForbidden)
				return
			}
		}
		h(w, r, ss)
	}
}

func (s *Server) render(w http.ResponseWriter, name string, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		s.cfg.Log.Warn("web ui template failed", "template", name, "err", err)
	}
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, "login", map[string]any{"Error": ""})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.limited(ip) {
		s.cfg.Log.Warn("web ui login rate limited", "remote", ip)
		w.Header().Set("Retry-After", strconv.Itoa(int(limitWindow.Seconds())))
		w.WriteHeader(http.StatusTooManyRequests)
		s.render(w, "login", map[string]any{"Error": "too many failed logins, try again in a minute"})
		return
	}
	cred := dvrweb.Credentials{User: r.PostFormValue("user"), Password: r.PostFormValue("password")}
	perms, err := s.cfg.DVR.Perms(r.Context(), cred)
	granted := false
	for _, p := range perms {
		granted = granted || p
	}
	if err != nil || !granted {
		msg := "login failed: wrong user name or password, or the account has no rights"
		if err != nil {
			msg = "the DVR did not answer: " + err.Error()
		}
		if err == nil {
			s.failed(ip)
		}
		s.cfg.Log.Info("web ui login failed", "user", cred.User, "remote", r.RemoteAddr, "dvr_error", err != nil)
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, "login", map[string]any{"Error": msg})
		return
	}
	id := randomToken()
	s.mu.Lock()
	s.sessions[id] = &session{csrf: randomToken(), cred: cred, perms: perms, last: s.now()}
	s.mu.Unlock()
	s.cfg.Log.Info("web ui login", "user", cred.User, "remote", r.RemoteAddr)
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, _ *session) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) base(ss *session) map[string]any {
	return map[string]any{"LoggedIn": true, "User": ss.cred.User, "CSRF": ss.csrf, "Pages": dvrweb.Pages,
		"Now": s.now(), "Active": "settings"}
}

// status is public; without a login it leaves out what may hold addresses: the DVR address, its last error,
// the RTSP client addresses (only their count is shown) and the log events.
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	d := map[string]any{"LoggedIn": false, "Now": s.now()}
	ss := s.current(r)
	if ss != nil {
		d = s.base(ss)
	}
	d["Active"] = "status"
	if s.cfg.Status != nil {
		st := s.cfg.Status()
		for i := range st.Channels {
			st.Channels[i].ClientCount = len(st.Channels[i].Clients)
		}
		if ss == nil {
			st.DVR.Addr, st.DVR.LastError, st.Events = "", "", nil
			for i := range st.Channels {
				st.Channels[i].Clients = nil
			}
		}
		d["Status"] = st
	}
	s.render(w, "status", d)
}

func (s *Server) settingsIndex(w http.ResponseWriter, r *http.Request, ss *session) {
	d := s.base(ss)
	d["Nav"] = sidebar(ss.perms, "")
	s.render(w, "settings", d)
}

// pageView is one settings page as rendered.
type pageView struct {
	Page     *dvrweb.Page
	Ch       int
	Channels []int
	Writable bool
	Fields   []fieldView
	Extra    []dvrweb.Param // read fields outside the form (read-only pages)
	Error    string
	Changes  []dvrweb.Change
	Written  bool
}

type fieldView struct {
	F       *dvrweb.Field
	Value   string
	Present bool
}

func (s *Server) pageAndChannel(w http.ResponseWriter, r *http.Request) (*dvrweb.Page, int, bool) {
	p := dvrweb.FindPage(r.PathValue("page"))
	if p == nil {
		http.NotFound(w, r)
		return nil, 0, false
	}
	ch := 0
	if p.PerChannel {
		ch = 1
		if v, err := strconv.Atoi(r.URL.Query().Get("ch")); err == nil && v >= 1 && v <= s.cfg.Channels {
			ch = v
		}
	}
	return p, ch, true
}

func (s *Server) view(p *dvrweb.Page, ch int, ss *session, rec *dvrweb.Record, err error) pageView {
	v := pageView{Page: p, Ch: ch, Writable: !p.ReadOnly && ss.perms[p.Perm]}
	if p.PerChannel {
		for i := 1; i <= s.cfg.Channels; i++ {
			v.Channels = append(v.Channels, i)
		}
	}
	if err != nil {
		v.Error = errorText(err)
		v.Writable = false
		return v
	}
	for i := range p.Fields {
		f := &p.Fields[i]
		if f.Kind == dvrweb.Const || f.Kind == dvrweb.Hidden {
			continue
		}
		val, ok := rec.Get(f.Name)
		if f.Kind == dvrweb.Secret {
			val = ""
		}
		v.Fields = append(v.Fields, fieldView{F: f, Value: val, Present: ok})
	}
	if len(p.Fields) == 0 {
		for _, f := range rec.Fields {
			if dvrweb.IsSecret(p, f.Name) {
				f.Value = "(secret, not shown)"
			}
			v.Extra = append(v.Extra, f)
		}
	}
	return v
}

func errorText(err error) string {
	if dvrweb.IsDenied(err) {
		return "the DVR denied access to this page for your account"
	}
	return err.Error()
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request, ss *session) {
	p, ch, ok := s.pageAndChannel(w, r)
	if !ok {
		return
	}
	rec, err := s.cfg.DVR.Load(r.Context(), ss.cred, p, ch)
	d := s.base(ss)
	d["Nav"] = sidebar(ss.perms, p.Name)
	d["View"] = s.view(p, ch, ss, rec, err)
	s.render(w, "page", d)
}

func (s *Server) settingsWrite(w http.ResponseWriter, r *http.Request, ss *session) {
	p, ch, ok := s.pageAndChannel(w, r)
	if !ok {
		return
	}
	d := s.base(ss)
	d["Nav"] = sidebar(ss.perms, p.Name)
	if p.ReadOnly || !ss.perms[p.Perm] {
		w.WriteHeader(http.StatusForbidden)
		d["View"] = pageView{Page: p, Ch: ch, Error: "your account may not change this page"}
		s.render(w, "page", d)
		return
	}
	before, err := s.cfg.DVR.Load(r.Context(), ss.cred, p, ch)
	if err != nil {
		d["View"] = s.view(p, ch, ss, nil, err)
		s.render(w, "page", d)
		return
	}
	edits := map[string]string{}
	for i := range p.Fields {
		f := &p.Fields[i]
		if f.Kind != dvrweb.Select && f.Kind != dvrweb.Text && f.Kind != dvrweb.Secret {
			continue
		}
		v, sent := r.PostForm[f.Name]
		if !sent {
			continue
		}
		cur, _ := before.Get(f.Name)
		if f.Kind == dvrweb.Secret && v[0] != "" || f.Kind != dvrweb.Secret && v[0] != cur {
			edits[f.Name] = v[0]
		}
	}
	view := func(rec *dvrweb.Record, err error) pageView { return s.view(p, ch, ss, rec, err) }
	if len(edits) == 0 {
		v := view(before, nil)
		v.Error = "nothing changed"
		d["View"] = v
		s.render(w, "page", d)
		return
	}
	params, err := dvrweb.BuildWrite(p, before, ch, edits)
	if err == nil {
		_, err = s.cfg.DVR.Write(r.Context(), ss.cred, p.Name, params)
	}
	if err != nil {
		v := view(before, nil)
		v.Error = errorText(err)
		d["View"] = v
		s.render(w, "page", d)
		return
	}
	names := make([]string, 0, len(edits))
	for n := range edits {
		names = append(names, n)
	}
	s.cfg.Log.Info("dvr settings written from the web ui", "user", ss.cred.User, "page", p.Name, "ch", ch, "fields", names)
	after, err := s.cfg.DVR.Load(r.Context(), ss.cred, p, ch)
	v := view(after, err)
	if err == nil {
		v.Changes = dvrweb.Diff(p, before, after)
	}
	v.Written = true
	d["View"] = v
	s.render(w, "page", d)
}

// serveAsset serves one embedded file; the logo and the favicon need no login.
func serveAsset(name, contentType string) http.HandlerFunc {
	data, err := assets.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(data)
	}
}
