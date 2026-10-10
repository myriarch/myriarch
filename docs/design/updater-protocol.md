---
title: "Updater protocol"
weight: 30
description: "The contract between myriarchd and myriarch-updater: identity, package offers, update health and host-side install behaviour."
---

The OpAMP Supervisor cannot replace the collector binary (see [upstream-notes.md](upstream-notes.md#supervisor-limits)).
`myriarch-updater` closes that gap. It runs next to the supervisor on the same host, receives package offers from myriarchd
over OpAMP, installs the collector binary and reports what it is doing. This document is the contract between myriarchd
and `myriarch-updater`: the identity attributes, the update health component, the phases and steps the server consumes,
the host-side install behaviour and the rule that picks the download base URL.

How a choice of version leads to an offer is described in [version-selection.md](version-selection.md). How the updater
runs as the main process of a container is described in [container-mode.md](container-mode.md).

## Roles

- myriarchd offers one package to the updater: the collector archive for the platform of the host. It first fetches the
  archive into its artifact cache and verifies checksum and signature there ([artifact cache](opamp-server.md#artifact-cache)).
- The updater downloads from myriarchd, verifies again on the host, swaps the binary, restarts the supervisor, checks
  health and rolls back on failure. It never trusts the server alone: the host policy (allowed URLs, minimum version,
  downgrade rule, signature identity) is decided in the updater configuration.
- The collector itself reports the new version. The server treats a target as finished only after that report and a
  healthy wait period, never because the updater said `INSTALLED`.

## Identity and attributes

myriarchd classifies an agent as an updater when its identifying attribute `service.name` equals `myriarch-updater`. A
collector (reporting the capability `AcceptsRemoteConfig`) is classified as `collector`, anything else as `unknown`.

The updater registers like any agent: its own instance UID is generated once (UUIDv7) and stored in its state
directory, and the server may replace it through `AgentIdentification.new_instance_uid`. It connects with the same
enrollment token as the supervisor.

Non-identifying attributes of the updater:

| Attribute | Value | Purpose |
|---|---|---|
| `os.type`, `host.arch`, `host.name` | host facts | Selects the artifact platform and ties the updater to its collector. |
| `dev.myriarch.collector.instance_uid` | UUID string read from `persistent_state.yaml` (key `instance_id`) in the supervisor storage directory | Links the updater to the collector it manages. The file is re-read about once a minute; it does not exist before the supervisor's first start, so the attribute can be absent at first and is added later. |
| `dev.myriarch.updater.supervisor_service` | Name of the supervisor service | Diagnostics. |
| `dev.myriarch.updater.install_method` | `binary` (a service on the host) or `container` | Display and diagnostics. |
| `dev.myriarch.updater.server_endpoint` | `scheme://host:port` of the configured OpAMP endpoint: no path, no credentials, port always explicit (for example `ws://fleet.example.com:8080`) | Lets the server choose the download base ([below](#download-base-urls)). |
| `dev.myriarch.updater.image_collector_version` | Container mode only: the collector version baked into the image, for example `0.161.0` | Shows the seed and its precedence. |

The simulator `myriarch-sim` reports `server_endpoint` (from its `--server` flag) and `install_method=binary`.

Linking rule. The server links an updater to a collector when the updater's attribute names the collector's instance
UID, and refuses the link (`domain.UpdaterMismatch`) unless both registered with the same credential (the same
enrollment token or, without tokens, the same client certificate subject) and, when both report one, the same host
name (case-insensitive). An agent therefore cannot attach itself to a collector of another credential or another host.

Capabilities of the updater: reports status, accepts packages, reports package statuses, reports health and reports
heartbeat. It reports health before it starts and keeps it current.

## Package offers

The server's offer follows the draft of the upstream supervisor, with one deliberate deviation in the signature field.

| OpAMP field | Value |
|---|---|
| package name | the empty string (the only package the updater accepts) |
| package type | top level |
| `version` | the target version with a leading `v`, for example `v0.162.0` |
| `file.download_url` | `<base>/api/v1/artifacts/<artifact-id>/content/<archive-file-name>` |
| `file.content_hash` | the 32 byte SHA-256 of the archive |
| `file.signature` | the raw Sigstore bundle JSON of the archive (upstream drafts a `"<b64 cert> <b64 sig>"` pair, which no longer matches the release format) |
| `file.headers` | `Authorization: Bearer <download token>` |

Rules that follow from the OpAMP client library and are part of the contract:

- An offer is always the complete set of packages. A package missing from an offer is forgotten by the client. For that
  reason the server never sends an empty set, and the updater's `DeletePackage` only forgets state: it never removes
  anything from disk.
- The updater accepts only the empty package name with the top-level type. Everything else fails fast with
  `INSTALL_FAILED` and an "unsupported package" message before any download.
- The client library passes only the download URL to the install callback, so the updater remembers the offered
  version per URL when the offer arrives.
- The client library reports `INSTALLED` as soon as the install callback returns. Therefore the whole update, including
  the health check and a possible rollback, runs inside that callback. The library does not check the content hash
  itself; the callback does.
- If the offer carries the content hash of the active version, the download is skipped and `INSTALLED` is reported.
- A failed install is remembered by content hash. The same broken package is not retried before `failedRetryBackoff`
  (default 10 minutes) has passed. This stops reconnecting updaters from restarting the supervisor on every re-offer.
- A restart of the updater during an install turns the stale status into `INSTALL_FAILED` so that the server does not
  see it stuck.
- Artifact format: the archive is a `.tar.gz` for every operating system, including Windows.

Download tokens. The token is a signed value bound to one artifact and one updater agent. myriarchd mints it when the offer
is sent. It is valid only while that agent still has an offer referencing the artifact and, when
`artifacts.downloadTokenTtl` is positive (default 24 hours), while it is younger than the TTL. An offer that is still
incomplete when its token has used half of the TTL is sent again with a fresh token.

## Update health component

The updater reports the steps of an update attempt as state, not as events. A lost intermediate report does no harm
because each report carries the full list of steps. The state sits inside the updater's own health, in the component
map under the key `dev.myriarch.update`.

```text
"dev.myriarch.update": ComponentHealth {
  status:                 <phase>   # a running step name, or a final outcome (see below)
  healthy:                false only when the attempt failed AND the rollback failed or was impossible
  last_error:             error text of the attempt (failed or rolled_back), at most 1000 characters,
                          without secrets or configuration content
  start_time_unix_nano:   start of the attempt
  status_time_unix_nano:  time of the last phase change
  component_health_map: {
    "version":           {status: "v0.162.0", healthy: true}   # offered version, with "v"
    "from":              {status: "v0.161.0", healthy: true}   # version active before the attempt, "" if none
    "step:downloading":  {status: running|done|failed|skipped, start/status time, last_error}
    "step:verifying":    {...}
    "step:installing":   {...}
    "step:restarting":   {...}
    "step:health_check": {...}
    "step:rolling_back": {...}   # only when a rollback happened
  }
}
```

Rules:

- An attempt begins when the updater starts handling an offer, which includes the policy check. The component always
  shows the latest attempt, also after it ended, until the next one begins. The updater persists it in its state
  directory and restores it after a restart; an attempt cut off by a restart is closed as failed ("interrupted").
- A step that has not begun is absent while the attempt runs. When the attempt ends, steps that never ran are reported
  as `skipped` (except `rolling_back`).
- An error before the swap yields the final status `failed`, later steps are `skipped` and no rollback happens. An error
  after the swap followed by a successful rollback yields `rolled_back`. If the rollback fails as well, the status is
  `failed` and `healthy` is `false`.
- The top-level health of the updater keeps its earlier meaning: status `StatusUpdating` during an attempt, and the
  `last_error` "last update failed: ..." after a failed one.
- Constants (defined in `internal/brand`, values are binding): component key `dev.myriarch.update`, step prefix
  `step:`, children `version` and `from`.

How myriarchd turns this into the phases of `TargetProgress` is specified in
[version-selection.md](version-selection.md#target-progress). Matching an attempt to a target uses the offered version
and a two minute tolerance on the start time.

## Phases and steps as the server consumes them

| Step | What it covers on the host |
|---|---|
| `offered` | Server side only: the executor handed the offer over and the host has not started yet. |
| `downloading` | Streaming the archive into a spool file in the state directory, hashing it while it is written. |
| `verifying` | Comparing the SHA-256 with the offered content hash, verifying the signature, unpacking the binary and the preflight run of the new binary. |
| `installing` | Writing the install marker and the journal, stopping the supervisor, activating the new version directory. |
| `restarting` | Starting the supervisor again. |
| `health_check` | Waiting for the host and the collector to be healthy. |
| `rolling_back` | Reactivating the previous version, restarting and checking health. Present only after a failure. |
| `health_wait` | Server side only: the collector reports the target version and the policy's `healthWait` runs. |

The phases `succeeded`, `failed`, `rolled_back` and `cancelled` are final outcomes. The updater itself reports
`succeeded`, `failed` and `rolled_back`; the engine adds `cancelled`.

## Host-side install behaviour

The order is deliberate. Nothing on the host changes before the downloaded archive has passed the checksum and the
signature checks and the new binary has passed the preflight. The supervisor is stopped only for the swap, and every
failure after the stop is rolled back.

1. Policy check ([anti-downgrade](#anti-downgrade-policy)) and a check that the supervisor service exists, before any
   download.
2. Download into the spool directory. Size is limited (default 1 GiB), a stalled read fails after the idle timeout
   (default 60 seconds), and there is no overall timeout because archives are large.
3. Compare the SHA-256 of the spool file with the offered `content_hash`. A missing or wrong hash fails the attempt.
4. Verify the signature ([below](#signature-verification)).
5. Unpack only the collector binary from the archive root into a staging directory, preventing path traversal and
   bounding the size, and make it executable.
6. Preflight: the new binary must run and its `--version` output must name the expected version. When
   `preflightValidate` is on and the supervisor has written an effective configuration, the binary also runs
   `validate` against that file, with the environment of the supervisor service where it can be determined. Because the
   effective configuration is written by the less privileged supervisor account, the check runs under that account
   where privileges can be dropped, and otherwise only for configurations without non-environment providers.
7. If the offered version is already active, the update succeeds only when the binary on disk equals the one just
   verified. It never overwrites a running version.
8. Write the install marker, move the staging directory to `versions/<version>`, write the journal, stop the supervisor
   service, activate the version, start the supervisor, wait for health.
9. On any error after the stop, roll back ([below](#rollback-and-recovery)). On success, prune old versions beyond
   `keepVersions` (default 2) and clear the journal.

Past the point where the supervisor is stopped, cancellation of the surrounding context is ignored until the host is
consistent again. The journal covers hard kills.

### Layout

```text
<installDir>/versions/<version>/<binary>
<installDir>/current -> versions/<version>        (Linux and containers: activation "symlink", replaced atomically)
<installDir>/bin/<binary> (+ .previous)           (Windows: activation "copy")
```

Each version directory holds an install marker with the version, the SHA-256 of archive and binary, the previously
active version, the install time and the source (`package` for downloaded and verified, `image` for a container seed).
The supervisor configuration points at the stable path (`current/<binary>` or `bin/<binary>`). The supervisor reads
`agent.executable` only when it starts and detects version and components during bootstrap, so the supervisor service
is restarted, not only the collector. On Windows a running executable is locked, so the service is stopped before the
copy.

### Health check

Healthy means, in this order: the supervisor service runs, the supervisor health endpoint (if configured) answers 200,
and the collector health endpoint (if configured, the `health_check` extension) answers 200. `healthSuccesses`
consecutive successes (default 3) at `healthInterval` (default 5 seconds) within `healthTimeout` (default 120 seconds)
count as healthy.

### Rollback and recovery

On failure after the stop the updater reactivates the previous version, restarts the service and runs the health check
again. The reported error then reads "update to v... failed: ...; rolled back to v...". If the rollback fails too, the
error carries both causes, the journal is kept, and the next start of the updater tries again. Without a previous
version there is nothing to roll back to.

At startup the updater reads the journal and repairs a host that was interrupted in the middle of an update: it trusts
the binary bytes on disk rather than a version file, finishes or reverts the swap, and makes sure the service runs and
is healthy. A cancelled recovery keeps the journal.

### Anti-downgrade policy

Decided on the host before a download:

- `update.minimumVersion` is a floor. Offers below it are refused whatever the server says.
- Unless `update.allowDowngrade` is set, an offer older than the active version is refused, except the one-step return
  to the version recorded as `previousVersion` in the install marker. An image seed in container mode carries the
  previously active version in the same field.
- A refusal is an `INSTALL_FAILED` with the reason.

### Signature verification

- A signature that starts with `{` is a Sigstore bundle. It is verified with the artifact digest (the SHA-256 computed
  from the download), the Fulcio certificate identity and the OIDC issuer.
- A signature in the legacy `"<cert> <sig>"` form is refused with "legacy signature format not supported (needs online
  Rekor)". An empty signature is refused when `requireSignature` is true (the default).
- The identity pattern must be anchored: the configuration check refuses `update.certificateIdentityRegexp` (and
  `verify-bundle --identity-regexp`) unless the pattern starts with `^` and ends with `$` at its top level, because an
  unanchored pattern also accepts identities that merely contain the expected text. It matches the release workflow of the upstream collector releases for plain
  `vX.Y.Z` tags, with the issuer `https://token.actions.githubusercontent.com`. Since the pattern accepts any release
  tag, the updater additionally requires the certificate identity to end with `@refs/tags/v<offered version>`. A valid
  signature of another release therefore cannot be replayed for this version.
- At start the updater warns when `server.tokenFile` is writable by group or others, readable by others, or owned by an
  account other than root and its own (and when an inline `server.token` sits in such a configuration file). The
  message names the setting and the path, never the content.
- The trusted root comes from `trustedRootFile` (offline verification) or, when empty, from the public Sigstore TUF
  repository (needs network and a writable cache).
- The verifier requires a signed certificate timestamp, a transparency log entry and an observer timestamp (each at
  least one), because the signing certificates live only minutes.

## Download base URLs

Property `artifacts.agentBaseUrls` in the myriarchd configuration. It lets agents that reach myriarchd through an internal
name download from there, while agents elsewhere keep using the public URL.

- It is an allowlist of absolute `http` or `https` base URLs with no path (other than `/`), query, fragment or
  credentials. In production an `http://` entry needs `artifacts.allowInsecureAgentBaseUrls: true` and logs a warning at
  startup; the package content is verified by checksum and signature on the host in any case. In development mode the
  entries are unrestricted.
- For each package offer myriarchd picks the base per updater: the first entry whose `host:port` equals the `host:port` of
  the updater's `dev.myriarch.updater.server_endpoint` (compared case-insensitively; the default port is 80 for `http` and
  `ws`, and 443 for `https` and `wss`). If none matches, the base is `server.publicUrl`.
- The URL is `<base>/api/v1/artifacts/<id>/content/<file>`. The token is created when the offer is sent.
- `server.publicUrl` stays the address for browsers, OIDC, links and enrollment snippets.
- On the updater, `update.allowedUrlPrefixes` must contain the chosen base. The updater accepts a URL only if scheme and
  host:port equal those of an allowed prefix, the path lies below the prefix on a path segment boundary, there are no
  `..` elements and no credentials. Redirects are re-checked against the same list, limited to five hops, and a
  redirect from `https` to `http` is refused. If the list is empty, the default is the artifact path on the host of the
  configured OpAMP endpoint.

An artifact mirror that the myriarchd host can reach is the usual way to serve an air-gapped deployment: myriarchd reads
releases from `artifacts.sourceBaseUrl`, keeping the layout `<base>/<tag>/<asset>` with the `.sha256` and
`.sigstore.json` files next to each asset, and verifies them offline with a `trustedRootFile` (see
[upstream-notes.md](upstream-notes.md#release-assets-and-signatures)). Agents only ever talk to myriarchd.
