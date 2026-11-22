<p align="center"><img src="assets/dvr-squirrel-256.png" width="160" alt="DVR Squirrel"></p>

# DVR Squirrel

[![ci](https://github.com/andras-tim/dvr-squirrel/actions/workflows/ci.yml/badge.svg)](https://github.com/andras-tim/dvr-squirrel/actions/workflows/ci.yml)

Bridges legacy camera recorders to modern NVRs: a small Go daemon that turns an old HiSilicon-based DVR (the
Panacom OEM family with the `HiCtrl.ocx` ActiveX client and the PC Viewer software) into ONVIF cameras, with a
status and admin web UI that replaces the DVR's dead ActiveX pages.

The DVR speaks only a proprietary TCP protocol (used by its ActiveX/OCX browser client) and offers no RTSP or ONVIF.
The bridge keeps one logged-in connection to the DVR, splits the multiplexed stream by channel, and serves each
channel as its own ONVIF Profile S device with an RTSP stream. NVRs with ONVIF adoption, such as UniFi Protect
("third-party ONVIF camera"), can then add the channels as cameras.

Status: early development. The protocol layer is tested against captured streams and the RTSP/ONVIF side against
local clients (gortsplib, FFmpeg). In a live test UniFi Protect adopted all four channels of a 4-channel DVR, one
address and MAC (macvlan) per channel.

## Quick start

The bridge needs only the DVR's address and an account; a view-only one is enough.

```sh
dvr-squirrel discover                         # optional: find DVRs on the local networks, without logging in
export DVR_HOST=dvr.example.lan DVR_USER=viewer DVR_PASSWORD_FILE=/etc/dvr-squirrel/dvr-password
dvr-squirrel generate-config --output /etc/dvr-squirrel/config.toml
$EDITOR /etc/dvr-squirrel/config.toml         # name the cameras, NVRs show these names
dvr-squirrel
```

Without a config file every channel the DVR reports is bridged, named `CH<n>`, with the model detected from the
DVR; the status page is on port 8090.

## Install

### Release binaries

Each [release](https://github.com/andras-tim/dvr-squirrel/releases) has static Linux binaries (`linux_amd64`), a
checksum file signed with Sigstore (keyless, by the release workflow), SBOMs and build provenance attestations:

```sh
VERSION=0.1.0
BASE=https://github.com/andras-tim/dvr-squirrel/releases/download/v$VERSION
ARCHIVE=dvr-squirrel_${VERSION}_linux_amd64.tar.gz
curl -LO "$BASE/$ARCHIVE" -LO "$BASE/checksums.txt" -LO "$BASE/checksums.txt.sigstore.json"

sha256sum --ignore-missing -c checksums.txt
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/andras-tim/dvr-squirrel/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify "$ARCHIVE" --repo andras-tim/dvr-squirrel

tar xzf "$ARCHIVE" && sudo install -m 0755 dvr-squirrel /usr/local/bin/
```

The release binary embeds a minimal FFmpeg (H.264 decoder and JPEG encoder only, 3 MB, LGPL-2.1-or-later,
see [NOTICE](NOTICE)) for the snapshot pictures. At startup it is written into a private directory
(`$RUNTIME_DIRECTORY` or `$CACHE_DIRECTORY` under systemd, else the user cache directory, else a new temporary
directory), checked against its sha256 and made executable. Its source tarball and build recipe are attached to
every release; `make ffmpeg` rebuilds it from them.

`[snapshot] ffmpeg` selects another FFmpeg instead: any path (e.g. a static build under `/opt`) or a name looked up
in `PATH`. Builds without the embedded one (`go install`, the `-slim` image) use `ffmpeg` from `PATH` when there is
one, else serve the placeholder picture. Sources of an FFmpeg: the distribution packages (`apt install ffmpeg`,
`apk add ffmpeg`), static Linux builds (<https://johnvansickle.com/ffmpeg/>,
<https://github.com/BtbN/FFmpeg-Builds/releases>) or <https://ffmpeg.org/download.html>.

### go install

```sh
go install github.com/andras-tim/dvr-squirrel/cmd/dvr-squirrel@latest
```

`dvr-squirrel --version` then reports the module version.

### Container image

Two variants of each release on `ghcr.io/andras-tim/dvr-squirrel`, signed with Sigstore and attested:

- `:latest`, `:<version>`: distroless with the binary that embeds FFmpeg, snapshots are decoded video frames;
- `:slim`, `:<version>-slim`: scratch with the binary built without FFmpeg, snapshots are the placeholder picture.

Pick `latest`: NVRs such as UniFi Protect show the snapshots as camera previews. `slim` is for size-constrained
setups that do without them.

```sh
docker run -d --name dvr-squirrel --network host --restart unless-stopped \
  -e DVR_HOST=dvr.example.lan -e DVR_USER=viewer -e DVR_PASSWORD_FILE=/run/secrets/dvr-password \
  -v "$PWD/dvr-password:/run/secrets/dvr-password:ro" \
  -v "$PWD/config.toml:/etc/dvr-squirrel/config.toml:ro" \
  ghcr.io/andras-tim/dvr-squirrel:latest

cosign verify ghcr.io/andras-tim/dvr-squirrel:latest \
  --certificate-identity-regexp '^https://github.com/andras-tim/dvr-squirrel/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify oci://ghcr.io/andras-tim/dvr-squirrel:latest --repo andras-tim/dvr-squirrel
```

The config file mount is optional (camera names and models). Host networking is needed: WS-Discovery uses multicast, and NVRs reach the channels by the host's addresses. The
image runs as an unprivileged user, so use ports of 1024 and above, or grant `CAP_NET_BIND_SERVICE`. With a read-only root file system, give it a writable
`/tmp` (`--tmpfs /tmp:exec`) for the embedded FFmpeg. Set `TZ` for
the local time shown on the filler picture.

## Build and test

`make` is the single entry point, for local runs and CI alike; the tools (staticcheck, govulncheck, actionlint,
GoReleaser) are pinned as Go tool dependencies under `tools/`. Requires Go 1.26 or newer.

```sh
make lint       # gofmt check, go vet, staticcheck, govulncheck, actionlint, NOTICE check
make test       # go test -race ./...
make ffmpeg     # the minimal FFmpeg to embed, built in Docker from the verified source tarball
make test-embedded  # the tests that run the embedded FFmpeg
make build      # bin/dvr-squirrel, static, with the embedded FFmpeg and the version stamped from git
make build-slim # bin/dvr-squirrel-slim, without FFmpeg (as `go install` builds it)
make image      # local images from the Dockerfile: dvr-squirrel:dev and :dev-slim
make snapshot   # release dry run into dist/: archives, checksums, SBOMs, images; nothing signed or published
```

The tests use synthetic frames and localhost only. `TestFixture` additionally replays captured DVR-to-client streams
named in `DVR_FIXTURE` (a path list), e.g. `DVR_FIXTURE=capture.bin go test -v -run Fixture ./internal/dvrproto`.

### Releasing

Push a signed `v*` tag; the release workflow runs `make release` (GoReleaser): the GitHub release with the archives,
checksums, SBOMs and the Sigstore bundle, the two images, and the provenance attestations.

```sh
git tag -s v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

## Configuration

Everything has a default or comes from the DVR; the required input is the DVR's address and an account:

| setting | flag | environment | config file |
|---|---|---|---|
| DVR host | `--dvr-host` | `DVR_HOST` | `[dvr] host` |
| DVR media port (8000) | `--dvr-port` | `DVR_PORT` | `[dvr] port` |
| DVR account | `--dvr-user` | `DVR_USER` | `[dvr] user` |
| DVR password | `--dvr-password-file` | `DVR_PASSWORD`, `DVR_PASSWORD_FILE` (Docker secrets) | `[dvr] password_file` |

Flags take precedence over the environment, the environment over the config file, the file over the defaults.
`dvr-squirrel --check-config` prints the effective config and where each value came from (secrets masked).

The config file is optional; the default path is `/etc/dvr-squirrel/config.toml` (a missing default file is
fine, a missing `--config` file is an error). Its main use is naming the cameras, as NVRs show them:

```toml
[[channel]]
index = 1
name = "Front door"
model = "Dome camera"

[[channel]]
index = 2
name = "Yard"
model = "Bullet camera"
```

`dvr-squirrel generate-config` writes such a file for the connected DVR, one block per channel with `CH<n>` and
the detected model, and every other option commented out with its default. [config.example.toml](config.example.toml)
describes every key.

- With `[[channel]]` blocks exactly those channels are bridged, from startup; without any, every channel the DVR
  reports, once it has been reached.
- The default model is the DVR's type, read from the DVR: "DVR 500 series, 4 channels" (see the type codes in
  [docs/protocol.md](docs/protocol.md)); a configured `model` is used as is.
- `[dvr]`: also `reconnect_seconds` and `reconnect_max_seconds` (default 10): after an error the reconnect delay
  starts at the first and doubles up to the second; the DVR is usually on a LAN or VPN, so a short cap brings the
  streams back soon after the link returns. `web_port` (80) is the DVR's HTTP port: type detection and the settings
  pages of the web UI.
- `[web]`: the status/admin UI (`enabled`, default on; `listen`, default `:8090`).
- `[discovery]`: the WS-Discovery responder.
- `[defaults]`: settings inherited by every channel; `[[channel]]` may override them: `enabled`, `audio`,
  `listen_ip`, `rtsp_port`, `rtsp_path`, `onvif_port`, `user`, `password_file`, `model`, `serial`, `firmware`,
  `filler_image`, `filler_text`. Only in `[[channel]]`: `index`, `name` and `mac`, the MAC reported to NVRs
  (default: the MAC of the interface holding `listen_ip`).

Authentication is always on: ONVIF takes WS-UsernameToken (digest or plain text), RTSP takes HTTP digest. The
credentials default to the DVR login. File paths may contain `${VAR}`, e.g. `${CREDENTIALS_DIRECTORY}/dvr-password`
for a systemd credential.

Each channel gets an address of its own with the ports 554 and 80, or the channels share the host's address with
ports of their own: 8550+n and 8080+n for channel n. For NVRs that key cameras by MAC, such as UniFi Protect, each
channel's address needs an interface of its own (see [UniFi Protect](#unifi-protect)). Unknown keys, overlapping
listeners and unreadable password files are errors.

### Finding the DVR

`dvr-squirrel discover [CIDR...]` scans the host's IPv4 networks (or the given ones) for compatible DVRs,
read-only and without logging in: a TCP connect to port 8000, then the web server's banner and its answer to
`/gw/Port.xml`, 20 hosts at a time. It prints the address, the ports, the server header and the type code when the
DVR tells it without a login.

## Running

```sh
dvr-squirrel --config config.toml --check-config   # validate, print the resolved config without secrets
dvr-squirrel --config config.toml [--log-level debug]
```

Per enabled channel the bridge serves:

- ONVIF at `http://<address>:<onvif_port>/onvif/device_service` (the media service shares the endpoint): one
  profile `main`, H.264 at the DVR's resolution. Without credentials only `GetSystemDateAndTime`,
  `GetCapabilities` and `GetServices` answer. Each channel has a stable endpoint UUID derived from its serial.
  `GetNetworkInterfaces` reports the channel's MAC: the configured `mac`, else the MAC of the interface holding
  `listen_ip`, else a locally administered one derived from the serial; the log line "channel ready" shows which.
- RTSP at `rtsp://<address>:<rtsp_port><rtsp_path>`, TCP (interleaved) transport only. A client joining mid-stream
  receives nothing until the next IDR frame (about 1.3 s), so its first picture decodes. With `audio = true` the
  stream also carries the DVR's AAC track.
- A snapshot at `http://<address>:<onvif_port>/snapshot.jpg` (the `GetSnapshotUri` answer), behind HTTP digest or
  basic auth with the channel credentials. FFmpeg decodes the latest IDR frame to JPEG; the result is cached for
  `cache_seconds` and shared by concurrent requests. While the channel is offline it shows the filler picture; when
  FFmpeg is disabled, missing or fails, a placeholder with the channel name (the fallback is logged once).
- WS-Discovery on UDP 3702: Hello on start, Bye on stop, and a ProbeMatch per channel for probes of
  `dn:NetworkVideoTransmitter` or `tds:Device`. The socket uses `SO_REUSEPORT`, so it coexists with other responders
  on the host (e.g. wsdd).

### Web UI

With `[web] enabled = true` the daemon serves an admin/status UI on `[web] listen` (default `:8090`), plain HTML with
no JavaScript and no external resources, in a light or dark theme following the system setting:

- **Status**, public without a login: the DVR session (online or offline since, channel count), per channel the
  video state (live or filler), resolution, frame rate, bitrate, frames, last IDR frame and the number of RTSP
  clients. Logged-in users also see the DVR address and last error, the RTSP client addresses and the recent log
  events, as these can hold addresses.
- **DVR settings:** the DVR's configuration pages (channels, recording, detectors, system, network, screen, PTZ,
  audio), read and written through the DVR's web settings API with the fields described in
  [docs/protocol.md](docs/protocol.md). A write sends the complete set the DVR's own form would send, with the edited
  fields changed, then reads the page again and shows what changed.
- **Login:** with a DVR account, checked against the DVR; after 5 failed logins in a minute from one client (or 20
  in total) the login answers 429 without asking the DVR. The credentials stay in memory for the session only (30 min
  idle timeout); the bridge config holds no administrator credential. The DVR's own rights decide what a user may
  change; pages without the right are read only. Writes carry a per-session CSRF token.
- Passwords stored on the DVR are never shown; account management is read only, and actions such as NTP sync or
  adding users are not offered. The bridge never changes DVR settings by itself.

The UI uses plain HTTP, like the DVR: keep it on a trusted network.

### DVR outages

NVRs such as UniFi Protect mark a camera offline once its RTSP stream stops, so the streams never stop:

- At startup, and whenever the DVR delivered no video for 10 s or its session ended, every channel switches to a
  filler picture: one IDR frame per second, drawn and encoded in Go (H.264 I_PCM macroblocks, SPS and PPS in band).
  It shows the channel name, "OFFLINE since HH:MM" (local time) with the time elapsed since then ("12 min",
  "1 h 5 min"; redrawn once a minute) and the reason, "no connection to DVR", over a dark
  background or the channel's `filler_image`; `filler_text = false` leaves the picture without text.
- The channel returns to live video at the first IDR frame after the DVR reconnects, within the same RTSP sessions.
  A client joining during an outage gets the filler within a second.
- RTP timestamps stay continuous and increasing per channel across the switches and DVR reconnects: the timeline
  continues from its last value by the elapsed wall time, whatever the DVR's own counter does.
- Audio pauses during an outage.

On SIGINT or SIGTERM it sends the logout message to the DVR before closing the connection.

Deployment examples:

- systemd: [deploy/dvr-squirrel.service](deploy/dvr-squirrel.service) runs it as a `DynamicUser` with
  `CAP_NET_BIND_SERVICE` only, and passes the DVR password as a systemd credential.
- Docker: the [Dockerfile](Dockerfile) has the two image variants as targets, `full` and `slim`, built from
  source by `make image` or from the release binaries by GoReleaser; see [Container image](#container-image).

Not implemented: RTP over UDP or multicast, PTZ, events, playback and the DVR's other features
([docs/protocol.md](docs/protocol.md) documents them).

## UniFi Protect

Findings from adopting the channels as third-party ONVIF cameras:

- Protect keys a camera by its MAC address: a discovered one by the MAC it resolves for the camera's address (ARP),
  an adopted one by the `HwAddress` of `GetNetworkInterfaces`. Channels whose addresses share an interface show up
  as one camera, and when the two MACs differ, Protect lists the adopted camera again as a new one on every
  discovery. Give each channel its own `listen_ip` on an interface of its own, e.g. a macvlan with a fixed MAC, and
  keep the default ports; the bridge then reports that interface's MAC. Example with systemd-networkd, one per
  channel (the parent interface's `.network` also needs `MACVLAN=cam1`):

  ```ini
  # cam1.netdev
  [NetDev]
  Name=cam1
  Kind=macvlan
  MACAddress=02:00:5e:00:53:11

  [MACVLAN]
  Mode=bridge
  ```

  The host then has several interfaces in one subnet: answer ARP only on the interface holding the address
  (`net.ipv4.conf.<if>.arp_ignore=1`, `arp_announce=2`) and send each address's replies out of its own interface
  (a routing policy rule per source address), or the MAC on the wire is the wrong one again.
- Protect requests the snapshot about once a minute and shows no preview image without it.
- Protect names a camera "<manufacturer> <model>" from `GetDeviceInformation` and shows the WS-Discovery/GetScopes
  name scope in its model column. The bridge therefore reports the channel name as the manufacturer with an empty
  model, and the configured (or detected) model in the name and hardware scopes, so Protect shows the channel name as
  the name and the model as the model. Verified for the joined form; how Protect treats the empty model is to be
  checked in the next live test. Protect may keep the name it stored at adoption; renaming in Protect stays
  possible.

## DVR protocol

The DVR serves its media on one TCP connection to port 8000: after a 44-byte login with an MD5 digest of the
credentials, it pushes all channels, multiplexed, as 32-byte headers followed by H.264 (Annex-B) or AAC (ADTS) payloads.
The bridge sends nothing after the login except a logout on shutdown, and reconnects on any error.
The full reference is [docs/protocol.md](docs/protocol.md).

## License

GPL-3.0-or-later: DVR Squirrel is free software; you can redistribute it and/or modify it under the terms of the GNU
General Public License as published by the Free Software Foundation, either version 3 of the License, or (at your
option) any later version. See [LICENSE](LICENSE).
