# syntax=docker/dockerfile:1.7

# Build toolchains run natively on the BuildKit host. Without BUILDPLATFORM,
# the arm64 branch executes npm and the Go compiler through QEMU, which is much
# slower and makes npm ci appear to hang despite producing no progress output.
# ---- Stage 1: build the web frontend once on the native builder ----
FROM --platform=$BUILDPLATFORM node:20-alpine AS web-builder
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ---- Stage 2: cross-compile the Go binary on the native builder ----
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS go-builder
RUN apk add --no-cache git
WORKDIR /src

ARG VERSION=0.1.0-dev
ARG BUILD_TIME=""
ARG TARGETOS
ARG TARGETARCH

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Overlay the freshly built frontend so go:embed web/dist picks it up.
COPY --from=web-builder /web/dist ./web/dist

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build \
    -trimpath \
    -ldflags "-s -w -X vocat/internal/buildinfo.Version=${VERSION} -X vocat/internal/buildinfo.BuildTime=${BUILD_TIME}" \
    -o /out/vocat \
    ./cmd/vocat

# ---- Stage 3: runtime -------------------------------------------------------
FROM alpine:3.20
# Hardware access is opt-in at container launch, but the official image carries
# the userspace tools required by that mode.  Keep the checks here so an Alpine
# package-layout change fails the image build instead of failing on a modem.
RUN apk add --no-cache \
        busybox \
        ca-certificates \
        ccid \
        iproute2 \
        pcsc-lite \
        qmi-utils \
        tzdata && \
    command -v ip >/dev/null && \
    command -v busybox >/dev/null && \
    busybox udhcpc --help >/dev/null 2>&1 && \
    command -v qmi-network >/dev/null && \
    command -v qmicli >/dev/null && \
    test -x /usr/libexec/qmi-proxy && \
    addgroup -S -g 1000 vocat && \
    adduser -S -D -H -u 1000 -G vocat vocat

RUN mkdir -p /opt/vocat/bin /opt/vocat/data && \
    chown -R vocat:vocat /opt/vocat

COPY --from=go-builder /out/vocat /opt/vocat/bin/vocat
COPY scripts/docker-entrypoint.sh /usr/local/bin/vocat-entrypoint

# Symlink into /usr/local/bin so `docker exec <ctr> vocat ...` finds it via $PATH.
RUN ln -s /opt/vocat/bin/vocat /usr/local/bin/vocat && \
    chmod 0755 /usr/local/bin/vocat-entrypoint

# Hardware access and the bundled pcscd daemon require root inside the
# container. The container already needs host networking and privileged device
# access for modem, QMI, IPsec, and hot-plug support.
USER root
VOLUME ["/opt/vocat/data"]
EXPOSE 7575
ENV VOCAT_ADDR=0.0.0.0:7575 \
    VOCAT_DATABASE_PATH=/opt/vocat/data/vocat.db

ENTRYPOINT ["/usr/local/bin/vocat-entrypoint"]
