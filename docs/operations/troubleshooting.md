---
title: "Troubleshooting"
weight: 30
description: "Symptoms, causes and fixes for the local system and for installations, plus the endpoints that help to diagnose them."
---

Start with the log of `myriarchd` and the page of the object in the UI (agent, rollout, GitOps). The tables below are
ordered by area. Issue codes of the GitOps repository, such as `blueprint_not_found`, are explained in the
[GitOps schema reference](../reference/gitops-schema.md).

## Local development

| Symptom | Cause and fix |
|---|---|
| `too many open files`, connections break | Raise the limit with `ulimit -n 65536` in **every** terminal that runs `myriarchd` or the simulator. The macOS default is 256. |
| `myriarchd dev` does not start: `address already in use` | Port 8080 (`myriarchd`) or 54329 (PostgreSQL) is taken. If a dev PostgreSQL with the dev credentials already runs there, it is **reused**, even for another `--data-dir`. For parallel environments set `dev.postgresPort` in a `-c` file. |
| `myriarchd dev` refuses to start: "refusing to start dev mode" | The settings look like production (non-loopback `--listen`, another database host, OIDC, TLS, an https `publicUrl`). Dev mode must stay on the local machine; see [Local development](../getting-started/local-dev.md). `--allow-nonlocal` overrides the check on purpose. |
| The first start takes long and downloads files | A one-time download of the PostgreSQL binaries (about 30 MB) into `myriarch/embedded-postgres` below the user cache directory (`~/Library/Caches` on macOS, `~/.cache` on Linux). |
| A `postgres` process is still running after `kill -9` of `myriarchd` | The embedded PostgreSQL is stopped only on a regular exit: `kill $(head -1 .dev/postgres/data/postmaster.pid)`. |
| The UI shows nothing, or `/` answers 404 | `myriarchd` was built without the embedded UI. Run `make web-build && make build` (build tag `embedui`). |
| A real collector stays in `applying` for more than 30 seconds on its first start | macOS Gatekeeper checks `otelcol-contrib` at the first start; later starts take seconds. |

## Server and GitOps

| Symptom | Cause and fix |
|---|---|
| `/readyz` answers 503 `not ready` | The body names no reason. The cause (the database or the event bus; the schema is checked at start) is in the log line `readiness check failed`. `/healthz` answers `ok` as long as the process runs. |
| `myriarchd` stops at start with `the database schema is incompatible with this myriarchd` (from v0.2.0) | A newer version migrated the database in a way that this version cannot work with. The error names the schema version of the database, its compatibility level and the schema level of this binary. Nothing was changed. Start a version whose schema level is at least the compatibility level in the error, or restore a backup of the database from before the upgrade. |
| `myriarchd` stops at start with `the database schema is at version N but this myriarchd needs version M and database.migrateOnStart is false` (from v0.2.0) | The database lacks migrations and the server is not allowed to apply them. Run `myriarchd migrate` (it needs only the database settings; `--check` shows the state without changing anything), or set `database.migrateOnStart` to `true`. |
| The log warns `the database schema is newer than this myriarchd but compatible with it` (from v0.2.0) | An older version runs on a database that a newer version migrated: after a rollback, or while a rolling update is under way. It works because the compatibility level allows it. Run the version that matches the schema as soon as you can. |
| GitOps status `error` | `GET /api/v1/gitops/status` lists the issues with file and line. Reproduce them locally with `myrctl validate <repo>` (see [Validate a GitOps repository](../tasks/validate-a-gitops-repository.md)). An invalid state **never** replaces the applied one. |
| Changes in Git do not arrive | The server polls every `gitops.pollInterval` (60 s by default, 10 s in dev mode). Trigger a sync with `POST /api/v1/gitops/actions/sync` or the webhook `POST /api/v1/gitops/webhook`, and check the status for a validation error. |
| Two leaders in `system/info` | Only briefly after a crash: a dead leader keeps its last heartbeat row. `system/info` names only the replica with the newest heartbeat as leader. |
| `x509: certificate signed by unknown authority` for OIDC, Git, GitHub, the mirror, TUF or Teams | `myriarchd` does not know the CA of the target. Set `outbound.caFile` to a PEM file with the CA (it applies to **all** outbound connections; in the Helm chart mount the CA bundle with `extraVolumes` and `extraVolumeMounts`, for example from a ConfigMap, and set `config.outbound.caFile` to the mounted path). For Git there is also `gitops.auth.caFile`, for an AWX executor its `caFile`; both add to the trust store. Check with `myriarchd config validate --config ...`, which reads the file. |
| `myriarchd config validate` prints `ERROR` lines | Each line names the setting (`<path>: <message>`). File and certificate errors disappear when the secrets are mounted. Use `--skip-files` in CI. |

