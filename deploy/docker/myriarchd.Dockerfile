# syntax=docker/dockerfile:1.7
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# myriarchd - multi-stage build: web UI (Node 20 + pnpm) -> Go binary with embedded UI -> distroless.
#
# The build context is the repository root:
#   docker build -f deploy/docker/myriarchd.Dockerfile \
#       --build-arg VERSION=1.0.0 --build-arg COMMIT=$(git rev-parse --short HEAD) \
#       -t registry.example.com/myriarch/myriarch:1.0.0 .
# Running it on Kubernetes: deploy/helm/myriarch (image path in values.yaml).
# The image holds LICENSE and THIRD_PARTY_NOTICES in /usr/share/licenses/myriarch/ (both are in the repository root, which is
# the build context); myriarchd serves the same texts under /licenses.

# ---- 1. Web UI -> internal/webui/dist -------------------------------------------------
FROM node:20-bookworm-slim@sha256:2cf067cfed83d5ea958367df9f966191a942351a2df77d6f0193e162b5febfc0 AS web
ENV CI=true
RUN corepack enable
WORKDIR /src/web
# Dependencies first (layer cache); the pnpm version comes from package.json (packageManager)
COPY web/package.json web/pnpm-lock.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store pnpm install --frozen-lockfile
COPY web/ ./
COPY api/openapi.yaml /src/api/openapi.yaml
# vite.config.ts writes to ../internal/webui/dist
RUN mkdir -p /src/internal/webui && pnpm build

# ---- 2. Go binary (-tags embedui) -------------------------------------------------------
FROM golang:1.27-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS build
# Package that receives the build metadata (-X); must equal the module path of go.mod plus /internal/version.
ARG VERSION_PKG=github.com/myriarch/myriarch/internal/version
ARG VERSION=0.0.0-dev
ARG COMMIT=unknown
ARG DATE=unknown
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -tags embedui \
      -ldflags "-s -w \
        -X ${VERSION_PKG}.Version=${VERSION} \
        -X ${VERSION_PKG}.Commit=${COMMIT} \
        -X ${VERSION_PKG}.BuildDate=${DATE}" \
      -o /out/myriarchd ./cmd/myriarchd

# ---- 3. Runtime -------------------------------------------------------------------------
FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
LABEL org.opencontainers.image.title="myriarchd" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.description="OpAMP fleet manager for OpenTelemetry Collectors"
COPY --from=build /out/myriarchd /myriarchd
# License files (Apache-2.0 and the notices of bundled software travel with the image): /usr/share/licenses/myriarch/
COPY --chmod=0644 LICENSE /usr/share/licenses/myriarch/LICENSE
COPY --chmod=0644 THIRD_PARTY_NOTICES /usr/share/licenses/myriarch/THIRD_PARTY_NOTICES
USER 65532:65532
# 8080: UI, REST, OpAMP (WebSocket) - 9090: Prometheus metrics
EXPOSE 8080 9090
ENTRYPOINT ["/myriarchd"]
CMD ["serve", "--config", "/etc/myriarchd/myriarchd.yaml"]
