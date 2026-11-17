// SPDX-License-Identifier: GPL-3.0-or-later

// Package dvrproto implements the proprietary TCP protocol of HiSilicon-based DVRs:
// login, logout and the multiplexed media frame stream.
package dvrproto

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Magic starts every control message (login, login reply, logout).
var Magic = []byte{0x00, 0x00, 0x01, 0xFE}

const (
	loginRequestSize = 44
	logoutSize       = 12
	// LoginReplySize is the size of a login reply.
	LoginReplySize = 32

	cmdLogin  = "0081"
	cmdLogout = "0086"
)

// Login reply status values.
const (
	StatusWait   = 1
	StatusError  = 2
	StatusOK     = 3
	StatusError2 = 4
)

// LoginRequest returns the 44-byte login message. The digest in it is replayable, so the
// result is as sensitive as the password.
func LoginRequest(user, password string) []byte {
	sum := md5.Sum([]byte(user + password))
	b := make([]byte, 0, loginRequestSize)
	b = append(b, Magic...)
	b = fmt.Appendf(b, "%04X", loginRequestSize)
	b = append(b, cmdLogin...)
	b = append(b, strings.ToUpper(hex.EncodeToString(sum[:]))...)
	return b
}

// LogoutRequest returns the 12-byte logout message.
func LogoutRequest() []byte {
	b := make([]byte, 0, logoutSize)
	b = append(b, Magic...)
	b = fmt.Appendf(b, "%04X", logoutSize)
	return append(b, cmdLogout...)
}

// LoginReply is a parsed login reply.
type LoginReply struct {
	Status int
	// Arg is the channel count on StatusOK and the error code on an error status.
	Arg int
}

// ParseLoginReply parses a 32-byte login reply.
func ParseLoginReply(b []byte) (LoginReply, error) {
	if len(b) != LoginReplySize {
		return LoginReply{}, fmt.Errorf("login reply: %d bytes, want %d", len(b), LoginReplySize)
	}
	if !bytes.Equal(b[0:4], Magic) {
		return LoginReply{}, fmt.Errorf("login reply: bad magic % x", b[0:4])
	}
	if string(b[4:8]) != fmt.Sprintf("%04X", LoginReplySize) {
		return LoginReply{}, fmt.Errorf("login reply: bad length field %q", b[4:8])
	}
	status, err := parseHex4(b[8:12])
	if err != nil {
		return LoginReply{}, fmt.Errorf("login reply: status: %w", err)
	}
	arg, err := parseHex4(b[12:16])
	if err != nil {
		return LoginReply{}, fmt.Errorf("login reply: argument: %w", err)
	}
	return LoginReply{Status: status, Arg: arg}, nil
}

func parseHex4(b []byte) (int, error) {
	v, err := strconv.ParseUint(string(b), 16, 16)
	if err != nil {
		return 0, fmt.Errorf("not hex: %q", b)
	}
	return int(v), nil
}

// LoginError is a login rejected by the DVR.
type LoginError struct {
	Status int
	Code   int
}

// Known login error codes.
const (
	CodeTooManyClients    = 1
	CodeIncorrectPassword = 2
	CodeBadNetwork        = 3
	CodeBadConnection     = 4
)

func (e *LoginError) Error() string {
	var msg string
	switch e.Code {
	case CodeTooManyClients:
		msg = "too many clients"
	case CodeIncorrectPassword:
		msg = "incorrect password"
	case CodeBadNetwork:
		msg = "bad network"
	case CodeBadConnection:
		msg = "bad connection"
	default:
		msg = "unknown error"
	}
	return fmt.Sprintf("login rejected: %s (status %d, code %d)", msg, e.Status, e.Code)
}

// ErrUnexpectedStatus is returned for a login reply status outside the known values.
var ErrUnexpectedStatus = errors.New("unexpected login status")

// Err returns nil for a successful login, a *LoginError for a rejection,
// and ErrUnexpectedStatus for an unknown status. StatusWait is not final: callers read on.
func (r LoginReply) Err() error {
	switch r.Status {
	case StatusOK:
		return nil
	case StatusError, StatusError2:
		return &LoginError{Status: r.Status, Code: r.Arg}
	default:
		return fmt.Errorf("%w %d", ErrUnexpectedStatus, r.Status)
	}
}
