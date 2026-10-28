FROM --platform=$BUILDPLATFORM golang:1.27-bookworm AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/discord-music-bot ./cmd/bot

FROM denoland/deno:2.3.7 AS deno

FROM debian:bookworm-slim

ENV PATH="/opt/yt-dlp-venv/bin:${PATH}" \
    XDG_CACHE_HOME=/var/lib/discord-music-bot/cache \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates ffmpeg libopus0 python3 python3-venv \
    && python3 -m venv /opt/yt-dlp-venv \
    && /opt/yt-dlp-venv/bin/pip install --no-cache-dir --upgrade "yt-dlp[default]" bgutil-ytdlp-pot-provider==2.0.2 \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

RUN groupadd --system app && useradd --system --gid app --home-dir /nonexistent app
RUN install -d -o app -g app -m 700 /var/lib/discord-music-bot
COPY --from=deno /usr/bin/deno /usr/local/bin/deno
COPY --from=build /out/discord-music-bot /usr/local/bin/discord-music-bot
USER app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/discord-music-bot"]
