# syntax=docker/dockerfile:1

# Litebase builds in two stages and ships as a single static binary with the
# dashboard embedded, so the runtime image needs no Node, no Go and no CGO
# runtime libraries.

# ---- stage 1: build the dashboard ----
FROM node:22-alpine AS frontend

WORKDIR /build

# Dependencies are installed from the lockfile first so this layer is cached
# whenever only application source changes.
COPY frontend/package.json frontend/package-lock.json* ./
RUN npm ci --no-audit --no-fund

COPY frontend/ ./
# Vite writes the bundle into the Go module's embed directory, which lives
# outside the frontend project root.
RUN mkdir -p /backend/cmd/litebase/frontend && npm run build


# ---- stage 2: build the server ----
FROM golang:1.25-alpine AS backend

WORKDIR /build

COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./
# Bring in the compiled dashboard so go:embed finds it.
COPY --from=frontend /backend/cmd/litebase/frontend ./cmd/litebase/frontend

ARG VERSION=dev
# CGO is off because the SQLite driver is pure Go; the result is a static
# binary that runs on any Linux with a matching architecture.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /litebase ./cmd/litebase


# ---- stage 3: runtime ----
FROM alpine:3.20

# ca-certificates is needed to reach Google Drive over TLS, tzdata makes backup
# timestamps render in the operator's zone, wget backs the health check, and
# su-exec lets the entrypoint drop privileges after fixing volume ownership.
RUN apk add --no-cache ca-certificates tzdata wget su-exec && \
    addgroup -g 10001 -S litebase && \
    adduser -u 10001 -S -G litebase -h /app litebase

WORKDIR /app
COPY --from=backend /litebase /usr/local/bin/litebase
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

# The data directory is the only writable path the server needs.
RUN mkdir -p /data && chown -R litebase:litebase /data /app
VOLUME ["/data"]

# The entrypoint starts as root only to fix the data volume's ownership, then
# drops to the litebase user. Deliberately no USER directive here: setting one
# would prevent that repair and break host-directory mounts.

ENV LITEBASE_DATA_DIR=/data \
    LITEBASE_ADDR=0.0.0.0:8090 \
    LITEBASE_LOG_FORMAT=json

EXPOSE 8090

# The health endpoint is unauthenticated precisely so orchestrators can use it.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget --quiet --tries=1 --spider http://127.0.0.1:8090/api/health || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["litebase"]
