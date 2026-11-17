// SPDX-License-Identifier: GPL-3.0-or-later

package dvrproto_test

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"

	"github.com/andras-tim/dvr-squirrel/internal/dvrproto"
)

// TestFixture replays captured DVR->client streams named by DVR_FIXTURE (a path list). A capture
// may start with the login reply or directly at a frame boundary. Captures stay out of the repo.
func TestFixture(t *testing.T) {
	list := os.Getenv("DVR_FIXTURE")
	if list == "" {
		t.Skip("DVR_FIXTURE not set")
	}
	for _, path := range filepath.SplitList(list) {
		t.Run(filepath.Base(path), func(t *testing.T) { checkFixture(t, path) })
	}
}

func checkFixture(t *testing.T, path string) {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	br := bufio.NewReader(f)
	if head, _ := br.Peek(4); bytes.Equal(head, dvrproto.Magic) {
		var b [dvrproto.LoginReplySize]byte
		if _, err := io.ReadFull(br, b[:]); err != nil {
			t.Fatal(err)
		}
		rep, err := dvrproto.ParseLoginReply(b[:])
		if err != nil || rep.Err() != nil {
			t.Fatalf("login reply %+v: %v %v", rep, err, rep.Err())
		}
		t.Logf("login reply: channels=%d", rep.Arg)
	}

	type chanStats struct {
		idr, p     int
		lastMicros uint32
		seen       bool
		maxDelta   uint32
	}
	var video [16]chanStats
	audio := 0
	r := dvrproto.NewReader(br)
	for {
		fr, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("after %d audio frames: %v", audio, err)
		}
		if !fr.IsVideo() {
			var pkts mpeg4audio.ADTSPackets
			if err := pkts.Unmarshal(fr.Payload); err != nil {
				t.Fatalf("audio frame: %v", err)
			}
			if pkts[0].SampleRate != fr.SampleRate() {
				t.Fatalf("ADTS rate %d, header rate %d", pkts[0].SampleRate, fr.SampleRate())
			}
			audio++
			continue
		}
		cs := &video[fr.Channel]
		var au h264.AnnexB
		if err := au.Unmarshal(fr.Payload); err != nil {
			t.Fatalf("channel %d: %v", fr.Channel, err)
		}
		if fr.Kind == dvrproto.KindIDR {
			cs.idr++
			if h264.NALUType(au[0][0]&0x1f) != h264.NALUTypeSPS {
				t.Fatalf("channel %d: IDR starts with %v", fr.Channel, h264.NALUType(au[0][0]&0x1f))
			}
			var sps h264.SPS
			if err := sps.Unmarshal(au[0]); err != nil {
				t.Fatalf("channel %d: SPS: %v", fr.Channel, err)
			}
			if sps.Width() != int(fr.Width) || sps.Height() != int(fr.Height) {
				t.Fatalf("channel %d: SPS %dx%d, header %dx%d", fr.Channel, sps.Width(), sps.Height(), fr.Width, fr.Height)
			}
		} else {
			cs.p++
		}
		if cs.seen {
			// unsigned difference handles the 2^32 wrap
			d := fr.Micros - cs.lastMicros
			if int32(d) <= 0 {
				t.Fatalf("channel %d: timestamp went from %d to %d", fr.Channel, cs.lastMicros, fr.Micros)
			}
			cs.maxDelta = max(cs.maxDelta, d)
		}
		cs.lastMicros, cs.seen = fr.Micros, true
	}
	for ch, cs := range video {
		if cs.seen {
			t.Logf("channel %d: %d IDR, %d P, max frame gap %d us", ch, cs.idr, cs.p, cs.maxDelta)
		}
	}
	t.Logf("audio: %d frames", audio)
}
