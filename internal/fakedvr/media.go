// SPDX-License-Identifier: GPL-3.0-or-later

// Package fakedvr generates synthetic DVR frames and serves them like a DVR, for tests.
package fakedvr

import (
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"

	"github.com/andras-tim/dvr-squirrel/internal/dvrproto"
)

// Width and Height of the synthetic video.
const (
	Width  = 352
	Height = 288
)

var startCode = []byte{0, 0, 0, 1}

type bitWriter struct {
	buf  []byte
	nbit int
}

func (w *bitWriter) bit(b uint) {
	if w.nbit%8 == 0 {
		w.buf = append(w.buf, 0)
	}
	if b != 0 {
		w.buf[len(w.buf)-1] |= 0x80 >> (w.nbit % 8)
	}
	w.nbit++
}

func (w *bitWriter) bits(v uint, n int) {
	for i := n - 1; i >= 0; i-- {
		w.bit((v >> i) & 1)
	}
}

// ue writes an unsigned Exp-Golomb code.
func (w *bitWriter) ue(v uint) {
	v++
	n := 0
	for t := v; t > 1; t >>= 1 {
		n++
	}
	w.bits(0, n)
	w.bits(v, n+1)
}

func (w *bitWriter) trailing() []byte {
	w.bit(1)
	for w.nbit%8 != 0 {
		w.bit(0)
	}
	return w.buf
}

// SPS returns a Constrained Baseline SPS for Width x Height without VUI.
func SPS() []byte {
	w := &bitWriter{}
	w.bits(0x67, 8) // NAL header: SPS
	w.bits(66, 8)   // profile_idc: Baseline
	w.bits(0xc0, 8) // constraint_set0 + constraint_set1: Constrained Baseline
	w.bits(20, 8)   // level_idc 2.0
	w.ue(0)         // seq_parameter_set_id
	w.ue(0)         // log2_max_frame_num_minus4
	w.ue(2)         // pic_order_cnt_type
	w.ue(1)         // max_num_ref_frames
	w.bit(0)        // gaps_in_frame_num_value_allowed_flag
	w.ue(Width/16 - 1)
	w.ue(Height/16 - 1)
	w.bit(1) // frame_mbs_only_flag
	w.bit(1) // direct_8x8_inference_flag
	w.bit(0) // frame_cropping_flag
	w.bit(0) // vui_parameters_present_flag
	return w.trailing()
}

// PPS returns a PPS matching SPS.
func PPS() []byte {
	w := &bitWriter{}
	w.bits(0x68, 8) // NAL header: PPS
	w.ue(0)         // pic_parameter_set_id
	w.ue(0)         // seq_parameter_set_id
	w.bit(0)        // entropy_coding_mode_flag: CAVLC
	w.bit(0)        // bottom_field_pic_order_in_frame_present_flag
	w.ue(0)         // num_slice_groups_minus1
	w.ue(0)         // num_ref_idx_l0_default_active_minus1
	w.ue(0)         // num_ref_idx_l1_default_active_minus1
	w.bit(0)        // weighted_pred_flag
	w.bits(0, 2)    // weighted_bipred_idc
	w.ue(0)         // pic_init_qp_minus26 (se 0 == ue 0)
	w.ue(0)         // pic_init_qs_minus26
	w.ue(0)         // chroma_qp_index_offset
	w.bit(1)        // deblocking_filter_control_present_flag
	w.bit(0)        // constrained_intra_pred_flag
	w.bit(0)        // redundant_pic_cnt_present_flag
	return w.trailing()
}

// sei is a user_data_unregistered SEI with a zero UUID and no payload beyond it.
var sei = append([]byte{0x06, 0x05, 16}, append(make([]byte, 16), 0x80)...)

// slice returns a fake slice NALU of the given type, filled with a marker byte. It is not decodable,
// but it is a valid NALU for packetization.
func slice(naluHeader byte, size int, marker byte) []byte {
	b := make([]byte, size)
	b[0] = naluHeader
	for i := 1; i < size; i++ {
		b[i] = marker
	}
	return b
}

func annexB(nalus ...[]byte) []byte {
	var b []byte
	for _, n := range nalus {
		b = append(b, startCode...)
		b = append(b, n...)
	}
	return b
}

// IDRPayload is a DVR IDR payload: SPS, PPS, SEI and an IDR slice of sliceSize bytes.
func IDRPayload(sliceSize int, marker byte) []byte {
	return annexB(SPS(), PPS(), sei, slice(0x65, sliceSize, marker))
}

// PPayload is a DVR P frame payload: one non-IDR slice.
func PPayload(sliceSize int, marker byte) []byte {
	return annexB(slice(0x41, sliceSize, marker))
}

// ADTSPayload is one AAC-LC ADTS frame at 16 kHz, mono, with an auSize-byte dummy AU.
func ADTSPayload(auSize int) []byte {
	b, err := mpeg4audio.ADTSPackets{{
		Type:          mpeg4audio.ObjectTypeAACLC,
		SampleRate:    16000,
		ChannelConfig: 1,
		AU:            make([]byte, auSize),
	}}.Marshal()
	if err != nil {
		panic(err)
	}
	return b
}

// VideoFrame builds a video frame for the 0-based channel.
func VideoFrame(channel uint8, idr bool, micros uint32, payload []byte) *dvrproto.Frame {
	kind := dvrproto.KindPFrame
	if idr {
		kind = dvrproto.KindIDR
	}
	return &dvrproto.Frame{
		Header: dvrproto.Header{
			UnixTime: 1_800_000_000 + micros/1_000_000,
			Channel:  channel,
			Micros:   micros,
			Kind:     kind,
			Width:    Width,
			Height:   Height,
		},
		Payload: payload,
	}
}

// AudioFrame builds an audio frame.
func AudioFrame(micros uint32, payload []byte) *dvrproto.Frame {
	return &dvrproto.Frame{
		Header: dvrproto.Header{
			UnixTime: 1_800_000_000 + micros/1_000_000,
			Micros:   micros,
			Kind:     dvrproto.KindAudio,
			Width:    256,
			Height:   16000,
		},
		Payload: payload,
	}
}

// Sequence returns n video frames per channel for the given channels, interleaved like the DVR
// sends them, plus audio when withAudio is set. Every gop-th frame of a channel is an IDR, the
// first one included. Timestamps start at startMicros and advance about 93 ms per video frame.
func Sequence(channels []uint8, n, gop int, startMicros uint32, withAudio bool) []*dvrproto.Frame {
	const frameMicros = 93_333
	const audioMicros = 64_000 // 1024 samples at 16 kHz
	var out []*dvrproto.Frame
	nextAudio := startMicros
	for i := range n {
		ts := startMicros + uint32(i*frameMicros)
		for _, ch := range channels {
			idr := i%gop == 0
			var p []byte
			if idr {
				p = IDRPayload(1500+int(ch)*7, byte(i))
			} else {
				p = PPayload(300+int(ch)*5, byte(i))
			}
			out = append(out, VideoFrame(ch, idr, ts+uint32(ch)*1000, p))
		}
		for withAudio && int32(ts-nextAudio) >= 0 {
			out = append(out, AudioFrame(nextAudio, ADTSPayload(40)))
			nextAudio += audioMicros
		}
	}
	return out
}
