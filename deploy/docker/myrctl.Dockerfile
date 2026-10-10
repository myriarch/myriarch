# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# myrctl - tool image for CI jobs (for example in a GitOps repository: myrctl validate, webhook call).
# Contains /usr/local/bin/myrctl, sh, curl and CA certificates. Deliberately NO entrypoint: CI systems start the job
# scripts through a shell in this image (the base image sets an entrypoint, which is reset here).
#
# Build context: a directory that contains exactly three files: the binary "myrctl" (linux/amd64), for example taken from
# dist/myrctl_linux_amd64_v1/myrctl of the goreleaser build, and the files LICENSE and THIRD_PARTY_NOTICES of the same commit
# (repository root; the release archives hold them as well). The image places the two texts in /usr/share/licenses/myriarch/.
# The base image brings its own packages (curl, CA certificates) with their licenses. Base image: the same version and digest as
# MYRCTL_BASE_IMAGE in ci/versions.env (ci/check.sh compares them).
FROM curlimages/curl:8.22.0@sha256:58adaa4e8dca9c988bae2aba4ab3434a0bb2da16bbe3f92dec39ec7785166777
ARG VERSION=0.0.0-dev
ARG REVISION=unknown
ARG SOURCE=
LABEL org.opencontainers.image.title="myrctl" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.description="myrctl (myriarch CLI) for CI jobs: validate GitOps repositories, trigger syncs" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.source="${SOURCE}"
COPY --chmod=0755 myrctl /usr/local/bin/myrctl
# License files (Apache-2.0 and the notices of bundled software travel with the image): /usr/share/licenses/myriarch/
COPY --chmod=0644 LICENSE /usr/share/licenses/myriarch/LICENSE
COPY --chmod=0644 THIRD_PARTY_NOTICES /usr/share/licenses/myriarch/THIRD_PARTY_NOTICES
ENTRYPOINT []
CMD []
