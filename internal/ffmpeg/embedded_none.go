// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !(embedffmpeg && linux && amd64)

package ffmpeg

// Built without the embedded FFmpeg (plain `go build` or `go install`): the PATH one is used, if any.
var (
	embedded    []byte
	embeddedSum string
)
