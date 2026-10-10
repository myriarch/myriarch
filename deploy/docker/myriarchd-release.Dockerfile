# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# myriarchd - runtime image for releases: contains EXACTLY the binary from the release build (goreleaser, listed in
# checksums.txt and signed), not one recompiled during the image build. It therefore needs no network access in the build
# (no npm, no Go proxy). The multi-stage Dockerfile myriarchd.Dockerfile remains for local builds.
#
# Build context: a directory that contains exactly three files: the binary "myriarchd" (linux/amd64, with the embedded web UI),
# for example taken from dist/myriarchd_linux_amd64_v1/myriarchd of the goreleaser build, and the files LICENSE and
# THIRD_PARTY_NOTICES of the same commit (repository root; the release archives hold them as well). The image places the two
# texts in /usr/share/licenses/myriarch/. The build fails if one of the three files is missing.
#   docker build -f deploy/docker/myriarchd-release.Dockerfile --build-arg VERSION=0.1.0 --build-arg REVISION=<sha> <context>
# Running it on Kubernetes: deploy/helm/myriarch (image path in values.yaml).
FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
ARG VERSION=0.0.0-dev
ARG REVISION=unknown
ARG SOURCE=
LABEL org.opencontainers.image.title="myriarchd" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.description="OpAMP fleet manager for OpenTelemetry Collectors" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.source="${SOURCE}"
COPY --chmod=0755 myriarchd /myriarchd
# License files (Apache-2.0 and the notices of bundled software travel with the image): /usr/share/licenses/myriarch/
COPY --chmod=0644 LICENSE /usr/share/licenses/myriarch/LICENSE
COPY --chmod=0644 THIRD_PARTY_NOTICES /usr/share/licenses/myriarch/THIRD_PARTY_NOTICES
USER 65532:65532
# 8080: UI, REST, OpAMP (WebSocket) - 9090: Prometheus metrics
EXPOSE 8080 9090
ENTRYPOINT ["/myriarchd"]
CMD ["serve", "--config", "/etc/myriarchd/myriarchd.yaml"]
