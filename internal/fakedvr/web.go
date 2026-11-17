// SPDX-License-Identifier: GPL-3.0-or-later

package fakedvr

import (
	"encoding/base64"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// WebAccount is an account of the fake DVR web API; Perms[p] answers perm.xml?p=N.
type WebAccount struct {
	Password string
	Perms    [10]bool
}

// WebServer imitates the DVR's /gw/*.xml settings API: cookie auth, `_`-style query requirement,
// reads of stored records and writes that update them.
type WebServer struct {
	Accounts map[string]WebAccount
	// PagePerm is the right a page needs, for reads and writes alike; pages not listed need p=7.
	PagePerm map[string]int

	mu      sync.Mutex
	records map[string][]Field // key: page or page?ch=N
	writes  []string           // raw query strings of the write requests
}

// Field is one field of a stored record.
type Field struct{ Name, Value string }

// SetRecord stores a page's record; ch > 0 for a per-channel page.
func (s *WebServer) SetRecord(page string, ch int, fields ...Field) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records == nil {
		s.records = map[string][]Field{}
	}
	s.records[key(page, ch)] = fields
}

// Record returns a copy of a stored record.
func (s *WebServer) Record(page string, ch int) []Field {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Field(nil), s.records[key(page, ch)]...)
}

// Writes returns the raw query strings of the write requests received so far.
func (s *WebServer) Writes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writes...)
}

func key(page string, ch int) string {
	if ch > 0 {
		return fmt.Sprintf("%s?ch=%d", page, ch)
	}
	return page
}

func webReply(w http.ResponseWriter, fields ...Field) {
	w.Header().Set("Content-Type", "text/xml")
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><r>`)
	for _, f := range fields {
		fmt.Fprintf(&b, "<%s>%s</%s>", f.Name, html.EscapeString(f.Value), f.Name)
	}
	b.WriteString("</r>")
	_, _ = w.Write([]byte(b.String()))
}

func webStatus(w http.ResponseWriter, code int) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><r>%d</r>`, code)
}

func (s *WebServer) account(r *http.Request) (WebAccount, bool) {
	c, err := r.Cookie("account")
	if err != nil {
		return WebAccount{}, false
	}
	b, err := base64.StdEncoding.DecodeString(c.Value)
	if err != nil {
		return WebAccount{}, false
	}
	user, pass, _ := strings.Cut(string(b), ":")
	a, ok := s.Accounts[user]
	return a, ok && a.Password == pass
}

func (s *WebServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, ok := strings.CutPrefix(r.URL.Path, "/gw/")
	name, ok2 := strings.CutSuffix(name, ".xml")
	if !ok || !ok2 {
		http.NotFound(w, r)
		return
	}
	if r.URL.RawQuery == "" {
		webStatus(w, 11)
		return
	}
	acc, authed := s.account(r)
	q := r.URL.Query()
	if name == "perm" {
		p, _ := strconv.Atoi(q.Get("p"))
		v := "0"
		if authed && p >= 0 && p < len(acc.Perms) && acc.Perms[p] {
			v = "1"
		}
		webReply(w, Field{"p", v})
		return
	}
	page, write := strings.CutSuffix(name, "A")
	perm, listed := s.PagePerm[page]
	if !listed {
		perm = 7
	}
	if !authed || !acc.Perms[perm] {
		webStatus(w, 30)
		return
	}
	ch, _ := strconv.Atoi(q.Get("ch"))
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(page, ch)
	rec, found := s.records[k]
	if !found {
		http.NotFound(w, r)
		return
	}
	if !write {
		webReply(w, append([]Field{{"lang", "0"}}, rec...)...)
		return
	}
	s.writes = append(s.writes, r.URL.RawQuery)
	for i, f := range rec {
		if v, ok := q[f.Name]; ok {
			rec[i].Value = v[len(v)-1]
		}
	}
	webStatus(w, 0)
}
