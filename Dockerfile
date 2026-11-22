# syntax=docker/dockerfile:1.7
# MRSmith: one static binary with the panel embedded, on a distroless base
# (no shell, no package manager, runs as an unprivileged user).

FROM node:24-alpine AS web
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
# vite writes straight into the Go embed folder
RUN mkdir -p /src/backend/internal/webui && npm run build

FROM golang:1.26.11-alpine AS build
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download && go mod verify
COPY backend/ ./
COPY --from=web /src/backend/internal/webui/dist ./internal/webui/dist
# the version comes from backend/internal/version/VERSION
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/mrsmith ./cmd/mrsmith \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mrsmith /usr/local/bin/mrsmith
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
ENV MRSMITH_DATA_DIR=/data \
    MRSMITH_LISTEN=:8080
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 CMD ["/usr/local/bin/mrsmith", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/mrsmith"]
CMD ["serve"]
