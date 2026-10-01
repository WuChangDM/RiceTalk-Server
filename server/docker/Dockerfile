# RidgeRiceTalk server Dockerfile
#
# Build context must be the project root so this Dockerfile can access both
# server/ and web/ sources. Example:
#
#   docker build -t ridgericetalk:latest -f server/docker/Dockerfile .
#
# The voice and admin frontends must be built beforehand:
#
#   cd web/voice && npm ci && npm run build
#   cd web/admin && npm ci && npm run build

# -----------------------------------------------------------------------------
# Build stage
# -----------------------------------------------------------------------------
FROM golang:1.25-bookworm AS builder

WORKDIR /app

# Install build dependencies for CGO (opus via livekit/media-sdk)
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    build-essential \
    libopus-dev \
    libogg-dev \
    && rm -rf /var/lib/apt/lists/*

# Copy server dependency files first for better layer caching
COPY server/go.mod server/go.sum ./server/
RUN cd server && go mod download

# Copy server source code
COPY server/ ./server/

# Copy versioned SQL migrations so the migrate command can be run from the image.
COPY migrations/ ./migrations/

# Copy pre-built frontend artifacts
COPY web/voice/dist ./web/voice/dist
COPY web/admin/dist ./web/admin/dist

# Build the server binary with CGO enabled (required for music bot opus encoding)
RUN cd server && \
    CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o ridgericetalk cmd/server/main.go

# Build the standalone migration tool (shares the server module).
RUN cd server && \
    CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o ridgericetalk-migrate cmd/migrate/main.go

# -----------------------------------------------------------------------------
# Runtime stage
# -----------------------------------------------------------------------------
FROM debian:bookworm-slim

WORKDIR /app

# Install runtime dependencies. wget is required by the app healthcheck.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    tzdata \
    libopus0 \
    libogg0 \
    wget \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -r ridgericetalk \
    && useradd -r -g ridgericetalk -d /app -s /sbin/nologin ridgericetalk

# Create directories and assign ownership
RUN mkdir -p /app/storage /app/webhost/dist/voice /app/webhost/dist/admin /app/logs \
    && chown -R ridgericetalk:ridgericetalk /app

# Copy server binary and migration tool
COPY --from=builder --chown=ridgericetalk:ridgericetalk /app/server/ridgericetalk /app/
COPY --from=builder --chown=ridgericetalk:ridgericetalk /app/server/ridgericetalk-migrate /app/

# Copy pre-built frontend artifacts into the path the server serves
COPY --from=builder --chown=ridgericetalk:ridgericetalk /app/web/voice/dist /app/webhost/dist/voice/
COPY --from=builder --chown=ridgericetalk:ridgericetalk /app/web/admin/dist /app/webhost/dist/admin/

# Copy migration files into the runtime image for on-container migrations.
COPY --from=builder --chown=ridgericetalk:ridgericetalk /app/migrations /app/migrations

# Tell the bundled migrate tool where migrations live in this image.
ENV RRT_MIGRATIONS_PATH=/app/migrations

USER ridgericetalk

# API + Voice SPA + WebSocket /ws
EXPOSE 8080
# Admin UI + Admin API + /metrics
EXPOSE 9090

# Volume for persistent data (database, uploads, state files)
VOLUME ["/app/storage"]

CMD ["./ridgericetalk"]
