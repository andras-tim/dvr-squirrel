// SPDX-License-Identifier: GPL-3.0-or-later

package httpauth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newHandler() *Handler {
	return New("viewer", "pw", "cam", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
}

func do(h http.Handler, auth string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/snapshot.jpg", nil)
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func challengeNonce(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	for _, v := range w.Result().Header.Values("WWW-Authenticate") {
		if p := parseParams(strings.TrimPrefix(v, "Digest ")); strings.HasPrefix(v, "Digest ") {
			return p["nonce"]
		}
	}
	t.Fatal("no digest challenge")
	return ""
}

func digest(user, pass, realm, nonce, uri string, qop bool) string {
	ha1 := md5hex(user + ":" + realm + ":" + pass)
	ha2 := md5hex("GET:" + uri)
	if !qop {
		return fmt.Sprintf(`Digest username=%q, realm=%q, nonce=%q, uri=%q, response=%q`,
			user, realm, nonce, uri, md5hex(ha1+":"+nonce+":"+ha2))
	}
	resp := md5hex(ha1 + ":" + nonce + ":00000001:abcdef:auth:" + ha2)
	return fmt.Sprintf(`Digest username=%q, realm=%q, nonce=%q, uri=%q, algorithm=MD5, response=%q, qop=auth, nc=00000001, cnonce="abcdef"`,
		user, realm, nonce, uri, resp)
}

func TestBasicAndDigest(t *testing.T) {
	h := newHandler()
	w := do(h, "")
	if w.Code != 401 || len(w.Result().Header.Values("WWW-Authenticate")) != 2 {
		t.Fatalf("no auth: %d %v", w.Code, w.Result().Header)
	}
	nonce := challengeNonce(t, w)

	tests := []struct {
		name, auth string
		want       int
	}{
		{"basic", "Basic dmlld2VyOnB3", 200}, // viewer:pw
		{"basic wrong", "Basic dmlld2VyOnB4", 401},
		{"digest qop", digest("viewer", "pw", "cam", nonce, "/snapshot.jpg", true), 200},
		{"digest no qop", digest("viewer", "pw", "cam", nonce, "/snapshot.jpg", false), 200},
		{"digest wrong password", digest("viewer", "px", "cam", nonce, "/snapshot.jpg", true), 401},
		{"digest wrong realm", digest("viewer", "pw", "other", nonce, "/snapshot.jpg", true), 401},
		{"digest forged nonce", digest("viewer", "pw", "cam", forge(nonce), "/snapshot.jpg", true), 401},
	}
	for _, tt := range tests {
		if got := do(h, tt.auth).Code; got != tt.want {
			t.Errorf("%s: %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestStaleNonce(t *testing.T) {
	h := newHandler()
	start := time.Unix(1_800_000_000, 0)
	h.Now = func() time.Time { return start }
	nonce := challengeNonce(t, do(h, ""))
	h.Now = func() time.Time { return start.Add(nonceLifetime + time.Second) }
	if got := do(h, digest("viewer", "pw", "cam", nonce, "/snapshot.jpg", true)).Code; got != 401 {
		t.Fatalf("stale nonce: %d", got)
	}
}

func TestParseParams(t *testing.T) {
	p := parseParams(`username="a, b", realm="r", nc=00000001, qop=auth`)
	if p["username"] != "a, b" || p["realm"] != "r" || p["nc"] != "00000001" || p["qop"] != "auth" {
		t.Fatalf("%v", p)
	}
}

// forge changes one character of the HMAC part of a nonce.
func forge(n string) string {
	c := byte('A')
	if n[20] == 'A' {
		c = 'B'
	}
	return n[:20] + string(c) + n[21:]
}
