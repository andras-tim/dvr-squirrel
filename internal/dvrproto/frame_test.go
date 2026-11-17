// SPDX-License-Identifier: GPL-3.0-or-later

package dvrproto_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/andras-tim/dvr-squirrel/internal/dvrproto"
	"github.com/andras-tim/dvr-squirrel/internal/fakedvr"
)

func TestFrameRoundTrip(t *testing.T) {
	frames := fakedvr.Sequence([]uint8{0, 1, 2, 3}, 20, 10, 4_294_000_000, true)
	var stream bytes.Buffer
	for _, f := range frames {
		b := f.Marshal()
		if len(b)%4 != 0 {
			t.Fatalf("frame of %d bytes is not padded", len(b))
		}
		stream.Write(b)
	}

	r := dvrproto.NewReader(&stream)
	for i, want := range frames {
		got, err := r.Next()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if got.Kind != want.Kind || got.Channel != want.Channel || got.Micros != want.Micros {
			t.Fatalf("frame %d: header %+v, want %+v", i, got.Header, want.Header)
		}
		if !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("frame %d: payload differs", i)
		}
	}
	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("after the last frame: %v, want EOF", err)
	}
}

func TestParseHeaderRejects(t *testing.T) {
	good := fakedvr.VideoFrame(2, true, 1000, fakedvr.IDRPayload(100, 1)).Marshal()[:dvrproto.HeaderSize]
	if _, err := dvrproto.ParseHeader(good); err != nil {
		t.Fatalf("good header: %v", err)
	}
	mutate := func(f func(b []byte)) []byte {
		b := bytes.Clone(good)
		f(b)
		return b
	}
	tests := map[string][]byte{
		"short":          good[:31],
		"frame length":   mutate(func(b []byte) { b[0] += 4 }),
		"zero payload":   mutate(func(b []byte) { b[4], b[5], b[6], b[7] = 0, 0, 0, 0 }),
		"huge payload":   mutate(func(b []byte) { b[7] = 0x40 }),
		"unknown kind":   mutate(func(b []byte) { b[24] = 7 }),
		"video marker":   mutate(func(b []byte) { b[12] = 0 }),
		"control magic":  append(append([]byte{0, 0, 1, 0xfe}, "00200003"...), make([]byte, 24)...),
		"audio w/ video": mutate(func(b []byte) { b[24] = byte(dvrproto.KindAudio) }),
	}
	for name, b := range tests {
		if _, err := dvrproto.ParseHeader(b); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestReaderTruncated(t *testing.T) {
	b := fakedvr.VideoFrame(0, false, 0, fakedvr.PPayload(50, 1)).Marshal()
	_, err := dvrproto.NewReader(bytes.NewReader(b[:len(b)-3])).Next()
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("got %v, want unexpected EOF", err)
	}
}

func TestReadLoginReplyThenFrames(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(replyBytes("0003", "0004"))
	stream.Write(fakedvr.AudioFrame(5, fakedvr.ADTSPayload(10)).Marshal())
	r := dvrproto.NewReader(&stream)
	rep, err := r.ReadLoginReply()
	if err != nil || rep.Status != dvrproto.StatusOK || rep.Arg != 4 {
		t.Fatalf("reply %+v, %v", rep, err)
	}
	f, err := r.Next()
	if err != nil || f.Kind != dvrproto.KindAudio || f.SampleRate() != 16000 {
		t.Fatalf("frame %+v, %v", f, err)
	}
}
