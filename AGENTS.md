# DVR Squirrel - agent instructions

## Repo overview

A Go daemon that logs in to an old 4-channel HiSilicon-based DVR over its proprietary TCP protocol and republishes
each channel as an ONVIF Profile S camera with its own RTSP stream, for NVRs such as UniFi Protect.

| path | content |
|---|---|
| `cmd/dvr-squirrel/` | the daemon: config loading, wiring, signals, logging |
| `internal/dvrproto/` | DVR protocol: login/logout, frame header, frame reader, reconnecting client |
| `internal/config/` | TOML config, defaults inheritance, validation |
| `internal/rtsp/` | one RTSP server per channel (gortsplib) |
| `internal/onvif/` | ONVIF device + media SOAP services, WS-UsernameToken auth |
| `internal/discovery/` | WS-Discovery responder (UDP 3702) |
| `internal/snapshot/` | JPEG snapshots: ffmpeg decode of the latest IDR, Go-drawn placeholder |
| `internal/placeholder/` | the filler/placeholder picture: background or custom image, bitmap-font text |
| `internal/ffmpeg/` | the embedded FFmpeg: extraction into a private directory, PATH fallback |
| `build/ffmpeg/` | the reproducible recipe of the embedded FFmpeg (Dockerfile, configure flags) |
| `internal/h264pcm/` | pure Go H.264 I_PCM encoder of a still picture (the outage filler) |
| `internal/dvrweb/` | client of the DVR's web settings API and its page/field table |
| `internal/webui/` | admin/status web UI: status page, DVR settings, pass-through login |
| `internal/logring/` | recent log records for the status page |
| `internal/httpauth/` | HTTP Basic/Digest auth for the snapshot endpoint |
| `internal/fakedvr/` | test helpers: synthetic H.264/AAC frames, a localhost fake DVR |
| `deploy/`, `Dockerfile` | systemd unit example, container build |

This repo is public. No private names in any file or commit message: no real host names, site names, user names,
private addresses or local paths. Use placeholders (`dvr.example.lan`, `192.0.2.0/24` from TEST-NET-1). Grep the staged
diff for them before each commit.

## Running

```sh
make lint test build   # what CI runs
make snapshot          # release dry run into dist/ (GoReleaser, needs Docker for the images)
./bin/dvr-squirrel --config config.toml --check-config
```

`make` is the only entry point: CI and the release workflow call make targets, nothing else, so a local run is the
same as a CI run. Tools are Go tool dependencies (`tools/go.mod`, `tools/goreleaser/go.mod`), run with `go tool`.
The Makefile needs Go 1.26 or newer (and gcc for `-race`); where the host Go is older, run the same targets in a
`golang:1.26-alpine` based container with the repo mounted (plus `gcc musl-dev git docker-cli docker-cli-buildx syft`
for `snapshot`).

The release binary embeds a minimal FFmpeg (build tag `embedffmpeg`, `internal/ffmpeg/bin/`, produced by `make ffmpeg`
from the sha256-verified tarball with `build/ffmpeg/`); plain `go build`/`go test` work without it. Changing
`FFMPEG_VERSION` means a new tarball sha256 (check its signature) and the version in NOTICE (`make lint` checks it).

`make release` and pushing tags or branches are for the owner only.

Build, test and lint are safe to run freely. Tests use synthetic frames and localhost only. A fixture test replays a
real captured DVR stream when `DVR_FIXTURE` points to one; captures show private premises, so they never enter the
repo, not even in part.

Connecting to a real DVR or starting the daemon on a real network (it answers WS-Discovery multicast) needs the
owner's approval. Writing DVR settings (any `/gw/*A.xml` request) or calling the DVR's actions (`CgiAddUser`,
`CgiDelUser`, `NTPClient`, `AutoADJ`) is never done by an agent; tests use the fake DVR web server.

## Commit conventions

See the `commit-messages` skill: `<area>: Subject`, brief bullet-list bodies, referencing the patch a fix regresses.
No `Co-Authored-By` or similar trailers.

TODO (owner): branch model and release rules (tags, GitHub releases) once the repo has a remote.

## Docs

A README states what is, this file and the skills state what to do. Plans (what will or might happen, with the
reasoning) live in `.claude/plans/YYYY-MM-DD-title.md` and stay after they are applied.

Knowledge about working in this repo (a correction, a "remember", a rule for runs, commits or docs) goes into this file
or the repo's `.claude/skills/` in the same turn, never into Claude's private memory: memory is not versioned, and
other sessions and agents do not see it.
