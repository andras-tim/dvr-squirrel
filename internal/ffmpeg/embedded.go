// SPDX-License-Identifier: GPL-3.0-or-later

//go:build embedffmpeg && linux && amd64

package ffmpeg

import _ "embed"

// The minimal LGPL FFmpeg built by `make ffmpeg` (build/ffmpeg), and its sha256.
var (
	//go:embed bin/ffmpeg-linux-amd64
	embedded []byte
	//go:embed bin/ffmpeg-linux-amd64.sha256
	embeddedSum string
)
