FROM node:22-bookworm-slim AS frontend
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.26-bookworm AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/web/dist ./web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/frogbot ./cmd/frogbot

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl git && rm -rf /var/lib/apt/lists/* && groupadd --gid 10001 frogbot && useradd --uid 10001 --gid frogbot --create-home frogbot && mkdir -p /var/lib/frogbot/data /var/lib/frogbot/workspace && chown -R frogbot:frogbot /var/lib/frogbot
COPY --from=backend /out/frogbot /usr/local/bin/frogbot
ENV FROGBOT_MODE=server FROGBOT_NO_BROWSER=1 DATA_DIR=/var/lib/frogbot/data WORKSPACE=/var/lib/frogbot/workspace
USER frogbot
WORKDIR /var/lib/frogbot
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 CMD curl --fail --silent http://127.0.0.1:8080/api/health || exit 1
ENTRYPOINT ["/usr/local/bin/frogbot"]
