// SPDX-License-Identifier: GPL-3.0-or-later

package ffmpeg

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fake is a script standing in for the binary.
var fake = []byte("#!/bin/sh\necho 'ffmpeg version test'\n")

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestExtractOrderAndReuse(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	env := map[string]string{"RUNTIME_DIRECTORY": blocked + "/sub", "CACHE_DIRECTORY": cache + ":/other"}
	getenv := func(k string) string { return env[k] }

	// the runtime directory cannot be created under a file, so the cache directory is used
	p, err := extract(fake, sum(fake), getenv)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(p) != cache || !strings.HasPrefix(filepath.Base(p), "ffmpeg-") {
		t.Fatalf("extracted to %s", p)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o500 {
		t.Fatalf("mode %v", st.Mode())
	}
	mtime := st.ModTime()
	if p2, err := extract(fake, sum(fake), getenv); err != nil || p2 != p {
		t.Fatalf("second extract: %s %v", p2, err)
	}
	if st, _ := os.Stat(p); !st.ModTime().Equal(mtime) {
		t.Fatal("an intact copy was rewritten")
	}

	// a damaged copy is replaced
	_ = os.Chmod(p, 0o700)
	_ = os.WriteFile(p, []byte("broken"), 0o700)
	if _, err := extract(fake, sum(fake), getenv); err != nil || !intact(p, sum(fake)) {
		t.Fatalf("damaged copy not replaced: %v", err)
	}
}

func TestExtractRejectsWrongSum(t *testing.T) {
	if _, err := extract(fake, sum([]byte("other")), func(string) string { return "" }); err == nil {
		t.Fatal("accepted a binary that does not match its sha256")
	}
	if _, err := extract(nil, "", func(string) string { return "" }); err == nil {
		t.Fatal("extracted nothing")
	}
}

func TestResolvePath(t *testing.T) {
	if Available() {
		t.Skip("built with the embedded ffmpeg")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ffmpeg"), fake, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if p, note := Resolve(""); p != filepath.Join(dir, "ffmpeg") || note != "from PATH" {
		t.Fatalf("%s %s", p, note)
	}
	if p, note := Resolve(Embedded); p == "" || !strings.Contains(note, "no embedded") {
		t.Fatalf("%s %s", p, note)
	}
	if p, _ := Resolve("/nonexistent/ffmpeg"); p != "" {
		t.Fatal(p)
	}
}
