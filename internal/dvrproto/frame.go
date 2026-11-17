// SPDX-License-Identifier: GPL-3.0-or-later

package dvrproto

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

// HeaderSize is the size of a media frame header.
const HeaderSize = 32

// MaxPayload bounds a payload; larger lengths mean a desynchronized stream. A CIF IDR frame is
// below 64 KiB, so this leaves room for higher resolutions.
const MaxPayload = 4 << 20

// Kind is the frame kind at header offset 24.
type Kind uint8

// Frame kinds.
const (
	KindPFrame Kind = 0
	KindAudio  Kind = 1
	KindIDR    Kind = 2
)

func (k Kind) String() string {
	switch k {
	case KindPFrame:
		return "P"
	case KindAudio:
		return "audio"
	case KindIDR:
		return "IDR"
	}
	return fmt.Sprintf("kind(%d)", uint8(k))
}

// Media type markers at header offsets 12 and 14.
const (
	videoMarker    = 0x0e
	videoSubMarker = 0x0f
)

// Header is a parsed media frame header.
type Header struct {
	FrameLen   uint32 // header + padded payload
	PayloadLen uint32
	UnixTime   uint32 // DVR clock, seconds
	Channel    uint8  // 0-based, video only
	Micros     uint32 // microsecond timestamp, wraps at 2^32
	Kind       Kind
	Width      uint16 // video only
	Height     uint16 // video: height; audio: sample rate
}

// IsVideo reports whether the frame carries H.264.
func (h Header) IsVideo() bool { return h.Kind == KindPFrame || h.Kind == KindIDR }

// SampleRate returns the audio sample rate (audio frames only).
func (h Header) SampleRate() int { return int(h.Height) }

// ParseHeader parses and sanity-checks a 32-byte frame header.
func ParseHeader(b []byte) (Header, error) {
	if len(b) < HeaderSize {
		return Header{}, fmt.Errorf("frame header: %d bytes, want %d", len(b), HeaderSize)
	}
	le := binary.LittleEndian
	h := Header{
		FrameLen:   le.Uint32(b[0:]),
		PayloadLen: le.Uint32(b[4:]),
		UnixTime:   le.Uint32(b[8:]),
		Channel:    b[13],
		Micros:     le.Uint32(b[16:]),
		Kind:       Kind(b[24]),
		Width:      le.Uint16(b[26:]),
		Height:     le.Uint16(b[28:]),
	}
	if h.PayloadLen == 0 || h.PayloadLen > MaxPayload {
		return h, fmt.Errorf("frame header: payload length %d out of range", h.PayloadLen)
	}
	if want := HeaderSize + pad4(h.PayloadLen); h.FrameLen != want {
		return h, fmt.Errorf("frame header: frame length %d, want %d for payload %d", h.FrameLen, want, h.PayloadLen)
	}
	switch h.Kind {
	case KindPFrame, KindIDR:
		if b[12] != videoMarker || b[14] != videoSubMarker {
			return h, fmt.Errorf("frame header: video kind with markers %#02x/%#02x", b[12], b[14])
		}
	case KindAudio:
		if b[12] != 0 {
			return h, fmt.Errorf("frame header: audio kind with marker %#02x", b[12])
		}
	default:
		return h, fmt.Errorf("frame header: unknown kind %d", b[24])
	}
	return h, nil
}

// MarshalHeader encodes h; the unknown fields get the values seen on the device.
func MarshalHeader(h Header) []byte {
	b := make([]byte, HeaderSize)
	le := binary.LittleEndian
	le.PutUint32(b[0:], h.FrameLen)
	le.PutUint32(b[4:], h.PayloadLen)
	le.PutUint32(b[8:], h.UnixTime)
	if h.IsVideo() {
		b[12], b[13], b[14] = videoMarker, h.Channel, videoSubMarker
	}
	le.PutUint32(b[16:], h.Micros)
	le.PutUint32(b[20:], 10003)
	b[24] = byte(h.Kind)
	b[25] = 4
	le.PutUint16(b[26:], h.Width)
	le.PutUint16(b[28:], h.Height)
	return b
}

func pad4(n uint32) uint32 { return (n + 3) &^ 3 }

// Frame is one media frame.
type Frame struct {
	Header
	Payload []byte // unpadded
}

// Marshal encodes the frame with its header and padding; FrameLen and PayloadLen are derived.
func (f *Frame) Marshal() []byte {
	h := f.Header
	h.PayloadLen = uint32(len(f.Payload))
	h.FrameLen = HeaderSize + pad4(h.PayloadLen)
	b := make([]byte, h.FrameLen)
	copy(b, MarshalHeader(h))
	copy(b[HeaderSize:], f.Payload)
	return b
}

// Reader reads media frames from a DVR byte stream positioned at a frame boundary.
type Reader struct {
	r   *bufio.Reader
	hdr [HeaderSize]byte
}

// NewReader returns a Reader on r.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReaderSize(r, 64<<10)}
}

// Next returns the next frame. A header failing the sanity checks is an error: the stream has no
// resync marker, so the caller should reconnect. The payload is freshly allocated per frame.
func (r *Reader) Next() (*Frame, error) {
	if _, err := io.ReadFull(r.r, r.hdr[:]); err != nil {
		return nil, err
	}
	h, err := ParseHeader(r.hdr[:])
	if err != nil {
		return nil, err
	}
	buf := make([]byte, h.FrameLen-HeaderSize)
	if _, err := io.ReadFull(r.r, buf); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return &Frame{Header: h, Payload: buf[:h.PayloadLen:h.PayloadLen]}, nil
}

// ReadLoginReply reads one 32-byte login reply; it shares the buffer with Next, since the DVR sends
// the first frames right behind the reply.
func (r *Reader) ReadLoginReply() (LoginReply, error) {
	var b [LoginReplySize]byte
	if _, err := io.ReadFull(r.r, b[:]); err != nil {
		return LoginReply{}, err
	}
	return ParseLoginReply(b[:])
}
