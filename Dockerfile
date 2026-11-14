# syntax=docker/dockerfile:1.7
#
# TeleVideoConverter: one rootless, shell-less image built FROM scratch.
# It holds the Go binary plus the external tools it drives:
#   ffmpeg/ffprobe  - built here, fully static, only the codecs we use
#   qjs             - QuickJS-ng, static, for yt-dlp's YouTube JS challenges
#   yt-dlp          - official onedir build (bundled CPython, needs musl loader)

ARG ALPINE=alpine:3.23
ARG GOLANG=golang:1.27-alpine

# ---------------------------------------------------------------- ffmpeg ---
FROM ${ALPINE} AS ffmpeg
ARG FFMPEG_VERSION=8.0.3
RUN apk add --no-cache build-base bash nasm pkgconf git curl xz linux-headers \
        zlib-dev zlib-static openssl-dev openssl-libs-static
WORKDIR /src
RUN git clone --depth 1 --branch stable https://code.videolan.org/videolan/x264.git \
 && cd x264 \
 && ./configure --prefix=/usr/local --enable-static --enable-pic --disable-cli --disable-opencl \
 && make -j"$(nproc)" && make install
RUN curl -fsSL "https://ffmpeg.org/releases/ffmpeg-${FFMPEG_VERSION}.tar.xz" | tar -xJ \
 && cd "ffmpeg-${FFMPEG_VERSION}" \
 && PKG_CONFIG_PATH=/usr/local/lib/pkgconfig ./configure \
      --prefix=/out --pkg-config-flags=--static \
      --extra-cflags=-static --extra-ldflags=-static --extra-libs='-lpthread -lm' \
      --enable-gpl --enable-version3 --disable-autodetect --disable-debug --enable-stripping \
      --disable-doc --disable-ffplay --disable-everything \
      --enable-libx264 --enable-zlib --enable-openssl \
      --enable-protocol=file,pipe,http,https,tcp,udp,tls,crypto,hls,data \
      --enable-demuxer=mov,matroska,webm_dash_manifest,mpegts,hls,flv,aac,mp3,ogg,flac,wav,concat,image2,mjpeg,image_jpeg_pipe,image_png_pipe,image_webp_pipe,h264,hevc,m4v,gif \
      --enable-muxer=mp4,mov,ipod,matroska,webm,mpegts,adts,image2,mjpeg,null \
      --enable-decoder=h264,hevc,vp8,vp9,mpeg4,mjpeg,png,webp,gif,aac,aac_latm,mp3,mp3float,opus,vorbis,flac,pcm_s16le,pcm_f32le,ac3,eac3 \
      --enable-encoder=libx264,aac,mjpeg,png \
      --enable-parser=h264,hevc,aac,aac_latm,mpegaudio,opus,vorbis,vp8,vp9,mjpeg,png,webp,flac,ac3 \
      --enable-bsf=aac_adtstoasc,h264_mp4toannexb,hevc_mp4toannexb,extract_extradata,vp9_superframe,vp9_superframe_split,setts,null,dump_extra \
      --enable-filter=scale,format,fps,pad,null,anull,aresample,aformat,concat,setsar,setpts,asetpts,trim,atrim,split,asplit,crop,thumbnail,select \
 && make -j"$(nproc)" && make install \
 && /out/bin/ffmpeg -hide_banner -version | head -1

# --------------------------------------------------------- yt-dlp & qjs ---
FROM ${ALPINE} AS tools
ARG TARGETARCH
ARG YTDLP_VERSION=2026.10.26
ARG QJS_VERSION=v0.18.0
RUN apk add --no-cache curl unzip
RUN set -eu; \
    case "${TARGETARCH}" in \
      arm64) ytarch=_aarch64; qarch=aarch64; loader=ld-musl-aarch64.so.1 ;; \
      amd64) ytarch=;         qarch=x86_64;  loader=ld-musl-x86_64.so.1 ;; \
      *) echo "unsupported arch ${TARGETARCH}"; exit 1 ;; \
    esac; \
    mkdir -p /out/opt /out/usr/local/bin /out/lib /out/etc/ssl/certs; \
    curl -fsSLo /tmp/ytdlp.zip "https://github.com/yt-dlp/yt-dlp/releases/download/${YTDLP_VERSION}/yt-dlp_musllinux${ytarch}.zip"; \
    curl -fsSLo /tmp/SUMS "https://github.com/yt-dlp/yt-dlp/releases/download/${YTDLP_VERSION}/SHA2-256SUMS"; \
    (cd /tmp && grep " yt-dlp_musllinux${ytarch}.zip\$" SUMS | sed "s#yt-dlp_musllinux${ytarch}.zip#ytdlp.zip#" | sha256sum -c -); \
    unzip -q /tmp/ytdlp.zip -d /out/opt/yt-dlp; \
    mv "/out/opt/yt-dlp/yt-dlp_musllinux${ytarch}" /out/opt/yt-dlp/yt-dlp; \
    chmod 0755 /out/opt/yt-dlp/yt-dlp; \
    curl -fsSLo /out/usr/local/bin/qjs "https://github.com/quickjs-ng/quickjs/releases/download/${QJS_VERSION}/qjs-linux-${qarch}"; \
    chmod 0755 /out/usr/local/bin/qjs; \
    cp "/lib/${loader}" /out/lib/; \
    ln -s "${loader}" "/out/lib/libc.musl-${qarch}.so.1"; \
    cp -L /usr/lib/libz.so.1 /out/lib/libz.so.1; \
    cp /etc/ssl/certs/ca-certificates.crt /out/etc/ssl/certs/; \
    printf 'tvc:x:65532:65532::/tmp:/sbin/nologin\n' > /out/etc/passwd; \
    printf 'tvc:x:65532:\n' > /out/etc/group

# -------------------------------------------------------------------- go ---
FROM ${GOLANG} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/tvc ./cmd/tvc \
 && mkdir -p /out/tmp /out/storage /out/db /out/cookies

# ----------------------------------------------------------------- final ---
FROM scratch
COPY --from=tools /out/ /
COPY --from=ffmpeg /out/bin/ffmpeg /out/bin/ffprobe /usr/local/bin/
COPY --from=build --chown=65532:65532 /out/tmp /tmp
COPY --from=build --chown=65532:65532 /out/storage /storage
COPY --from=build --chown=65532:65532 /out/db /db
COPY --from=build /out/tvc /usr/local/bin/tvc
ENV PATH=/usr/local/bin:/opt/yt-dlp \
    HOME=/tmp \
    XDG_CACHE_HOME=/tmp/.cache \
    SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
USER 65532:65532
EXPOSE 3000
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/usr/local/bin/tvc", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/tvc"]
CMD ["serve"]
