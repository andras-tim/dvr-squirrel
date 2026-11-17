// SPDX-License-Identifier: GPL-3.0-or-later

// Package h264pcm encodes a still picture as an H.264 Constrained Baseline IDR access unit made of
// I_PCM macroblocks: uncompressed samples, so the encoder is a few hundred lines of pure Go.
package h264pcm

import (
	"image"
	"image/draw"
)

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

func (w *bitWriter) align() {
	for w.nbit%8 != 0 {
		w.bit(0)
	}
}

// bytes appends whole bytes; the writer must be byte aligned.
func (w *bitWriter) bytes(b []byte) {
	w.buf = append(w.buf, b...)
	w.nbit += 8 * len(b)
}

func (w *bitWriter) trailing() []byte {
	w.bit(1)
	w.align()
	return w.buf
}

// levels maps the frame size in macroblocks to the lowest level_idc whose MaxFS allows it.
var levels = []struct {
	maxFS uint
	idc   uint
}{{99, 10}, {396, 20}, {792, 21}, {1620, 30}, {3600, 31}, {5120, 32}, {8192, 40}, {22080, 50}, {36864, 51}}

func level(mbs uint) uint {
	for _, l := range levels {
		if mbs <= l.maxFS {
			return l.idc
		}
	}
	return 52
}

func sps(w, h int) []byte {
	mbW, mbH := uint(w+15)/16, uint(h+15)/16
	b := &bitWriter{}
	b.bits(0x67, 8) // nal_ref_idc 3, SPS
	b.bits(66, 8)   // profile_idc: Baseline
	b.bits(0xc0, 8) // constraint_set0 + constraint_set1: Constrained Baseline
	b.bits(level(mbW*mbH), 8)
	b.ue(0) // seq_parameter_set_id
	b.ue(0) // log2_max_frame_num_minus4
	b.ue(2) // pic_order_cnt_type: no POC syntax in the slice header
	b.ue(0) // max_num_ref_frames
	b.bit(0)
	b.ue(mbW - 1)
	b.ue(mbH - 1)
	b.bit(1)                                               // frame_mbs_only_flag
	b.bit(1)                                               // direct_8x8_inference_flag
	cropR, cropB := (mbW*16-uint(w))/2, (mbH*16-uint(h))/2 // 4:2:0 crop units are 2 samples
	if cropR != 0 || cropB != 0 {
		b.bit(1)
		b.ue(0)
		b.ue(cropR)
		b.ue(0)
		b.ue(cropB)
	} else {
		b.bit(0)
	}
	b.bit(0) // vui_parameters_present_flag
	return b.trailing()
}

func pps() []byte {
	b := &bitWriter{}
	b.bits(0x68, 8) // nal_ref_idc 3, PPS
	b.ue(0)         // pic_parameter_set_id
	b.ue(0)         // seq_parameter_set_id
	b.bit(0)        // entropy_coding_mode_flag: CAVLC
	b.bit(0)        // bottom_field_pic_order_in_frame_present_flag
	b.ue(0)         // num_slice_groups_minus1
	b.ue(0)         // num_ref_idx_l0_default_active_minus1
	b.ue(0)         // num_ref_idx_l1_default_active_minus1
	b.bit(0)        // weighted_pred_flag
	b.bits(0, 2)    // weighted_bipred_idc
	b.ue(0)         // pic_init_qp_minus26
	b.ue(0)         // pic_init_qs_minus26
	b.ue(0)         // chroma_qp_index_offset
	b.bit(1)        // deblocking_filter_control_present_flag
	b.bit(0)        // constrained_intra_pred_flag
	b.bit(0)        // redundant_pic_cnt_present_flag
	return b.trailing()
}

// Planes are 4:2:0 limited-range YCbCr samples padded to whole macroblocks.
type planes struct {
	mbW, mbH  int
	y, cb, cr []byte
}

func clampSample(v int) byte {
	// Early H.264 editions forbid 0 in PCM samples; limited range never needs it.
	return byte(min(max(v, 1), 255))
}

