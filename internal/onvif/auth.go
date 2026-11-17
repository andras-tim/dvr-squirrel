// SPDX-License-Identifier: GPL-3.0-or-later

package onvif

import (
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"strings"
	"sync"
	"time"
)

// Accepted difference between the client's Created time and ours. Clients that read our clock
// with GetSystemDateAndTime stay well inside it.
const maxClockSkew = 5 * time.Minute

// authenticator checks WS-UsernameToken credentials and rejects replayed nonces.
type authenticator struct {
	user, password string
	now            func() time.Time

	mu     sync.Mutex
	nonces map[string]time.Time // seen nonce -> expiry
}

// check reports whether the token carries the configured credentials. The digest form is
// Base64(SHA1(nonce + created + password)); the plain text form is accepted too.
func (a *authenticator) check(t *usernameToken) bool {
	if t == nil || t.Username != a.user {
		return false
	}
	pw := strings.TrimSpace(t.Password.Value)
	if !strings.HasSuffix(t.Password.Type, "#PasswordDigest") {
		return subtle.ConstantTimeCompare([]byte(pw), []byte(a.password)) == 1
	}

	nonce, err := base64.StdEncoding.DecodeString(strings.TrimSpace(t.Nonce))
	if err != nil || len(nonce) == 0 {
		return false
	}
	created := strings.TrimSpace(t.Created)
	ct, err := time.Parse(time.RFC3339Nano, created)
	now := a.now()
	if err != nil || ct.Sub(now).Abs() > maxClockSkew {
		return false
	}
	h := sha1.New()
	h.Write(nonce)
	h.Write([]byte(created))
	h.Write([]byte(a.password))
	want := base64.StdEncoding.EncodeToString(h.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(pw), []byte(want)) != 1 {
		return false
	}
	// Keyed with Created too: a client reusing a nonce in a new token is not a replay.
	return a.fresh(string(nonce)+"|"+created, now)
}

// fresh records the nonce and reports whether it was unused within the skew window.
func (a *authenticator) fresh(nonce string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for n, exp := range a.nonces {
		if now.After(exp) {
			delete(a.nonces, n)
		}
	}
	if _, seen := a.nonces[nonce]; seen {
		return false
	}
	if a.nonces == nil {
		a.nonces = map[string]time.Time{}
	}
	a.nonces[nonce] = now.Add(2 * maxClockSkew)
	return true
}
