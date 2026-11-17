# syntax=docker/dockerfile:1
#
# One Dockerfile for local builds and GoReleaser, with two final targets:
#   full: distroless static with the binary that embeds the minimal FFmpeg, snapshots are decoded frames (default)
#   slim: scratch with the binary built without FFmpeg, snapshots fall back to the placeholder picture
# Local: `make image` (builds the FFmpeg first). GoReleaser passes BINARY_SOURCE=prebuilt and puts its binaries at
# <platform>/dvr-squirrel.
# Run with host networking: WS-Discovery needs multicast, and NVRs reach the channels by the host's address.
# Configuration: DVR_HOST, DVR_USER, DVR_PASSWORD or DVR_PASSWORD_FILE; optionally /etc/dvr-squirrel/config.toml.

ARG BINARY_SOURCE=source

FROM --platform=$BUILDPLATFORM golang:1.26-alpine3.23@sha256:23f198931d4bd83b4f5a613bd75656257ae8bf832dcbc2db80e1050b5219ca2a AS source-base
ARG TARGETOS TARGETARCH VERSION=devel COMMIT DATE
ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    LDFLAGS="-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$DATE"
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd cmd
COPY internal internal

FROM source-base AS source-full
RUN go build -tags embedffmpeg -trimpath -ldflags="$LDFLAGS" -o /dvr-squirrel ./cmd/dvr-squirrel

FROM source-base AS source-slim
RUN go build -trimpath -ldflags="$LDFLAGS" -o /dvr-squirrel ./cmd/dvr-squirrel

FROM scratch AS prebuilt-full
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/dvr-squirrel /dvr-squirrel

FROM scratch AS prebuilt-slim
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/dvr-squirrel /dvr-squirrel

FROM ${BINARY_SOURCE}-full AS binary-full
FROM ${BINARY_SOURCE}-slim AS binary-slim

FROM scratch AS slim
COPY --from=binary-slim /dvr-squirrel /dvr-squirrel
USER 65534:65534
ENTRYPOINT ["/dvr-squirrel"]

# distroless rather than scratch: the embedded FFmpeg is extracted into the user's cache directory (or /tmp).
FROM gcr.io/distroless/static-debian13:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931 AS full
COPY --from=binary-full /dvr-squirrel /usr/local/bin/dvr-squirrel
ENTRYPOINT ["/usr/local/bin/dvr-squirrel"]
