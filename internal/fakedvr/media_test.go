// SPDX-License-Identifier: GPL-3.0-or-later

package fakedvr

import (
	"testing"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
)

func TestSPS(t *testing.T) {
	var sps h264.SPS
	if err := sps.Unmarshal(SPS()); err != nil {
		t.Fatal(err)
	}
	if sps.Width() != Width || sps.Height() != Height || sps.ProfileIdc != 66 {
		t.Fatalf("got %dx%d profile %d", sps.Width(), sps.Height(), sps.ProfileIdc)
	}
}

func TestIDRPayload(t *testing.T) {
	var au h264.AnnexB
	if err := au.Unmarshal(IDRPayload(100, 7)); err != nil {
		t.Fatal(err)
	}
	want := []h264.NALUType{h264.NALUTypeSPS, h264.NALUTypePPS, h264.NALUTypeSEI, h264.NALUTypeIDR}
	if len(au) != len(want) {
		t.Fatalf("%d NALUs", len(au))
	}
	for i, n := range au {
		if got := h264.NALUType(n[0] & 0x1f); got != want[i] {
			t.Fatalf("NALU %d: %v, want %v", i, got, want[i])
		}
	}
}

func TestADTSPayload(t *testing.T) {
	var pkts mpeg4audio.ADTSPackets
	if err := pkts.Unmarshal(ADTSPayload(20)); err != nil {
		t.Fatal(err)
	}
	if len(pkts) != 1 || pkts[0].SampleRate != 16000 || len(pkts[0].AU) != 20 {
		t.Fatalf("got %+v", pkts[0])
	}
}
