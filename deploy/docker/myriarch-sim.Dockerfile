# syntax=docker/dockerfile:1.7
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# myriarch-sim - load simulator (simulated supervisors and myriarch-updaters), build context: repository root.
#   docker build -f deploy/docker/myriarch-sim.Dockerfile -t myriarch-sim .
#   docker run --rm myriarch-sim run --server ws://myriarchd:8080/v1/opamp --token myre_... --agents 1000
# The image holds LICENSE and THIRD_PARTY_NOTICES in /usr/share/licenses/myriarch/ (from the repository root, the build context).
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
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags "-s -w \
        -X ${VERSION_PKG}.Version=${VERSION} \
        -X ${VERSION_PKG}.Commit=${COMMIT} \
        -X ${VERSION_PKG}.BuildDate=${DATE}" \
      -o /out/myriarch-sim ./cmd/myriarch-sim

FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
LABEL org.opencontainers.image.title="myriarch-sim" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /out/myriarch-sim /myriarch-sim
# License files (Apache-2.0 and the notices of bundled software travel with the image): /usr/share/licenses/myriarch/
COPY --chmod=0644 LICENSE /usr/share/licenses/myriarch/LICENSE
COPY --chmod=0644 THIRD_PARTY_NOTICES /usr/share/licenses/myriarch/THIRD_PARTY_NOTICES
# Profiles and component lists (a profile refers to components-*.json relatively)
COPY --from=build /src/examples/sim /examples/sim
USER 65532:65532
# Prometheus metrics (--metrics-listen :9464) and local control API (--control-listen)
EXPOSE 9464
ENTRYPOINT ["/myriarch-sim"]
CMD ["run", "--profile", "/examples/sim/mixed-fleet.yaml", "--metrics-listen", ":9464"]
