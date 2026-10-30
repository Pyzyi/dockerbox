# syntax=docker/dockerfile:1.7
# One image for the three Dockerbox services: web, broker, egress (ADR 0004).
# Base images use literal, explicit tags.
FROM --platform=$BUILDPLATFORM node:24-alpine AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts --no-audit --no-fund
COPY web/scripts ./scripts
RUN node scripts/vendor.mjs /out/vendor

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
ARG AGENT_IMAGE=ghcr.io/pyzyi/dockerbox-agentbox:latest
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY --from=ui /out/vendor ./internal/web/static/vendor
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.agentImage=${AGENT_IMAGE}" \
      -o /out/dockerbox ./cmd/dockerbox \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/dockerbox /dockerbox
# Owned by the non-root user so a fresh named volume inherits writable ownership.
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
EXPOSE 8080 8081 3128
LABEL org.opencontainers.image.title="dockerbox" \
      org.opencontainers.image.description="AI Agent in a Box - dashboard, policy broker and egress proxy" \
      org.opencontainers.image.source="https://github.com/Pyzyi/dockerbox" \
      org.opencontainers.image.licenses="MIT"
ENTRYPOINT ["/dockerbox"]
CMD ["web"]
