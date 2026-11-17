# syntax=docker/dockerfile:1
#
# Minimal static FFmpeg for the snapshots: H.264 (Annex-B on a pipe) in, one MJPEG frame out. LGPL-2.1-or-later
# build: no --enable-gpl, no --enable-nonfree. Run by `make ffmpeg`, which downloads and verifies the source tarball
# into this directory first; the output is the binary, its license text and the configuration used.

FROM alpine:3.23.8@sha256:469c9ea410b9215eaee7976ba6c0639039b444be8e5d638f7abbada189971eb2 AS build
RUN apk add --no-cache build-base nasm pkgconf xz
ARG FFMPEG_VERSION
WORKDIR /src
COPY ffmpeg-${FFMPEG_VERSION}.tar.xz configure-flags ./
RUN tar xf ffmpeg-${FFMPEG_VERSION}.tar.xz --strip-components=1 \
 && ./configure $(grep -v '^#' configure-flags) \
 && make -j"$(nproc)" ffmpeg \
 && strip ffmpeg \
 && mkdir /out \
 && cp ffmpeg /out/ffmpeg \
 && cp COPYING.LGPLv2.1 /out/ \
 && ./ffmpeg -hide_banner -buildconf > /out/buildconf.txt

FROM scratch
COPY --from=build /out/ /
