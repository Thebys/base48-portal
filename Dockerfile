# syntax=docker/dockerfile:1
#
# One image, two entrypoints:
#   /app/portal              web server        (default)
#   /app/portal-cron daemon  sync/fees daemon  (compose overrides `command`)
#
# Base images are pinned by digest. To bump: `docker pull golang:1.24-alpine`
# then read the new digest from `docker images --digests`.

FROM golang:1.24-alpine@sha256:8bee1901f1e530bfb4a7850aa7a479d17ae3a18beb6e09064ed54cfd245b7191 AS deps
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

FROM deps AS source
COPY . .

# Not in the default build path — BuildKit skips it unless you ask:
#   docker build --target test .
FROM source AS test
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go vet ./cmd/server ./cmd/cron ./internal/... && \
    go test ./internal/... ./cmd/server ./cmd/cron

FROM source AS builder
ARG VERSION=dev
ENV CGO_ENABLED=0 GOOS=linux
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    STAMP="${VERSION} ($(date -u '+%Y-%m-%d %H:%M UTC'))" && \
    go build -trimpath -buildvcs=false -ldflags="-s -w -X 'main.BuildDate=${STAMP}'" \
        -o /out/portal ./cmd/server && \
    go build -trimpath -buildvcs=false -ldflags="-s -w" \
        -o /out/portal-cron ./cmd/cron

FROM alpine:3.21@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d AS runtime

# ca-certificates: TLS out to Keycloak, FIO and SMTP.
# tzdata: fee periods are computed in local time.
# sqlite: online backups without a second image — hosts don't reliably have it.
RUN apk add --no-cache ca-certificates tzdata sqlite && \
    adduser -u 10001 -S -D -h /app portal

WORKDIR /app
COPY --from=builder --chmod=0755 /out/portal      /app/portal
COPY --from=builder --chmod=0755 /out/portal-cron /app/portal-cron
COPY web/ /app/web/
RUN install -d -o portal /app/data

# Migrations are compiled into the binaries (migrations/embed.go), so there is
# nothing to copy and nothing that can drift out of sync with the code.

ENV PORT=8080 \
    WEB_ROOT=/app/web \
    DATABASE_URL="file:/app/data/portal.db?_pragma=busy_timeout(5000)" \
    TZ=Europe/Prague

USER portal
EXPOSE 8080

# No VOLUME for /app/data on purpose: compose always bind-mounts it, and the
# declaration would only leave an anonymous volume behind on a bare `docker run`.

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null "http://127.0.0.1:${PORT}/healthz" || exit 1

CMD ["/app/portal"]
