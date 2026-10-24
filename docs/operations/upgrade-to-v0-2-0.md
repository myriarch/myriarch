---
title: "Upgrade from v0.1.0 to v0.2.0"
weight: 25
description: "Upgrade notes for v0.2.0: database migrations and the rolling update, new and changed settings, the Helm chart, the installers and the deprecated Kubernetes collector files."
---

This page is for people who run v0.1.0 and move to v0.2.0. It gathers what the changelog says in many entries into one
order of work: what to do before the update, what the update changes, and how to check it afterwards. The complete list of
changes is the [changelog](../../CHANGELOG.md); the guide for upgrades in general (backup and restore, rolling updates in
detail) is planned for v0.3.0.

> **Available from v0.2.0.** The release candidates `v0.2.0-rc.N` behave as described here. Read the release notes of the
> version you install: they link the changelog section of that version.

## At a glance

| Area | What changes | What you do |
|---|---|---|
| Database | Two migrations, both expand only: `00006_schema_compat.sql` and `00007_agent_kind_bridge.sql`. | Back up the database first. Going back to v0.1.0 means restoring the backup. |
| Rolling update | Allowed. v0.1.0 and v0.2.0 replicas can run side by side, with one exception for OpAMP Bridge agents. | Scale to one replica for the update if a Bridge of the OpenTelemetry Operator is connected. |
| Server settings | New sections and keys: `install.*`, `ha.leaderSessionTimeout`, `opamp.staleApplyingAfter`. | Run `myriarchd config validate` with your configuration before you update. |
| Outbound connections | The server fetches the installer bundles of its own release from GitHub. | Allow the connection, point it to a mirror, or set `install.enabled: false`. |
| Helm chart | A strict values schema, a separate `image.registry`, a test Pod. The chart is also a signed OCI artifact. | Run `helm lint` or `helm template` with your values and the new chart before `helm upgrade`. |
| GitOps repository | The warning `bridge_selected`. | Narrow broad fleet selectors, or CI with `--strict` fails. |
| Hosts | The installers check the host first. The unit of the updater changes. | Run the installer of v0.2.0 again on hosts that you want to bring along. |
| Kubernetes collectors | The Kustomize base and the combined image are deprecated. | Move to the collector chart when it suits you; nothing breaks at the update. |

## Before you update

1. **Back up the PostgreSQL database** (`pg_dump` or a snapshot of the managed database). The update applies migrations at
   the start of the first replica. v0.1.0 does not check the schema and its migration library accepts a database with
   higher versions, so it would start on a migrated database without a warning. A rollback to v0.1.0 is not supported
   ([ADR-0036](../adr/0036-database-schema-compatibility-and-rollback.md)): to go back, restore the backup and start
   v0.1.0 against it.
2. **Validate your configuration** with the binary or image of v0.2.0, without starting the server:
   `myriarchd config validate --config myriarchd.yaml`. It reports a value that v0.2.0 refuses, for example `install.grantTtl` above
   `artifacts.downloadTokenTtl` ([Production checklist](production-checklist.md)).
