// SPDX-License-Identifier: GPL-3.0-or-later

// Package ffmpeg provides the FFmpeg binary for the snapshots: the embedded minimal build (release binaries with
// the embedffmpeg build tag), extracted at startup into a private directory, else the one from PATH.
package ffmpeg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Embedded is the config value that selects the embedded build.
const Embedded = "embedded"

// Available reports whether this binary carries an embedded FFmpeg.
func Available() bool { return len(embedded) > 0 }

// Sum is the sha256 of the embedded FFmpeg, empty without one.
func Sum() string {
	f := strings.Fields(embeddedSum)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// dirs are the extraction candidates in order: the systemd directories, the user cache, a new temp directory.
func dirs(getenv func(string) string) []string {
	var out []string
	for _, env := range []string{"RUNTIME_DIRECTORY", "CACHE_DIRECTORY"} {
		// systemd may list several directories separated by ':'.
		if v := getenv(env); v != "" {
			out = append(out, strings.Split(v, ":")[0])
		}
	}
	if d, err := os.UserCacheDir(); err == nil {
		out = append(out, filepath.Join(d, "dvr-squirrel"))
	}
	return append(out, "") // "": a new private directory under os.TempDir
}

// Extract writes the embedded FFmpeg into the first usable private directory, verifies it and returns its path.
// A directory is unusable when it cannot be written or its files cannot be executed (noexec mounts).
func Extract() (string, error) {
	return extract(embedded, Sum(), os.Getenv)
}

func extract(data []byte, sum string, getenv func(string) string) (string, error) {
	if len(data) == 0 {
		return "", errors.New("this build has no embedded ffmpeg")
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != sum {
		return "", fmt.Errorf("embedded ffmpeg: sha256 %x does not match the recorded %s", got, sum)
	}
	var errs []error
	for _, dir := range dirs(getenv) {
		p, err := place(dir, data, sum)
		if err == nil {
			return p, nil
		}
		errs = append(errs, err)
	}
	return "", errors.Join(errs...)
}

// place writes the binary as dir/ffmpeg-<sum prefix>, mode 0500, reusing an intact earlier copy.
func place(dir string, data []byte, sum string) (string, error) {
	var err error
	if dir == "" {
		if dir, err = os.MkdirTemp("", "dvr-squirrel-ffmpeg-"); err != nil {
			return "", err
		}
	} else if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, "ffmpeg-"+sum[:16])
	if !intact(p, sum) {
		tmp, err := os.CreateTemp(dir, ".ffmpeg-*")
		if err != nil {
			return "", err
		}
		_, werr := tmp.Write(data)
		cerr := tmp.Close()
		if err := errors.Join(werr, cerr, os.Chmod(tmp.Name(), 0o500)); err != nil {
			_ = os.Remove(tmp.Name())
			return "", err
		}
		if err := os.Rename(tmp.Name(), p); err != nil {
			_ = os.Remove(tmp.Name())
			return "", err
		}
		if !intact(p, sum) {
			return "", fmt.Errorf("%s: written copy does not match the sha256", p)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, p, "-hide_banner", "-version").Output()
	if err != nil || !bytes.HasPrefix(out, []byte("ffmpeg version")) {
		return "", fmt.Errorf("%s: cannot run it (noexec mount?): %v", p, err)
	}
	return p, nil
}

func intact(p, sum string) bool {
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	got := sha256.Sum256(b)
	return hex.EncodeToString(got[:]) == sum
}

// Resolve returns the FFmpeg to use for setting (from [snapshot] ffmpeg): "embedded" or "" with an embedded
// build extracts it; otherwise, or when that fails, setting (default "ffmpeg") is looked up in PATH. An empty
// result means no FFmpeg: snapshots are placeholders. note describes the choice for the log.
func Resolve(setting string) (path, note string) {
	if (setting == "" || setting == Embedded) && Available() {
		p, err := Extract()
		if err == nil {
			return p, "embedded"
		}
		note = fmt.Sprintf("embedded ffmpeg unusable (%v); ", err)
	} else if setting == Embedded {
		note = "this build has no embedded ffmpeg; "
	}
	name := setting
	if name == "" || name == Embedded {
		name = "ffmpeg"
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", note + "no " + name + " in PATH"
	}
	return p, note + "from PATH"
}
