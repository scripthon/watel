# syntax=docker/dockerfile:1

# ---- build ----------------------------------------------------------------
FROM golang:1.25-alpine AS build

WORKDIR /src

# Dependencies are copied first so that editing source code does not
# invalidate the module download layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# CGO is off on purpose: the sqlite driver (modernc.org/sqlite) is pure Go, so
# the result is a static binary that needs nothing from the build image.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/watel ./cmd/watel

# ---- runtime --------------------------------------------------------------
FROM alpine:3.22

# ca-certificates: TLS to the Telegram API and the WhatsApp websocket.
# tzdata: makes the TZ environment variable work for timestamps.
RUN apk add --no-cache ca-certificates tzdata

# The databases live on a volume so that pairing survives image updates.
# Owned by the unprivileged user the bridge runs as.
RUN adduser -D -H -u 10001 watel && mkdir -p /data && chown watel:watel /data

COPY --from=build /out/watel /usr/local/bin/watel

USER watel
WORKDIR /data
VOLUME /data

ENV BRIDGE_DB=/data/bridge.db \
    SESSION_DB=/data/whatsapp.db

ENTRYPOINT ["/usr/local/bin/watel"]