## Agents and assignment

| Symptom | Cause and fix |
|---|---|
| An agent does not appear, or the handshake answers `401` | Check the enrollment token (header `Authorization: Bearer myre_...`); in dev mode it is optional. Use a `ws://` endpoint for a plain local server. |
| The installer of a host stops with `ERROR [<code>] ...`, or a Windows host has no service `otelcol-supervisor` after the installer ran | The code names the cause, for example `option_missing` (the collector version is required), `sigstore_verifier_missing` or `template_missing`. A supervisor from an upstream MSI is another service (`opampsupervisor`) that myriarch does not manage; from v0.2.0 the installer stops with `conflicting_install` when it finds one, and it names a missing supervisor file (`supervisor_missing`) and a script that PowerShell runs in Constrained Language Mode (`language_mode_unsupported`). The table "When the installer stops" in [Connect a host](../tasks/connect-a-host.md#when-the-installer-stops) lists the codes of a first installation and the fixes. |
| The agent is `unassigned` | No fleet selector matches. Compare the attributes on the agent page (`site.type`, `site.country`, ...) with the `selector` of the fleets, or use *Fleets*, "Which fleet?". Two fleets with the same priority that match the same agent also leave it unassigned (`fleet_ambiguous`): give overlapping fleets different priorities. |
| The agent reports "the instance uid is bound to another credential" | The instance UID was registered with another enrollment token or certificate (a cloned machine, a re-enrolled host). The administrator action `POST /api/v1/agents/{id}/actions/reenroll` releases the binding. It is also needed after the old token was revoked, unless `opamp.autoRebindOnTokenRotation` is on. |
| `429` at login or on API calls | Rate limiting: the dev login and the bootstrap admin allow 5 attempts per minute per address, repeated invalid bearer tokens delay the client address (header `Retry-After`). Only distinct invalid tokens count; recently valid tokens of other clients still work. Behind an ingress set `server.trustedProxies`, otherwise all clients share the address of the ingress. |
| The supervisor reports `FAILED` and the collector stays on the old configuration | Intended (`automatic_config_rollback`). `supervisor.log` and `lastError` on the agent name the validation error. |
| Dashboard shows 100 % "outdated" after a new release | By definition: outdated means below the newest stable release. The target version of a fleet is decided by Git (`spec.collector.version`). |

## Rollouts

| Symptom | Cause and fix |
|---|---|
| A configuration target stays `in_progress` and the agent reports `applying` after it restarted | A supervisor that restarts while it applies a configuration reports the stored `applying` again, and nothing ends it. After `opamp.staleApplyingAfter` (90 seconds by default) on the connection, the server offers the configuration once more (agent event `config_offered` with the reason `stale_applying`, counter `myriarch_opamp_config_reoffers_total`), and the supervisor answers `applied`. If it does not, the target waits for the `applyTimeout` of its policy. With `opamp.staleApplyingAfter: 0` the server never offers it again. |
| A rollout waits forever in `waiting_approval` | A wave with `requireApproval` needs an approval (UI, or `.../actions/approve` with the `wave`). Waves with no targets are skipped. |
| A rollout is `paused` with errors | The failure threshold was reached. Read `lastError` of the failed targets, then `retry-failed`, `resume`, `rollback`, or fix Git (a fix replaces the paused rollout). |
| A rollout `failed` with "fleet ... was removed from the GitOps repository" | The fleet was removed from Git. The rollout needs your confirmation: `.../actions/abort`. |
| A configuration target fails with "agent became unhealthy during the health wait" | The collector restarted during `healthWait`, for example through a **simultaneous** version update of the same host (a configuration and a version rollout of one fleet run independently). Use `retry-failed`, or finish the version rollout first. |
| A version rollout stays `pending` or "retry later" | The artifact is still being downloaded or verified. `GET /api/v1/artifacts` shows `pending`, `downloading` or `failed` with the error. Without internet access use a mirror ([Roll out a collector version](../tasks/roll-out-a-collector-version.md)). |
| Targets stay in `waiting_window` | The rollout policy has maintenance windows and the current time is outside them (site-local time, or UTC where a window sets it). Wait, or use a policy without windows in a test setup. |
| An update of a Linux host fails in the step `verifying`, and the journal of `myriarch-updater` says `preflight: ... fork/exec ... operation not permitted` | A defect of v0.1.0: its unit `myriarch-updater.service` sets `NoNewPrivileges=true`, which stops the preflight from starting the new collector under the account of the supervisor. The old version keeps running. Fixed from v0.2.0 (run its installer again). On v0.1.0 run `systemctl edit myriarch-updater`, add the two lines `[Service]` and `NoNewPrivileges=false`, save, and run `systemctl restart myriarch-updater`. Then retry the failed target (`retry-failed`). |
| `INSTALL_FAILED ... not retrying for another ...` at the updater | The retry brake: the same content is not tried again for 10 minutes after a failure (`update.failedRetryBackoff`). |
| A webhook or AWX call does not arrive | Is the target in `executors.allowedWebhookHosts` (an empty list refuses everything; `http://host` allows plain HTTP) **and** is its address released by `outbound.allowedCidrs` ("not allowed by the outbound policy" for private and loopback ranges)? Does the secret environment variable (`secretEnv`) start with `MYRIARCH_EXEC_` and exist in the process (or is the name in `executors.secrets` with matching `allowedHosts`)? In production the secret must also be bound to the target host (`executors.secretEnvHosts` or `secretFileHosts`; message "is not bound to a destination"). Redirects are not followed. |
| An artifact is `failed` with `HTTP 401` or `HTTP 403` from the mirror | The mirror rejects the credentials or wants some: set `artifacts.sourceAuth` or check the token or password and the permissions of the account (read access to the remote or proxy repository). The message is in `GET /api/v1/artifacts`. The credentials go only to the origin of `artifacts.sourceBaseUrl`; if the mirror redirects to another host, that host must allow anonymous reads and be listed in `artifacts.allowedRedirectHosts`. |
| An artifact is `failed` with `fetch sigstore trusted root via TUF` | `myriarchd` cannot reach the TUF repository. Check the proxy (`HTTPS_PROXY`, `NO_PROXY`) and `outbound.caFile`, point `artifacts.verify.tufRepositoryUrl` at a mirror, or set a fixed `artifacts.verify.trustedRootFile` (`trusted_root.json`). |

## Sign-in

| Symptom | Cause and fix |
|---|---|
| OIDC sign-in fails with `oidc: issuer did not match`, or `myriarchd` does not start with an issuer error | The issuer must equal the `issuer` of the discovery document exactly. The formats of common providers and the fixes are in [Authentication](authentication.md) (sections 4 and 5). Look at the document by hand: `curl <issuerUrl>/.well-known/openid-configuration` (through the proxy if there is one). |
| OIDC sign-in works but ends with `access_denied` | No role matched (the audit log shows `no_role`). Check `auth.oidc.groupsClaim`, `roleMapping` and the claims of the ID token; see [Authentication](authentication.md) (section 7). |

## Simulator

| Symptom | Cause and fix |
|---|---|
| Many `connectErrors` after `myriarchd` restarted | Expected: while shutting down the server answers `503` with `Retry-After`, and the agents reconnect with backoff (1,900 agents take one to two minutes). |

## Useful endpoints

| Endpoint | Content |
|---|---|
| `GET /healthz` | Liveness: `ok`. |
| `GET /readyz` | Readiness: `ready`, or 503 `not ready`. |
| `GET /metrics` | Prometheus metrics, for example `myriarch_opamp_connections`, `myriarch_agents`, `myriarch_config_sync`, `myriarch_leader`, `myriarch_rollouts_active`, `myriarch_gitops_sync_total` and `myriarch_http_requests_total`. In production they are on the separate port `server.metricsListenAddr` (default `:9090`). |
| `GET /api/v1/system/info` | Version, replicas, leader, database state, features. |
| `GET /api/v1/events/stream` | Live events (server-sent events). |
| `GET /api/v1/gitops/status`, `GET /api/v1/gitops/syncs` | State and history of the GitOps sync. |
| `GET /api/v1/audit` | The audit log. |

Still stuck? The [production checklist](production-checklist.md) covers the settings that `myriarchd` refuses, and the
[`myriarchd config validate`](../reference/cli/myriarchd_config_validate.md) reference explains how to check a configuration.
