// SPDX-License-Identifier: GPL-3.0-or-later

package dvrproto_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/andras-tim/dvr-squirrel/internal/dvrproto"
)

func TestLoginRequest(t *testing.T) {
	got := dvrproto.LoginRequest("admin", "")
	want := "\x00\x00\x01\xfe" + "002C" + "0081" + "21232F297A57A5A743894A0E4A801FC3" // MD5("admin")
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// user and password are concatenated without a separator
	if a, b := dvrproto.LoginRequest("ad", "min"), dvrproto.LoginRequest("admin", ""); string(a) != string(b) {
		t.Fatalf("digest of user+password differs: %q vs %q", a, b)
	}
}

func TestLogoutRequest(t *testing.T) {
	if got := string(dvrproto.LogoutRequest()); got != "\x00\x00\x01\xfe000C0086" {
		t.Fatalf("got %q", got)
	}
}

func replyBytes(status, arg string) []byte {
	b := append([]byte{0, 0, 1, 0xfe}, "0020"+status+arg...)
	return append(b, make([]byte, 16)...)
}

func TestParseLoginReply(t *testing.T) {
	tests := []struct {
		name    string
		in      []byte
		want    dvrproto.LoginReply
		wantErr bool
		code    int // expected LoginError code, 0 for success
	}{
		{name: "ok", in: replyBytes("0003", "0004"), want: dvrproto.LoginReply{Status: 3, Arg: 4}},
		{name: "wait", in: replyBytes("0001", "0000"), want: dvrproto.LoginReply{Status: 1}},
		{name: "too many", in: replyBytes("0002", "0001"), want: dvrproto.LoginReply{Status: 2, Arg: 1}, code: 1},
		{name: "password", in: replyBytes("0004", "0002"), want: dvrproto.LoginReply{Status: 4, Arg: 2}, code: 2},
		{name: "short", in: replyBytes("0003", "0004")[:31], wantErr: true},
		{name: "magic", in: append([]byte{1}, replyBytes("0003", "0004")[1:]...), wantErr: true},
		{name: "not hex", in: replyBytes("00X3", "0004"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := dvrproto.ParseLoginReply(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("no error for %q", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			if tt.code != 0 {
				var le *dvrproto.LoginError
				if !errors.As(got.Err(), &le) || le.Code != tt.code {
					t.Fatalf("Err() = %v, want LoginError code %d", got.Err(), tt.code)
				}
			}
		})
	}
}

func TestLoginReplyErr(t *testing.T) {
	if err := (dvrproto.LoginReply{Status: dvrproto.StatusOK, Arg: 4}).Err(); err != nil {
		t.Fatal(err)
	}
	err := (dvrproto.LoginReply{Status: 9}).Err()
	if !errors.Is(err, dvrproto.ErrUnexpectedStatus) {
		t.Fatalf("got %v", err)
	}
	msg := fmt.Sprint(&dvrproto.LoginError{Status: 2, Code: 2})
	if msg != "login rejected: incorrect password (status 2, code 2)" {
		t.Fatalf("got %q", msg)
	}
}
