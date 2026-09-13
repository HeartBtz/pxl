# syntax=docker/dockerfile:1.7
FROM golang:1.26.7-alpine3.24 AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /build
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
ARG BUILD_TIME=unknown
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X main.version=${VERSION} -X main.buildTime=${BUILD_TIME}" -trimpath -o /pxl ./cmd/server

# ── Production stage ──
FROM alpine:3.24.1
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 1000 -h /app pxl
WORKDIR /app

COPY --from=builder /pxl .
COPY --from=builder /build/internal/handler/templates /app/internal/handler/templates
COPY --from=builder /build/web/static /app/web/static
COPY --from=builder /build/migrations /app/migrations

RUN mkdir -p /app/data/images /tmp/pxl \
 && chown -R pxl:pxl /app /tmp/pxl

USER pxl

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://localhost:8080/health || exit 1

LABEL org.opencontainers.image.title="PXL" \
      org.opencontainers.image.description="Self-hosted image hosting" \
      org.opencontainers.image.vendor="HeartBtz" \
      org.opencontainers.image.source="https://github.com/HeartBtz/pxl" \
      org.opencontainers.image.licenses="Apache-2.0"

ENTRYPOINT ["./pxl"]