3. **Run the Helm checks** if you install with Helm: [below](#helm-chart).
4. **Check your GitOps repository** with `myrctl validate --strict` of v0.2.0: [below](#gitops-repository).
5. **Decide on the installation from the server**, which is on by default: [below](#installation-from-the-server).

## Database migrations and the rolling update

The update applies two migrations. Both are *expand only*: they add to the schema and remove nothing, so replicas of
v0.1.0 keep working on the migrated database.

| Migration | What it does |
|---|---|
| `00006_schema_compat.sql` | Adds the table `schema_compat` with one row, the compatibility level `min_binary_schema` (it starts at 3). From v0.2.0 on, `myriarchd` compares it with its own migrations at the start. |
| `00007_agent_kind_bridge.sql` | Widens the kinds of `agents.kind` by `bridge`. |

The compare-and-set of the fleet assignments needs no migration and no new column. In a rolling update, replicas of v0.1.0
still write assignments unconditionally, so two of them can still record one fleet change twice, as before; the protection is
complete when all replicas run v0.2.0.

**Exception: OpAMP Bridge agents.** A Bridge of the OpenTelemetry Operator that was connected to v0.1.0 was treated as a
collector. v0.2.0 stores it as the kind `bridge` with its next description message, offers it remote configuration only from a
fleet with `spec.bridge.manage: true` and only in standalone mode, sends it no restart command and never makes it the target
of a version rollout. Replicas of v0.1.0 keep classifying a Bridge as a collector until all replicas run v0.2.0. If a Bridge is
connected, check that no fleet relies on configuring it, and scale `myriarchd` to one replica for the update
([Fleets](../concepts/fleets.md#bridge-agents)).

**With `database.migrateOnStart: false`**, run `myriarchd migrate` with the new version before you update the deployment.
The server refuses a schema that lacks migrations of its binary at once, and the error names `myriarchd migrate`. With the
default `true`, nothing changes for you.

**What the server refuses.** A schema that is incompatible with the binary is refused with an error that names the schema
version and the levels, whatever `database.migrateOnStart` says. A newer schema that is still compatible only logs a
warning. `myriarchd migrate --check` changes nothing: it prints the schema version, the compatibility level and a status
(`current`, `pending`, `newer` or `incompatible`), and the exit code tells it (0 ready, 3 migrations pending, 4
incompatible, 1 error). [Troubleshooting](troubleshooting.md) lists the messages.

**`myriarchd migrate` reads only the database settings.** The public URL, the login path and the secret files of other
features are no longer needed, and other sections of the configuration file are ignored. Its output changed: the line
`schema version: N` is replaced by `schema version before: N`, `schema version after: N` and `compatibility level: N`. A
script that reads the old line has to read `schema version after:`.

## Settings

### New settings

| Setting | Default | What it does |
|---|---|---|
| `install.enabled` | `true` | The installation of hosts from the server: the plan, the install grants and the installer bundles. `false` keeps the surface of v0.1.0: the routes answer 404 and nothing is fetched. |
| `install.grantTtl` | 1 hour, or `artifacts.downloadTokenTtl` if that is shorter and the setting is left out | Lifetime of the install grants. Production refuses a value above `artifacts.downloadTokenTtl`. A value that you set is checked as written. |
| `install.bundleSourceBaseUrl` | the GitHub releases of the project | Where the server fetches the installer bundles of its own release (layout `<base>/v<version>/<asset>`). Never empty; https in production. |
| `install.platforms` | `linux/amd64`, `linux/arm64`, `windows/amd64` | The platforms for which the server keeps an installer bundle. |
| `ha.leaderSessionTimeout` | `30s` (10s to 10m) | Bounds the failover after a hard failure of the leader's node. See [High availability](high-availability.md). Environment variable `MYRIARCH_HA_LEADERSESSIONTIMEOUT`. |
| `opamp.staleApplyingAfter` | `90s` (`0` turns it off) | A configuration that an agent reports as `APPLYING` for this long is offered once more on that connection. |

The generated [configuration reference](../reference/configuration.md) lists every setting with its environment variable.

### Changed behavior

| Behavior | v0.1.0 | v0.2.0 |
|---|---|---|
| Failover after a hard failure of the leader's node | PostgreSQL held the advisory lock until it noticed the dead session, which can take hours; there was no leader and no rollout meanwhile. | A second replica becomes leader within `ha.leaderSessionTimeout`. The lock session and the event bus listener use a small pool of their own: a database with a tight `max_connections` sees up to two more connections per replica. |
| An agent stuck in `APPLYING` | The target waited for the `applyTimeout` of its rollout policy (10 minutes for configuration by default). | The server offers the configuration once more after `opamp.staleApplyingAfter`. A supervisor answers with `APPLIED` and does not restart the collector. Keep a value above the `config_apply_timeout` of your supervisors (30 seconds in the shipped templates). |
| Health wait on the rollout page | The page did not say how long was left. | It shows the remaining time. The default `healthWait` is 60 seconds ([Rollouts](../concepts/rollouts.md#health-wait)). |
| Start and migrations | `serve`, `dev` and `migrate` did not share one schema step. | They do; the log line `database schema ready` names the schema version, the compatibility level and the schema level. |
| `server.shutdownTimeout` and `terminationGracePeriodSeconds` | Not related to the failover. | Size them as [High availability](high-availability.md) describes; the worst case is 90 seconds with the defaults, and the chart default of 60 covers a normal shutdown. |

## Installation from the server

With the default `install.enabled: true`, v0.2.0 changes three things that you should decide on before the update:

- **A new outbound connection.** Every replica of a server that runs a release (not a build from a Git checkout) fetches
  `checksums.txt`, its Sigstore bundle and the updater archives of its own release for the platforms of `install.platforms`
  from `install.bundleSourceBaseUrl`, 15 seconds after the start and again when somebody renders the install commands or asks
  for a bundle. The connection goes through `outbound.allowedCidrs`, `artifacts.proxyUrl` and
  `artifacts.allowedRedirectHosts`. A server that cannot reach the source logs a warning and shows the bundles as `failed`;
  nothing else is affected, but the install commands cannot be rendered until it can. In an air gap, point
  `install.bundleSourceBaseUrl` to an internal mirror, or set `install.enabled: false`.
- **Disk.** The bundles take about 25 MB of `artifacts.dir` for each release and replica, outside
  `artifacts.maxCacheBytes`. The archives of an earlier version are removed after a day.
- **Enrollment tokens are install credentials.** An enrollment token now also authorizes `POST /api/v1/install/plan` and
  `GET /api/v1/install/bundles/{filename}`, which need no session, also where `opamp.requireEnrollmentToken` is off, and
  through the install grants of a plan the download of collector and supervisor with their checksums and signatures. Its
  holder can make the server prepare every release that the frame of its fleet allows. Treat the tokens as fleet
  credentials: one token for each channel or region, and revoke one that may have leaked, which stops its grants at once.
  Set `install.enabled: false` if the hosts of your sites are installed on the manual path only.

A new host that is installed from the server joins at the effective target version of its fleet. The plan waits (503
`preparing`) until the rollout planner has created the rollout for a new target, so a host cannot take a new version past the
waves and approvals. The controls are in the [security model](../security/model.md#host-installation-from-the-server), the
design in [Server behaviour](../design/opamp-server.md#installation-from-the-server).

The manual path of v0.1.0 still works: download the updater archive of a release, verify it, and run the installer with the
OpAMP endpoint, the token, the collector version and a verifier ([Connect a host](../tasks/connect-a-host.md)). From v0.2.0
the guide also describes the one-command installation, in which the server renders the command that a person copies to a host.

## Helm chart

The server chart is also published as a signed OCI artifact, `oci://ghcr.io/myriarch/charts/myriarch`, with the version of the
release; the collector chart is `oci://ghcr.io/myriarch/charts/myriarch-collector` ([Install with Helm](install-helm.md#install-the-signed-oci-chart)).
The chart of v0.1.0 is installed from a checkout of the tag, and you can keep doing that.

What the chart of v0.2.0 does differently:

- **Strict values schema.** `helm install`, `upgrade`, `template` and `lint` refuse a key that the chart does not know (also
  inside `config`), a value of the wrong type or form (a string `"false"` where a boolean is needed, a duration written as an
  integer such as `config.server.shutdownTimeout: 30`, or with a unit that Go does not know such as `30d`, a digest without
  `sha256:`, a registry with a scheme) and a secret as a value. The message names the path of the value
  ([The values schema](install-helm.md#the-values-schema)).
- **The registry of the image is a value of its own.** `image.registry` (default `ghcr.io`), `image.repository` (default
  `myriarch/myriarch`) and `global.imageRegistry`. A repository that starts with a registry host, the form of v0.1.0, is used
  unchanged. A repository **without** a registry host now takes `image.registry`: if you relied on the default registry of
  your container runtime, set `image.registry: ""` or put the host into the repository
  ([Registry and repository of the image](install-helm.md#registry-and-repository-of-the-image)).
- **A test Pod.** `helm test` runs `myriarchd healthcheck` against the Service (`tests.enabled`, default `true`). Helm creates
  the Pod only while `helm test` runs. `helm template | kubectl apply` creates it as an ordinary Pod: add `--skip-tests`. With
  `networkPolicy.ingress.allowSameNamespace: false` and without `config.server.trustedProxies` and
  `config.opamp.clientCertHeader`, the NetworkPolicy gets one more source for port 8080, the labels of the test Pod; set
  `tests.enabled: false` to keep the policy of v0.1.0 ([Test the installation](install-helm.md#test-the-installation)).
- **A PrometheusRule** with 13 alerts, off by default (`prometheusRule.enabled`).
- **`podDisruptionBudget.maxUnavailable`** is a documented value.

To upgrade from the chart of v0.1.0:

```bash
# 1. Write the values of the installed release to a file (also needed if you used --set or --reuse-values).
helm get values myriarch -n myriarch -o yaml > current-values.yaml

# 2. Render the new chart with these values. Fix what the schema reports, then render again.
helm template myriarch oci://ghcr.io/myriarch/charts/myriarch --version 0.2.0 -n myriarch \
  -f current-values.yaml > /dev/null

# 3. Back up the database, then upgrade with the values file.
helm upgrade myriarch oci://ghcr.io/myriarch/charts/myriarch --version 0.2.0 -n myriarch -f current-values.yaml
```

Do not use `helm upgrade --reuse-values` for this step. It renders the chart with the values of the installed release in place
of its own defaults, and the values of v0.1.0 have neither `tests` nor `prometheusRule`: the chart renders as v0.1.0 did, and
`--set tests.enabled=true` on top of them gives a Pod without requests and limits. Use a values file, or
`--reset-then-reuse-values`. When you install from a checkout, replace the OCI reference with the path of the chart of the new
tag, and run `helm lint` in step 2. Verify the OCI chart before you install it ([Verify releases](../security/verify-releases.md#verify-the-helm-chart)).

After the upgrade, `helm test myriarch -n myriarch` checks the server.

## GitOps repository

The new field `spec.bridge.manage` of a Fleet (default `false`) opts the fleet in to configure OpAMP Bridge agents. The new
warning `bridge_selected` of `myrctl validate` and of the GitOps sync names a fleet whose selector also matches a Bridge and
that does not set the field. It triggers for an empty selector and for a selector made only of `NotIn` and `DoesNotExist`
conditions. **A CI that runs `myrctl validate --strict` fails on it**; without `--strict` the exit code stays 0.

What to do: narrow the selector with a positive condition on an attribute that a Bridge does not report, for example
`{key: os.type, operator: Exists}` in place of an empty selector that is meant to match every collector, or set
`spec.bridge.manage: true` on a fleet that is meant to configure Bridges in standalone mode. Fleets with a positive condition on
`os.type`, a `site.*` attribute or `labels.*` are not affected. A sample agent that is a Bridge is no longer rendered and does
not have to match a fleet ([Fleets](../concepts/fleets.md#bridge-agents), [GitOps schema](../reference/gitops-schema.md#fleet)).

## Hosts

The host agent `myriarch-updater` and the collectors on your hosts keep working with a v0.2.0 server; nothing forces you to
touch them. The installers of v0.2.0 differ from those of v0.1.0 in these points:

- **They check the host before they change anything.** `install-linux.sh` and `install-windows.ps1` stop with
  `conflicting_install` when a package of the upstream project would run a collector next to the one that myriarch manages
  (a service `opampsupervisor` or `otelcol-contrib` on Windows; the unit `otelcol-contrib.service` or
  `opampsupervisor.service` that is active or enabled on Linux). The installer of v0.1.0 went on and left two collectors on
  the host, and a re-run on such a host stops now as well. Nothing is removed: remove the package, then run the installer
  again.
- **More codes with a next step.** `supervisor_missing` (both installers), `language_mode_unsupported` (Windows, in a
  PowerShell language mode below `FullLanguage`, for example under App Control script enforcement) and, on Windows,
  `unsupported_platform` for ARM64 without `-Arch amd64`, now before the installer stops the service. The table "When the
  installer stops" in [Connect a host](../tasks/connect-a-host.md#when-the-installer-stops) lists the cause and the next step
  of every code.
- **The unit of the updater changes.** `myriarch-updater.service` no longer sets `NoNewPrivileges=true`, which made every update
  through the updater fail in the step `verifying` with "operation not permitted". Hosts installed with v0.1.0 get the new unit
  when the installer of v0.2.0 runs again. Until then, a drop-in with `NoNewPrivileges=false` helps; see
  [Troubleshooting](troubleshooting.md).
- **Installation from the server.** Both installers accept `--server` (Linux) or `-Server` (Windows) and a token file, and
  take the versions, the OpAMP endpoint and the artifacts from the plan of the server. On Linux, `--collector-version` is
  optional without `--server`: the installer installs the version that its release was tested with and says so. A script
  that relied on the error `option_missing` now installs that version: pass the version of your fleet explicitly, or use
  `--server`. The Windows installer still requires `-CollectorVersion` without `-Server`.

Windows hosts stay experimental in v0.2.0, and the Windows files are not Authenticode-signed
([Support levels and tested platforms](tested-platforms.md)).

## Kubernetes collectors

`deploy/k8s/collector` (with the `Dockerfile` of the combined collector image), `deploy/supervisor/k8s`,
`deploy/updater/container/updater.yaml` and `scripts/build-collector-image-crane.sh` moved to
`contrib/legacy-kubernetes-collector`. They are **deprecated**: they receive no support and will be removed once the kind probe
and the production example use the chart. The collector chart `deploy/helm/myriarch-collector` runs the unchanged upstream
images, needs no root init container and no registry of your own. [Install the collector with Helm](install-collector-helm.md#move-from-the-kustomize-base)
explains how to move. Container mode stays an explicit opt-in (`containerMode.enabled`).

## After the update

- `helm test myriarch -n myriarch`, or `myriarchd healthcheck` against the running server, answers with exit code 0.
- `myriarchd migrate --check` reports `current`. The log line `database schema ready` names the schema version and the
  compatibility level.
- The UI shows the agents back online, and no rollout is stuck. A leader is elected (`myriarch_leader` in the
  [metrics reference](../reference/metrics.md)).
- If you left `install.enabled` on, the enrollment-token dialog in the UI shows the install commands for the platforms you use.

## Going back

A rollback to v0.1.0 means restoring the backup that you took before the update. Between versions that have the schema check
(v0.2.0 and later), `helm rollback` works as long as the migrations since then were compatible; the server refuses to start on
a schema that it cannot work with and says why ([Troubleshooting](troubleshooting.md)).

## Related

- [Install with Helm](install-helm.md): the chart, the values schema and the upgrade in short.
- [Production checklist](production-checklist.md): the settings of `myriarchd serve` that matter for security.
- [Changelog](../../CHANGELOG.md): every change, with the upgrade notes in full.
