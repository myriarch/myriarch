---
title: "Upstream notes"
weight: 40
description: "Verified facts about third-party software that the design relies on, each with a public source and a date."
---

Facts about third-party software that the design relies on. Each item names a public source. The versions reviewed are
the OpAMP Supervisor and collector 0.162.0, opamp-go 0.25.0, sigstore-go 1.3.0, the OpenTelemetry Operator 0.160.0 and
embedded-postgres 1.34.0. The review of the release assets and the supervisor was done on 2026-10-06 against real
binaries and the public release pages. Upstream may change these details; the dated status at the end shows what was
rechecked last.

## Supervisor limits

Sources: the [supervisor README](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/cmd/opampsupervisor/README.md),
its [specification](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/cmd/opampsupervisor/specification/README.md)
and the [config schema in the source](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.162.0/cmd/opampsupervisor/supervisor/config/config.go).
Stability is alpha ([metadata.yaml](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.162.0/cmd/opampsupervisor/metadata.yaml)).

### No binary updates

The supervisor can manage configuration, health, restart and component inventory, but it cannot replace the collector
binary. The configuration accepts `accepts_packages`, but the package manager in the supervisor returns "unsupported" at
run time ([packages.go](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.162.0/cmd/opampsupervisor/supervisor/packages.go)).
The work is tracked in issue [#47272](https://github.com/open-telemetry/opentelemetry-collector-contrib/issues/47272).
That is why `myriarch-updater` exists. The upstream draft for the future feature offers exactly one package with an empty
name, a top-level type, a version such as `v0.135.0`, an archive URL, a SHA-256 content hash and a signature made of a
certificate and a signature value; the cosign identity is the issuer `https://token.actions.githubusercontent.com` and a
subject pattern for the `base-release.yaml` workflow. myriarchd follows the shape of that draft, but sends a Sigstore
bundle as signature because the releases no longer publish a separate certificate and signature (see below).

### Configuration keys that matter

| Area | Keys and notes |
|---|---|
| Server | `server.endpoint` (`ws`, `wss`, `http`, `https`), `server.headers` (values can use `${env:VAR}`, so a token comes from the service environment), `server.tls` (CA file, client certificate, and so on). |
| Capabilities | `accepts_remote_config` and `reports_available_components` default to `false` and must be enabled. Also `accepts_restart_command`, `reports_effective_config`, `reports_health`, `reports_heartbeat`. `accepts_opamp_connection_settings` should stay off (see below). `reports_remote_config` is deprecated. |
| Agent | `agent.executable` (required, read when the supervisor starts), `config_apply_timeout`, `bootstrap_timeout`, `validate_config` (runs `validate` before applying), `automatic_config_rollback` (keeps the last working remote config), `startup_fallback_configs`, `passthrough_logs` (collector logs to stdout instead of a file in the storage directory), `instance_id` (only used when no state file exists), `description.non_identifying_attributes`, `config_files`, `args`, `env`. |
| Storage | `storage.directory` holds `persistent_state.yaml`, the merged `effective.yaml` and the last-config files. |
| Health | `healthcheck.endpoint` serves `/health` and only checks the supervisor, not the collector. |
| Packages | `agent.package` (binary name, verifier type) exists as scaffolding; only an empty verifier type is valid today. |

The specification still describes `run_as` and `access_dirs`; the code has neither.

### Remote configuration handling

- The supervisor merges, in order, its own telemetry config, local `config_files`, the injected extension config and the
  remote config, so the remote config wins over local files. Remote `config_map` entries are merged sorted by key, with
  the empty key last.
- An empty `config_map` with `accepts_remote_config: true` activates a no-op pipeline: the collector is not started and
  `APPLIED` is reported. A server must never send an empty map.
- The status sequence: an unchanged result is `APPLIED` at once. Otherwise `APPLYING`, then after
  `config_apply_timeout` either `APPLIED` (collector healthy) or `FAILED` (timeout, or a crash while applying). With
  `automatic_config_rollback` the supervisor reports `FAILED` for the new hash and then `APPLIED` for the old hash.
- The result is written to `effective.yaml` and the collector is restarted (or reloaded with SIGHUP when configured).
- The effective configuration that the collector reports is expanded (environment references resolved), includes default
  values and shows opaque values as `[REDACTED]`. "In sync" must therefore be decided by hash and not by text comparison.
- The supervisor first reports its own effective configuration with an empty content type, then the collector reports
  its configuration as YAML.
- Connection settings offered by a server are kept only in memory, and without a certificate in the offer the supervisor
  disables TLS verification ([source](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.162.0/cmd/opampsupervisor/supervisor/supervisor.go)).
  myriarchd therefore never sends connection settings. The heartbeat interval is 30 seconds by default.

### Identity and reported attributes

- `persistent_state.yaml` in the storage directory holds `instance_id` and the last remote config status. It is created at
  the first start (a UUIDv7, or `agent.instance_id`). `myriarch-updater` reads `instance_id` to link itself to the collector.
- At bootstrap the supervisor starts the collector briefly with a no-op configuration and the injected OpAMP extension to
  learn the agent description and the available components, then overlays `agent.description`.
- The extension reports identifying `service.name` (the binary name, for example `otelcol-contrib`), `service.version`
  (without a leading `v`, for example `0.162.0`) and `service.instance.id`; and non-identifying `os.type`, `host.arch`,
  `host.name` and `os.description`. Available components are a map from component kind to type, each with the Go module
  and version in metadata, and a hash. Source: [opampextension](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/v0.162.0/extension/opampextension).
- Observed behaviour with a real supervisor: the first message has sequence 0 with a description and capabilities;
  restart arrives as a separate command message; health goes through `StatusStopping`, `StatusStarting` and `StatusOK`;
  heartbeats are empty messages.

### Windows and packaging

- On Windows the supervisor runs as a service ([main_windows.go](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.162.0/cmd/opampsupervisor/main_windows.go))
  and falls back to console mode.
- The supervisor has its own release with bare binaries (Linux amd64, arm64 and ppc64le, macOS, Windows `.exe`), deb and
  rpm packages, a Windows MSI for 64-bit Intel only (service `opampsupervisor`, start type manual, no ready-made
  configuration) and a container image without any collector.

## Release assets and signatures

Source repository: [open-telemetry/opentelemetry-collector-releases](https://github.com/open-telemetry/opentelemetry-collector-releases);
tag and release process in [docs/release.md](https://github.com/open-telemetry/opentelemetry-collector-releases/blob/main/docs/release.md).

### Tags and releases

- One tag `v0.X.Y` produces one GitHub release for all distributions (`otelcol`, `otelcol-contrib`, `otelcol-otlp`,
  `otelcol-prometheus`, `otelcol-k8s`, `otelcol-ebpf-profiler`).
- The supervisor has its own release under the tag `cmd/opampsupervisor/v0.X.Y` (the builder likewise under
  `cmd/builder/`). It is not marked as the latest release.
- Other tag forms exist: patch releases of the repository and nightly tags such as `v0.163.0-nightly.202610060221`,
  which have no public release. Filters used by myriarchd: `^v0\.\d+\.\d+$` and `^cmd/opampsupervisor/v0\.\d+\.\d+$`.
- Asset names carry the version without `v`. Downloads redirect to a release asset host.
- The repository warns that tags may change to fix release problems, and the release tooling replaces existing
  artifacts. Treat releases as mutable: pin the SHA-256 at the first verification.

### Asset names and platforms

Archive name pattern for the contrib distribution: `otelcol-contrib_<version>_<os>_<arch>.tar.gz`, defined by the
[goreleaser configuration](https://github.com/open-telemetry/opentelemetry-collector-releases/blob/main/distributions/otelcol-contrib/.goreleaser.yaml).
Windows archives are `.tar.gz` as well (not `.zip`). The archive contains the executable and a README at its root, with no
subdirectory. Platforms of the archives at 0.162.0: aix_ppc64, darwin_amd64, darwin_arm64, linux_386, linux_amd64,
linux_arm64, linux_armv7, linux_ppc64le, linux_riscv64, linux_s390x, windows_386, windows_amd64, windows_arm64. Linux has
deb and rpm packages and Windows has MSI installers. Supervisor binaries are named
`opampsupervisor_<version>_<os>_<arch>` (with `.exe` on Windows) and are not archived; see its
[goreleaser configuration](https://github.com/open-telemetry/opentelemetry-collector-releases/blob/main/cmd/opampsupervisor/.goreleaser.yaml).
A linux_amd64 contrib archive is about 112 MB (about 407 MB unpacked), which matters for fleet-wide updates.

### Checksums

Since release 0.158.0 there is one checksum file per asset, `<asset>.sha256`, containing exactly 64 hex characters with no
file name and no trailing newline. It equals the `digest` field of the GitHub API for that asset. Releases up to 0.157.0
used shared checksum lists plus detached signatures and certificates. Reason and date are in the repository's
[CHANGELOG](https://github.com/open-telemetry/opentelemetry-collector-releases/blob/main/CHANGELOG.md).

### Signatures

- Every asset, including the checksum files, is signed keyless with cosign. The signature is a Sigstore bundle
  `<asset>.sigstore.json`; there are no `.sig` or `.pem` files from 0.158.0 on.
- A bundle (media type `application/vnd.dev.sigstore.bundle.v0.3+json`) contains a message signature with the SHA-256
  digest, a short-lived Fulcio certificate, a transparency log entry with an inclusion proof, and an RFC 3161 timestamp.
- Certificate identity (the subject alternative name) and issuer:

| Artifact | Identity | Issuer |
|---|---|---|
| collector archives | `https://github.com/open-telemetry/opentelemetry-collector-releases/.github/workflows/base-release.yaml@refs/tags/v<version>` | `https://token.actions.githubusercontent.com` |
| supervisor binaries | `https://github.com/open-telemetry/opentelemetry-collector-releases/.github/workflows/base-binary-release.yaml@refs/tags/v<version>` | `https://token.actions.githubusercontent.com` |

  The supervisor identity ends in `refs/tags/v<version>`, not in `refs/tags/cmd/opampsupervisor/v<version>`, because the
  workflow runs on the plain version tag.

- Anchored patterns used by myriarchd and the updater:
  `^https://github\.com/open-telemetry/opentelemetry-collector-releases/\.github/workflows/base-release\.yaml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$`
  for collector archives, and the same with `base-(binary-)?release` when supervisor binaries are accepted as well. Besides
  the pattern, verification requires that the tag matches the version being installed.
- Verification by hand with cosign 3: `cosign verify-blob --bundle <asset>.sigstore.json --certificate-identity <identity>
  --certificate-oidc-issuer <issuer> <asset>`. The project documents only the image verification command; the blob
  command is derived and was confirmed to work. For a network-free check add `--trusted-root trusted_root.json`.
- Verifying the signature of the `.sha256` file plus checking the digest is equivalent to verifying the archive
  signature and avoids reading a 112 MB file twice on the server.
- Legacy detached signatures (before 0.158.0) cannot be verified offline because they contain no transparency log entry
  and no timestamp. myriarchd supports only bundles.

### Packages and what they install

- The contrib MSI installs a Windows service named `otelcol-contrib` that starts immediately. The supervisor MSI installs
  a service `opampsupervisor` with manual start. Both would run a collector next to each other; the MSI of different
  distributions even replaces each other through a shared upgrade code. See the
  [collector installer template](https://github.com/open-telemetry/opentelemetry-collector-releases/blob/main/cmd/msi-generator/windows-installer.wxs.tmpl)
  and the [supervisor installer](https://github.com/open-telemetry/opentelemetry-collector-releases/blob/main/cmd/opampsupervisor/windows-installer.wxs).
- The contrib deb ships a default configuration and its post-install script enables and starts the `otelcol-contrib`
  service at once. The supervisor deb enables its service only when its configuration file exists. Since 0.157.0 the rpm
  packages no longer start or enable services on install. Therefore myriarchd's installers use the `.tar.gz` archives.

### Container images

- Collector images: `otel/opentelemetry-collector-contrib:<version>` and the same name under
  `ghcr.io/open-telemetry/opentelemetry-collector-releases/`. Multi-architecture lists exist for Linux; Windows images have
  separate tags. The image is `FROM scratch` with the executable and a default configuration.
- The supervisor image has its own name (`opentelemetry-collector-opampsupervisor`), is based on a small Linux
  distribution, and contains only the supervisor binary. There is no official image that combines supervisor and
  collector: operators build one (copy the collector binary from the collector image into the supervisor image), and
  should keep both on the same version.
- Image signatures are cosign references. On 2026-10-06 the documented verification passed for several contrib tags but
  failed for the multi-architecture list of 0.162.0 (re-signed manually after a manifest repair), and supervisor images
  carried no signatures. Do not assume that image signature checks are reliable for every tag.

## Kubernetes options

Sources: the OpenTelemetry Operator [v0.160.0](https://github.com/open-telemetry/opentelemetry-operator/tree/v0.160.0)
and its [OpAMP Bridge](https://github.com/open-telemetry/opentelemetry-operator/tree/v0.160.0/cmd/operator-opamp-bridge).

### Option a: supervisor and collector in one pod

- No official chart or operator mode runs the supervisor. The Helm chart for the collector always starts the collector
  with its own config argument, and the operator never injects the OpAMP extension or a supervisor. A custom image and
  small manifests are needed.
- Mapping of volumes: supervisor config from a ConfigMap, token and certificates from a Secret, and the storage
  directory on a host path (one agent per node) or a persistent volume (stateful workloads). With an ephemeral directory
  every pod start produces a new instance UID unless `agent.instance_id` is fixed.
- Set `passthrough_logs: true`. The collector inherits the container environment, so downward API variables work.
- The supervisor survives collector crashes by restarting the collector inside the container and reports it as
  unhealthy; Kubernetes does not see a container restart.
- Updating the version means changing the image, which restarts the pod.

### Option b: Operator with the OpAMP Bridge

- The bridge is a separate deployment created from an `OpAMPBridge` resource (or a Helm value). It speaks OpAMP as one
  agent per bridge process, not per collector. Its instance UID is new at every start, so a server must recognize it by
  an attribute such as a cluster name.
- It reports as effective configuration one entry per managed collector custom resource, keyed `<namespace>/<name>`, with
  the complete resource as YAML. Selection is by labels (`opentelemetry.io/opamp-reporting` for reporting only,
  `opentelemetry.io/opamp-managed` for management).
- Remote configuration uses the same format. For each key the bridge creates or updates the custom resource; an update
  replaces the entire `spec` and keeps only the metadata. A key that was applied earlier and is missing from a later
  message causes the bridge to delete the resource. If one entry fails, the overall status is `FAILED` while the others
  remain applied.
- The component allowlist checks only receivers, processors and exporters, not extensions, connectors or other spec
  fields such as image, environment or volumes. Its role needs broad rights on collector resources.
- Health is a tree of the bridge, each resource and its pods, based on pod phase. A pod in a crash loop is usually still
  in phase `Running`. Component-level health is available only through the bridge's proxy, which accepts connections from
  collectors that carry the OpAMP extension.
- A restart command triggers a rollout restart of all managed workloads. Packages, connection settings and available
  components are not implemented, although the bridge reports the capabilities it is configured with.
- A standalone mode (since operator 0.154.0) works without the operator: one client per configured workload with a stable
  identity, config stored in ConfigMaps and followed by a rollout restart.
- Changing the version through the bridge is only possible indirectly: because the whole `spec` is replaced, a server can
  set `spec.image`. Without `spec.image` the operator's default image is used, and a default image of the operator is
  the core distribution, which lacks the OpAMP extension.

### Comparison in short

Option a gives one agent per pod with full capabilities (component health, inventory, rollback, validation) and a small
blast radius, since a remote config changes only the collector configuration. Option b gives one agent per cluster, pod
phase health only, and lets the server change the whole pod specification. Deployments with many nodes per cluster
produce tens of agents under option a, which is well within the design range of one to five thousand agents.

## Collector configuration merge

The collector's configuration library merges maps recursively and replaces every other value, including lists and
`null`, with the later one ([confmap](https://github.com/open-telemetry/opentelemetry-collector/tree/main/confmap)). An
alpha feature gate, `confmap.enableMergeAppendOption`, appends lists for `service::extensions`, `service::**::receivers`
and `service::**::exporters`; it is documented as not intended to be stabilized as it is. Keys are interpreted as paths
with `::`, so keys that contain `::` are ambiguous. The supervisor adds two special cases when it merges: it joins and
de-duplicates `service::extensions`, and it merges `service::telemetry::resource::attributes` by name. Components were
renamed to snake case (for example `host_metrics`, `file_log`, `otlp_grpc`); old names remain as deprecated aliases that
log a warning. `otelcol-contrib components` lists only the new names while the OpAMP extension reports both. `otelcol-contrib
validate` builds the pipelines and therefore fails for components that exist only on another operating system.

## OpAMP library behaviour

Source: [opamp-go v0.25.0](https://github.com/open-telemetry/opamp-go/tree/v0.25.0) and the
[OpAMP specification](https://github.com/open-telemetry/opamp-spec/blob/main/specification.md).

Server side:

- The server library can run its own HTTP server or be attached to an existing one. Attaching returns a handler and a
  connection-context hook; the hook must be set on the HTTP server, or the plain HTTP transport fails.
- The connecting callback runs for every WebSocket handshake and every HTTP poll; a rejection carries a status code and
  headers, and clients honour `Retry-After` on 429 and 503.
- For WebSocket the context of the connected callback is already cancelled, and the message callback gets a background
  context. The send call ignores its context and a write deadline on the socket does not help because the WebSocket
  library resets it per frame. A plain HTTP connection can only answer the request it is handling and cannot push.
- There is no server-side WebSocket ping; liveness depends on agent heartbeats (30 seconds by default).
- The maximum message size defaults to 64 MiB.

Client side:

- The capabilities, the agent description, the initial health (when health is reported) and the available components
  (when they are reported) must be set before start. Package support requires a state provider together with both package
  capabilities.
- The first message holds the full state (with the components as a hash only). The WebSocket client sends the full
  state only for its first connection; after a reconnect the next message can be almost empty, so a server must ask for
  the full state.
- A restart command is honoured only with the matching capability, and a message containing a command ignores all its
  other fields.
- In this version, setting a TLS configuration forces the secure scheme, even for a `ws://` URL; clients for plain
  endpoints must not set it.
- Package sync: the offered set is complete, local packages that are not offered are deleted, a status goes through
  installing and downloading to installed or failed, and the install callback runs synchronously and is responsible for
  verifying the hash. Installed is reported as soon as that callback returns.
- Protocol details (messages, flags, capability bits, status values) are defined by the
  [protobuf file](https://github.com/open-telemetry/opamp-spec/blob/main/proto/opamp.proto) of the specification.
  Downloadable files can carry headers, which lets a server hand out a short-lived download token.

## Verifying Sigstore bundles in Go

Source: [sigstore-go v1.3.0](https://github.com/sigstore/sigstore-go/tree/v1.3.0) and its
[verification guide](https://github.com/sigstore/sigstore-go/blob/v1.3.0/docs/verification.md). The library is stable
for production use, but without key management service support and without container image verification.

How myriarchd and the updater use it, in prose:

- Load the trusted root from a file or from the Sigstore TUF repository, create a verifier that requires at least one
  signed certificate timestamp, one transparency log entry and one observer timestamp, load the bundle from JSON, and
  verify it against a policy made of the artifact digest and a certificate identity (issuer plus a subject pattern).
- Verifying with the digest means the archive does not need to be read again. The digest comes from the `.sha256` file, and
  the host computes the digest of what it actually downloaded.
- Pattern matching is not anchored unless the pattern is; write patterns with `^` and `$`.
- Certificate extensions such as source repository and ref can be pinned in addition to the subject.
- Detached signature and certificate pairs are not a supported input; building a bundle from them needs a transparency
  log entry looked up online, so only bundles are accepted.

Trusted root, air-gapped use ([options in the TUF package](https://github.com/sigstore/sigstore-go/blob/v1.3.0/pkg/tuf/options.go)):

- By default the library uses an embedded TUF root, the public repository at `tuf-repo-cdn.sigstore.dev` and a cache
  under the user's home directory. A service account often has no usable home, so a cache path must be set.
- The library itself recommends providing the trust root file directly for air-gapped environments instead of tuning TUF.
  A `trusted_root.json` was observed to hold two Rekor logs, two certificate transparency logs, two Fulcio CA entries and
  a timestamp authority, about 6 KB. It must be refreshed after a key rotation, which is why an air-gapped setup needs a
  process that exports it on a connected host and imports it. A TUF mirror is possible as well.
- Observed results with real 0.162.0 assets: online and offline verification both pass; a wrong workflow, a wrong tag, a
  wrong version or a wrong digest all fail; TUF mode without network and cache fails with a TUF refresh error.

## Release tracking and rate limits

Releases are read through the [GitHub REST API](https://docs.github.com/en/rest/releases/releases) with conditional
requests ([ETags](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api#use-conditional-requests-if-appropriate)).
Unauthenticated requests are limited to 60 per hour and a 304 answer still counts, see the
[rate limit documentation](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api). Release
changelogs are in the `CHANGELOG.md` files of the [core](https://github.com/open-telemetry/opentelemetry-collector/blob/main/CHANGELOG.md)
and [contrib](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/CHANGELOG.md) repositories.
Since 0.162.0 the contrib changelog uses combined headings such as `## v1.1.0/v0.162.0`.

## PostgreSQL facts

- `LISTEN`/`NOTIFY`: a payload is at most 8,000 bytes; a notification sent in a transaction is delivered at commit;
  delivery is best effort; the listener needs its own connection ([documentation](https://www.postgresql.org/docs/current/sql-notify.html)).
- Session level advisory locks (`pg_try_advisory_lock`) are released when the session ends
  ([documentation](https://www.postgresql.org/docs/current/functions-admin.html#FUNCTIONS-ADVISORY-LOCKS)). A connection pooler
  in transaction mode breaks both mechanisms.
- Embedded PostgreSQL for development and tests ([embedded-postgres](https://github.com/fergusstrange/embedded-postgres)):
  the default port 5432 collides when test packages run in parallel; starting the server clears its runtime path, which
  by default contains the downloaded binaries; extraction can race between processes; servers can be orphaned after a
  panic or timeout; locale and time zone are inherited from the shell; the server does not run as root. A cold start
  needs about ten seconds (download and unpack), a warm start under a second.

## Upstream status (as of 2026-10-08)

Checked on 2026-10-08 through the public GitHub issue pages and API.

| Item | Status |
|---|---|
| [opentelemetry-collector-contrib #33947](https://github.com/open-telemetry/opentelemetry-collector-contrib/issues/33947), "Implement collector executable updates" | Closed on 2025-04-04 as not planned. The supervisor README still links it in the feature table for "Updates the Collector binary". |
| [opentelemetry-collector-contrib #47272](https://github.com/open-telemetry/opentelemetry-collector-contrib/issues/47272), "Implement Package Upgrades in the Supervisor" | Open, last updated 2026-09-01. It is a staged plan of seven changes: capabilities and docs, configuration surface and verification interfaces, Cosign verification, tar.gz extraction, package manager, binary update with rollback, end-to-end tests. |
| [#50685](https://github.com/open-telemetry/opentelemetry-collector-contrib/issues/50685), "Package Upgrades - Cosign verifier" | Open, last updated 2026-09-05. A verifier for Cosign keyless signing, the mechanism of the official releases, is in progress. |
| [#47298](https://github.com/open-telemetry/opentelemetry-collector-contrib/issues/47298) (initial skeleton), [#48791](https://github.com/open-telemetry/opentelemetry-collector-contrib/issues/48791) (configuration and interfaces), [#49766](https://github.com/open-telemetry/opentelemetry-collector-contrib/issues/49766) (tar.gz extractor) | Closed as completed. |
| Latest contrib release | [v0.162.0](https://github.com/open-telemetry/opentelemetry-collector-contrib/releases/tag/v0.162.0), published 2026-09-29. |

Consequence for the design: the supervisor still has no working binary update path, so `myriarch-updater` remains
necessary. Once upstream finishes the work, a supervisor-native executor could reuse the same package offers, because
the offers already follow the draft.
