# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Myriarch - collector image with the OpAMP supervisor + otelcol-contrib (one container, two processes)
#
# There is no official image with both binaries: the supervisor image (alpine, UID 10001) contains only the
# supervisor, the contrib image (scratch) only the collector. Supervisor and collector must have the same version.
#
#   docker build -f deploy/k8s/collector/Dockerfile --build-arg OTELCOL_VERSION=0.162.0 \
#       --build-arg UPDATER_VERSION=$(git describe --tags --always) --build-arg UPDATER_COMMIT=$(git rev-parse --short HEAD) \
#       -t registry.example.com/observability/otel-supervisor-collector:0.162.0 .
#
# The build context is the repository root (myriarch-updater is built as well; container mode: deploy/k8s/collector/README.md).
#
# myriarch-updater: by default it is compiled from the build context. To take the released binary instead, pass the published
# updater image by digest (the release notes name it; the binary is the one in the signed release archive):
#   --build-arg UPDATER_SOURCE=image --build-arg UPDATER_IMAGE=ghcr.io/myriarch/myriarch-updater@sha256:<digest>
# The image is FROM scratch with /usr/local/bin/myriarch-updater. A build with UPDATER_SOURCE=image and no UPDATER_IMAGE fails
# at the COPY. The Dockerfile builds for the platform of the build host (linux/amd64 when compiled from source).
#
# Without a container runtime (CI systems without Docker, developer Mac): scripts/build-collector-image-crane.sh <version> builds
# the same image with crane (same contract: numeric user 10001, group 0 writable, ENTRYPOINT/CMD); change both together.
#
# ENTRYPOINT/CMD remain the supervisor (older manifests run unchanged). Version changes in Kubernetes:
#  - image tag (GitOps/pipeline, webhook executor); myriarchd recognizes completion by the reported service.version, or
#  - container mode: `command: [/usr/local/bin/myriarch-updater, container, --config=...]`; myriarch-updater swaps the binary
#    on the volume /var/lib/myriarch (target version chosen in the UI, executor `updater`).
#
# Licenses: the image carries the license files described below (LICENSE and THIRD_PARTY_NOTICES of myriarch, LICENSE and README of
# the OpenTelemetry software). The alpine base of the supervisor image brings its own packages with their licenses.
#
# Supply chain: the upstream images are pinned by digest (the defaults below, digests and version in ci/versions.env; a
# build for another version passes --build-arg COLLECTOR_IMAGE=otel/opentelemetry-collector-contrib@sha256:... and
# SUPERVISOR_IMAGE=...@sha256:...) and verified beforehand with contrib/enterprise-mirror/verify-upstream-images.sh:
# the binaries in the image (/otelcol-contrib and /usr/local/bin/opampsupervisor) must be bit-identical to the release
# artifacts of github.com/open-telemetry/opentelemetry-collector-releases, whose SHA-256 and Sigstore bundle (identity of
# the release workflow, issuer token.actions.githubusercontent.com) are verified. Background: the supervisor image is
# NOT signed with cosign upstream, and the contrib image of 0.162.0 is signed by a maintainer account (not by the
# workflow) - the signed release binaries are therefore the reliable anchor.
# The defaults are the reviewed digests of ci/versions.env (OTELCOL_CONTRIB_IMAGE, OTELCOL_SUPERVISOR_IMAGE) and the Go image
# of the other Dockerfiles; ci/check.sh keeps them equal. A build for another OTELCOL_VERSION overrides all three images.
ARG OTELCOL_VERSION=0.162.0
ARG COLLECTOR_IMAGE=otel/opentelemetry-collector-contrib:0.162.0@sha256:39923a8e431bd1f57be82411999d389fcfe40857492e4365456d97a4c1f74be6
ARG SUPERVISOR_IMAGE=otel/opentelemetry-collector-opampsupervisor:0.162.0@sha256:13af4526381fe8279fb78f211628fea5cae693bf413737f234ee3587d9a44fb6
ARG GO_IMAGE=golang:1.27-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5
# Where myriarch-updater comes from: build (compile from the build context) or image (copy from UPDATER_IMAGE). Only these two
# values are valid: the final stage is chosen as "updater-${UPDATER_SOURCE}", and Docker cannot validate the value, so a value
# that names no stage (a typo) is resolved as an image reference from the default registry, unpinned. Do not pass anything else.
ARG UPDATER_SOURCE=build
# "scratch" is a placeholder that keeps the unused stage below valid; UPDATER_SOURCE=image needs a real image by digest
ARG UPDATER_IMAGE=scratch

# myriarch-updater (static, linux/amd64) for container mode, compiled from the build context
FROM ${GO_IMAGE} AS updater-build
# Package that receives the build metadata (-X); must equal the module path of go.mod plus /internal/version.
ARG VERSION_PKG=github.com/myriarch/myriarch/internal/version
ARG UPDATER_VERSION=0.0.0-dev
ARG UPDATER_COMMIT=unknown
ARG UPDATER_DATE=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
      -ldflags "-s -w -X ${VERSION_PKG}.Version=${UPDATER_VERSION} -X ${VERSION_PKG}.Commit=${UPDATER_COMMIT} -X ${VERSION_PKG}.BuildDate=${UPDATER_DATE}" \
      -o /out/myriarch-updater ./cmd/myriarch-updater

