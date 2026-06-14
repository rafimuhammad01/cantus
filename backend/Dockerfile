# syntax=docker/dockerfile:1.7

# ----- build stage -----
FROM golang:1.26-bookworm AS build
WORKDIR /src

# Cache go mod download
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static binary so it runs in the slim runtime without libc surprises.
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
RUN go build -trimpath -ldflags='-s -w' -o /out/cantus ./cmd/server

# ----- runtime stage -----
FROM debian:bookworm-slim AS runtime

# yt-dlp needs python3; ffmpeg + rubberband-cli are the CLI shifters; ca-certs
# for HTTPS to R2/Modal/YouTube; tini for clean PID-1 signal handling.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        ffmpeg \
        rubberband-cli \
        python3 \
        python3-pip \
        python3-venv \
        tini \
    && rm -rf /var/lib/apt/lists/*

# Install yt-dlp + the bgutil PoT provider plugin into a dedicated venv so the
# system Python stays clean and the plugin lives next to the yt-dlp it patches.
RUN python3 -m venv /opt/ytdlp \
    && /opt/ytdlp/bin/pip install --no-cache-dir --upgrade pip \
    && /opt/ytdlp/bin/pip install --no-cache-dir \
        yt-dlp \
        bgutil-ytdlp-pot-provider \
    && ln -s /opt/ytdlp/bin/yt-dlp /usr/local/bin/yt-dlp

WORKDIR /app
COPY --from=build /out/cantus /usr/local/bin/cantus

# Cache dir (used in local STORAGE_BACKEND=local; harmless when r2).
RUN mkdir -p /app/tmp/cache

EXPOSE 8080
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["/usr/local/bin/cantus"]