// toPlanes converts with BT.601 limited-range coefficients; the edge pixels fill the padding.
func toPlanes(img image.Image) planes {
	bw, bh := img.Bounds().Dx(), img.Bounds().Dy()
	p := planes{mbW: (bw + 15) / 16, mbH: (bh + 15) / 16}
	w, h := p.mbW*16, p.mbH*16
	rgba := image.NewRGBA(image.Rect(0, 0, bw, bh))
	draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
	at := func(x, y int) (int, int, int) {
		o := rgba.PixOffset(min(x, bw-1), min(y, bh-1))
		return int(rgba.Pix[o]), int(rgba.Pix[o+1]), int(rgba.Pix[o+2])
	}
	p.y = make([]byte, w*h)
	p.cb = make([]byte, w*h/4)
	p.cr = make([]byte, w*h/4)
	for y := range h {
		for x := range w {
			r, g, b := at(x, y)
			p.y[y*w+x] = clampSample(((66*r + 129*g + 25*b + 128) >> 8) + 16)
		}
	}
	for y := 0; y < h; y += 2 {
		for x := 0; x < w; x += 2 {
			var r, g, b int
			for _, d := range [4][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
				rr, gg, bb := at(x+d[0], y+d[1])
				r, g, b = r+rr, g+gg, b+bb
			}
			r, g, b = r/4, g/4, b/4
			i := (y/2)*(w/2) + x/2
			p.cb[i] = clampSample(((-38*r - 74*g + 112*b + 128) >> 8) + 128)
			p.cr[i] = clampSample(((112*r - 94*g - 18*b + 128) >> 8) + 128)
		}
	}
	return p
}

func slice(p planes, idrPicID uint) []byte {
	w := p.mbW * 16
	b := &bitWriter{buf: make([]byte, 0, p.mbW*p.mbH*386+16)}
	b.bits(0x65, 8) // nal_ref_idc 3, IDR slice
	b.ue(0)         // first_mb_in_slice
	b.ue(7)         // slice_type: I, all slices
	b.ue(0)         // pic_parameter_set_id
	b.bits(0, 4)    // frame_num
	b.ue(idrPicID)  // idr_pic_id
	b.bit(0)        // no_output_of_prior_pics_flag
	b.bit(0)        // long_term_reference_flag
	b.ue(0)         // slice_qp_delta (se 0)
	b.ue(1)         // disable_deblocking_filter_idc: off
	mb := make([]byte, 384)
	for my := range p.mbH {
		for mx := range p.mbW {
			b.ue(25) // mb_type: I_PCM
			b.align()
			for row := range 16 {
				o := (my*16+row)*w + mx*16
				copy(mb[row*16:], p.y[o:o+16])
			}
			for row := range 8 {
				o := (my*8+row)*(w/2) + mx*8
				copy(mb[256+row*8:], p.cb[o:o+8])
				copy(mb[320+row*8:], p.cr[o:o+8])
			}
			b.bytes(mb)
		}
	}
	return b.trailing()
}

// escape inserts emulation prevention bytes, turning an RBSP into NAL unit payload.
func escape(rbsp []byte) []byte {
	out := make([]byte, 0, len(rbsp)+len(rbsp)/64)
	zeros := 0
	for _, c := range rbsp {
		if zeros >= 2 && c <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// Encode returns an Annex-B access unit with SPS, PPS and an IDR slice of img. Consecutive IDR
// pictures must differ in idrPicID, so callers alternate it. Odd sizes are padded and cropped.
func Encode(img image.Image, idrPicID uint) []byte {
	// Cropping works in 2-sample units, so an odd last row or column is dropped.
	w, h := max(img.Bounds().Dx()&^1, 2), max(img.Bounds().Dy()&^1, 2)
	img = sub(img, w, h)
	start := []byte{0, 0, 0, 1}
	var au []byte
	for _, n := range [][]byte{sps(w, h), pps(), slice(toPlanes(img), idrPicID)} {
		au = append(au, start...)
		au = append(au, escape(n)...)
	}
	return au
}

// sub limits img to w x h from its origin, extending it by its edge when smaller.
func sub(img image.Image, w, h int) image.Image {
	b := img.Bounds()
	if b.Dx() == w && b.Dy() == h {
		return img
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	return out
}
