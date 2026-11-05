# Build context: repository root (the Go workspace spans apps/backend + agentkit).
#   docker build -f apps/backend/Dockerfile .

FROM golang:1.27-trixie AS build
WORKDIR /src
COPY go.work go.work.sum ./
COPY apps/backend/go.mod apps/backend/go.sum apps/backend/
COPY apps/backend/agentkit/go.mod apps/backend/agentkit/go.sum apps/backend/agentkit/
RUN cd apps/backend && go mod download
COPY apps/backend apps/backend
RUN cd apps/backend && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/server ./cmd/server

FROM debian:trixie-slim
# chromium + fonts: the renderer screenshots generated components (docs/backend.render.md)
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl chromium \
      fonts-noto-core fonts-noto-color-emoji fonts-liberation fonts-dejavu-core \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --create-home --home-dir /home/app --uid 10001 app \
 && mkdir -p /data && chown app:app /data
COPY --from=build /out/server /usr/local/bin/server
USER app
WORKDIR /home/app
ENV HTTP_ADDR=:8080 \
    DB_PATH=/data/app.db \
    RENDER_CHROME_PATH=/usr/bin/chromium
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=5 \
  CMD curl -fsS http://127.0.0.1:8080/readyz || exit 1
ENTRYPOINT ["/usr/local/bin/server"]
