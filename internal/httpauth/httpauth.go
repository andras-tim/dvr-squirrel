// SPDX-License-Identifier: GPL-3.0-or-later

// Package httpauth protects an HTTP handler with Basic and Digest (RFC 7616, MD5) authentication.
package httpauth

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// nonceLifetime bounds how long a server nonce is accepted.
const nonceLifetime = 5 * time.Minute

// Handler requires User and Password on every request to Next.
type Handler struct {
	User, Password, Realm string
	Next                  http.Handler
	// Now is the clock, replaceable in tests.
	Now func() time.Time

	key []byte
}

// New returns a Handler with a random nonce key.
func New(user, password, realm string, next http.Handler) *Handler {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return &Handler{User: user, Password: password, Realm: realm, Next: next, key: key}
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.authorized(r) {
		h.Next.ServeHTTP(w, r)
		return
	}
	w.Header().Add("WWW-Authenticate", fmt.Sprintf(`Digest realm=%q, qop="auth", algorithm=MD5, nonce=%q`, h.Realm, h.nonce()))
	w.Header().Add("WWW-Authenticate", fmt.Sprintf(`Basic realm=%q`, h.Realm))
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func (h *Handler) authorized(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	scheme, rest, _ := strings.Cut(auth, " ")
	switch strings.ToLower(scheme) {
	case "basic":
		u, p, ok := r.BasicAuth()
		return ok && eq(u, h.User) && eq(p, h.Password)
	case "digest":
		return h.digestOK(r.Method, parseParams(rest))
	}
	return false
}

func eq(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// nonce is a timestamp and its HMAC, so no nonce store is needed; nc reuse within the lifetime is not tracked.
func (h *Handler) nonce() string {
	b := make([]byte, 8, 8+sha256.Size)
	binary.BigEndian.PutUint64(b, uint64(h.now().Unix()))
	m := hmac.New(sha256.New, h.key)
	m.Write(b)
	return base64.RawURLEncoding.EncodeToString(m.Sum(b))
}

func (h *Handler) nonceFresh(n string) bool {
	b, err := base64.RawURLEncoding.DecodeString(n)
	if err != nil || len(b) != 8+sha256.Size {
		return false
	}
	m := hmac.New(sha256.New, h.key)
	m.Write(b[:8])
	if !hmac.Equal(m.Sum(nil), b[8:]) {
		return false
	}
	age := h.now().Sub(time.Unix(int64(binary.BigEndian.Uint64(b[:8])), 0))
	return age >= 0 && age <= nonceLifetime
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (h *Handler) digestOK(method string, p map[string]string) bool {
	if !eq(p["username"], h.User) || p["realm"] != h.Realm || !h.nonceFresh(p["nonce"]) {
		return false
	}
	if alg := p["algorithm"]; alg != "" && !strings.EqualFold(alg, "MD5") {
		return false
	}
	ha1 := md5hex(h.User + ":" + h.Realm + ":" + h.Password)
	ha2 := md5hex(method + ":" + p["uri"])
	var want string
	switch p["qop"] {
	case "auth":
		want = md5hex(ha1 + ":" + p["nonce"] + ":" + p["nc"] + ":" + p["cnonce"] + ":auth:" + ha2)
	case "":
		want = md5hex(ha1 + ":" + p["nonce"] + ":" + ha2)
	default:
		return false
	}
	return eq(strings.ToLower(p["response"]), want)
}

// parseParams parses the comma-separated key=value list of a Digest header; values may be quoted.
func parseParams(s string) map[string]string {
	out := map[string]string{}
	for s != "" {
		s = strings.TrimLeft(s, " ,")
		k, rest, ok := strings.Cut(s, "=")
		if !ok {
			break
		}
		k = strings.ToLower(strings.TrimSpace(k))
		var v string
		if strings.HasPrefix(rest, `"`) {
			end := strings.IndexByte(rest[1:], '"')
			if end < 0 {
				break
			}
			v, s = rest[1:end+1], rest[end+2:]
		} else {
			v, s, _ = strings.Cut(rest, ",")
			v = strings.TrimSpace(v)
		}
		out[k] = v
	}
	return out
}