# myriarch-updater of the release: the published updater image (FROM scratch, the binary is at its entrypoint path)
FROM ${UPDATER_IMAGE} AS updater-release-src
# UPDATER_SOURCE=image
FROM scratch AS updater-image
COPY --from=updater-release-src /usr/local/bin/myriarch-updater /out/myriarch-updater

# The stage that the collector image copies from: updater-build or updater-image, nothing else. Both write /out/myriarch-updater.
FROM updater-${UPDATER_SOURCE} AS updater

FROM ${COLLECTOR_IMAGE} AS collector

FROM ${SUPERVISOR_IMAGE}
ARG OTELCOL_VERSION
ARG COLLECTOR_IMAGE
ARG SUPERVISOR_IMAGE
# No OCI licenses label (image.licenses): the alpine base of the supervisor image brings packages under other licenses
# (scripts/build-collector-image-crane.sh omits the label for the same reason).
LABEL org.opencontainers.image.title="myriarch collector (opampsupervisor + otelcol-contrib)" \
      org.opencontainers.image.version="${OTELCOL_VERSION}"
COPY --from=collector --chmod=0755 /otelcol-contrib /usr/local/bin/otelcol-contrib
COPY --from=updater --chmod=0755 /out/myriarch-updater /usr/local/bin/myriarch-updater
# License files (0644 root:root; the build context is the repository root, LICENSE and THIRD_PARTY_NOTICES must exist there):
#  - /usr/share/licenses/myriarch/: the project license and the notices of the third-party software in myriarch-updater
#  - /usr/share/licenses/opentelemetry-collector/: Apache-2.0 text of the upstream OpenTelemetry software (a copy of LICENSE; it
#    equals the upstream LICENSE of opentelemetry-collector-contrib and -releases apart from one leading empty line) and a README
#    that names the upstream software, its copyright holder, repositories, tags and images (written below).
COPY --chmod=0644 LICENSE /usr/share/licenses/myriarch/LICENSE
COPY --chmod=0644 THIRD_PARTY_NOTICES /usr/share/licenses/myriarch/THIRD_PARTY_NOTICES
COPY --chmod=0644 LICENSE /usr/share/licenses/opentelemetry-collector/LICENSE
USER root
# README of the upstream software: the same text as scripts/build-collector-image-crane.sh writes (there the image references are
# the digests it pinned; here they are the build arguments as given - pin them by digest in the release pipeline).
RUN printf '%s\n' \
      "OpenTelemetry Collector and OpAMP Supervisor in this image" \
      "==========================================================" \
      "" \
      "This image contains two programs of the OpenTelemetry project, taken unchanged from the upstream container images:" \
      "" \
      "  /usr/local/bin/otelcol-contrib   otelcol-contrib (OpenTelemetry Collector, contrib distribution) ${OTELCOL_VERSION}" \
      "  /usr/local/bin/opampsupervisor   opampsupervisor (OpAMP Supervisor) ${OTELCOL_VERSION}" \
      "" \
      "Copyright The OpenTelemetry Authors" \
      "License: Apache-2.0. The text is in the file LICENSE next to this README." \
      "" \
      "Upstream repositories and tags:" \
      "  https://github.com/open-telemetry/opentelemetry-collector-releases  v${OTELCOL_VERSION} (otelcol-contrib), cmd/opampsupervisor/v${OTELCOL_VERSION} (opampsupervisor)" \
      "  https://github.com/open-telemetry/opentelemetry-collector-contrib   v${OTELCOL_VERSION}" \
      "  https://github.com/open-telemetry/opentelemetry-collector           v${OTELCOL_VERSION}" \
      "" \
      "Upstream images this image was built from:" \
      "  ${SUPERVISOR_IMAGE}" \
      "  ${COLLECTOR_IMAGE}" \
      "" \
      "The software of the myriarch project in this image (myriarch-updater) is licensed under Apache-2.0:" \
      "/usr/share/licenses/myriarch/LICENSE. The notices of its third-party software are in" \
      "/usr/share/licenses/myriarch/THIRD_PARTY_NOTICES." \
      > /usr/share/licenses/opentelemetry-collector/README \
 && chmod 0644 /usr/share/licenses/opentelemetry-collector/README
# Supervisor storage (persistent_state.yaml = instance UID) and room for the mounted configs.
# In the cluster it is overlaid by a PVC (StatefulSet) or hostPath (DaemonSet) so that the identity stays stable.
# /var/lib/myriarch: volume of container mode (collector binaries versions/<v> + current, state of the myriarch-updater),
# /etc/myriarch/trust: trusted_root.json (offline Sigstore verification). Writable for group 0 (OpenShift: arbitrary UID, GID 0).
RUN mkdir -p /var/lib/otelcol/supervisor /var/lib/myriarch /etc/opampsupervisor /etc/myriarch/trust \
 && chown -R 10001:0 /var/lib/otelcol /var/lib/myriarch && chmod -R g+rwX /var/lib/otelcol /var/lib/myriarch
USER 10001:10001
WORKDIR /var/lib/otelcol
ENTRYPOINT ["opampsupervisor"]
CMD ["--config=/etc/opampsupervisor/supervisor.yaml"]
