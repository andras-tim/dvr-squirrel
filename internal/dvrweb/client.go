// SPDX-License-Identifier: GPL-3.0-or-later

// Package dvrweb talks to the DVR's web settings API (/gw/*.xml): reads and writes of the settings pages
// and the permission checks, with the caller's DVR credentials.
package dvrweb

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Credentials of a DVR account. They are sent with every request, as the DVR keeps no session.
type Credentials struct {
	User     string
	Password string
}

// Param is one query parameter; the order of a write request follows the DVR's own form.
type Param struct {
	Name, Value string
}

// Record is a page as read: its fields in reply order.
type Record struct {
	Fields []Param
}

// Get returns a field's value.
func (r *Record) Get(name string) (string, bool) {
	for _, f := range r.Fields {
		if f.Name == name {
			return f.Value, true
		}
	}
	return "", false
}

// StatusError is a reply that carries only a status code, `<r>N</r>`.
type StatusError struct{ Code int }

// Known status codes.
const (
	CodeNoQuery = 11 // the request had no query string
	CodeDenied  = 30 // permission denied
)

func (e *StatusError) Error() string {
	switch e.Code {
	case CodeNoQuery:
		return "dvr: request without a query string (r=11)"
	case CodeDenied:
		return "dvr: permission denied (r=30)"
	}
	return fmt.Sprintf("dvr: status %d", e.Code)
}

// IsDenied reports whether err is the DVR's permission denied answer.
func IsDenied(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Code == CodeDenied
}

// Client of one DVR's settings API.
type Client struct {
	// BaseURL is the DVR's web root, e.g. http://dvr.example.lan.
	BaseURL string
	HTTP    *http.Client
	Now     func() time.Time
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// do sends GET /gw/<name>.xml with params. A reply that carries only a number is a StatusError
// when it is a known error code, and a Record holding `r` otherwise.
func (c *Client) do(ctx context.Context, cred Credentials, name string, params []Param) (*Record, error) {
	var q strings.Builder
	for i, p := range params {
		if i > 0 {
			q.WriteByte('&')
		}
		q.WriteString(url.QueryEscape(p.Name) + "=" + url.QueryEscape(p.Value))
	}
	u := strings.TrimRight(c.BaseURL, "/") + "/gw/" + name + ".xml?" + q.String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.AddCookie(&http.Cookie{Name: "account", Value: base64.StdEncoding.EncodeToString([]byte(cred.User + ":" + cred.Password))})
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		// The URL carries no secret, but keep the error short: the cookie never appears in it.
		return nil, fmt.Errorf("dvr %s: %w", name, errors.Unwrap(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dvr %s: HTTP %d", name, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("dvr %s: %w", name, err)
	}
	rec, code, err := parse(body)
	if err != nil {
		return nil, fmt.Errorf("dvr %s: %w", name, err)
	}
	if rec == nil {
		if code == CodeNoQuery || code == CodeDenied {
			return nil, &StatusError{Code: code}
		}
		return &Record{Fields: []Param{{"r", strconv.Itoa(code)}}}, nil
	}
	return rec, nil
}

// parse reads a flat XML reply: a root whose children are leaf fields, or a root holding only a number.
func parse(b []byte) (*Record, int, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	depth := 0
	var rec Record
	var text strings.Builder
	var field string
	children := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, fmt.Errorf("malformed reply: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if depth == 2 {
				field, children = t.Name.Local, true
				text.Reset()
			} else if depth > 2 {
				return nil, 0, fmt.Errorf("unexpected nesting at <%s>", t.Name.Local)
			} else {
				text.Reset()
			}
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			if depth == 2 {
				rec.Fields = append(rec.Fields, Param{field, text.String()})
			}
			depth--
		}
	}
	if children {
		return &rec, 0, nil
	}
	code, err := strconv.Atoi(strings.TrimSpace(text.String()))
	if err != nil {
		return nil, 0, fmt.Errorf("reply is neither a record nor a status code")
	}
	return nil, code, nil
}

// cacheBuster is the jQuery-style `_` parameter; the DVR rejects requests without a query string.
func (c *Client) cacheBuster() Param {
	return Param{"_", strconv.FormatInt(c.now().UnixMilli(), 10)}
}

// Read reads a page; ch > 0 selects a channel (1-based) on per-channel pages.
func (c *Client) Read(ctx context.Context, cred Credentials, page string, ch int) (*Record, error) {
	var params []Param
	if ch > 0 {
		params = append(params, Param{"ch", strconv.Itoa(ch)})
	}
	return c.do(ctx, cred, page, append(params, c.cacheBuster()))
}

// Write sends a page's write request (<page>A.xml) with params in the given order.
func (c *Client) Write(ctx context.Context, cred Credentials, page string, params []Param) (*Record, error) {
	return c.do(ctx, cred, page+"A", params)
}

// MaxPerm is the highest permission index the DVR's UI checks.
const MaxPerm = 9

// Perms returns the account's rights p=0..MaxPerm (perm.xml). A wrong password grants none.
func (c *Client) Perms(ctx context.Context, cred Credentials) ([MaxPerm + 1]bool, error) {
	var out [MaxPerm + 1]bool
	for p := range out {
		rec, err := c.do(ctx, cred, "perm", []Param{{"p", strconv.Itoa(p)}, c.cacheBuster()})
		if IsDenied(err) {
			continue
		}
		if err != nil {
			return out, err
		}
		v, _ := rec.Get("p")
		out[p] = v == "1"
	}
	return out, nil
}

// ParseReply parses a /gw reply: a record, or only a status code (rec == nil).
func ParseReply(b []byte) (rec *Record, code int, err error) { return parse(b) }
