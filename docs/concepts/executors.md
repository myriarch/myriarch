---
title: "Executors"
weight: 70
description: "How collector version updates are started: the executors none, updater, webhook, awx and manual, their payloads, signatures, secrets and the host allowlist."
---

An **executor** starts the update of a collector to a new version. The fleet chooses it with `spec.update.executor`.
The server never learns that an update is finished from the executor. It watches the version the collector itself
reports, and the rollout engine applies the usual health wait. This keeps the result trustworthy whatever the external
system reports, and it is the rule behind
[ADR-0006](../adr/0006-version-updates-via-exchangeable-executors.md).

| Executor | What it does | Needs an `Executor` object |
|---|---|---|
| `none` (default) | Versions are only reported, never changed. | no |
| `updater` | Offers a verified package to the `myriarch-updater` on the host through OpAMP. | no |
| `webhook` | Sends a signed HTTP request with the list of targets. | yes (`executorRef`) |
| `awx` | Launches a job template of Ansible AWX or Ansible Automation Platform. | yes (`executorRef`) |
| `manual` | Makes no external call. The version is changed outside of `myriarchd`. | no |

For `updater` and `manual`, an `executorRef` is ignored with a warning. For `webhook` and `awx` it is required and must
name an `Executor` object of the same type.

## Updater

The `updater` executor offers a package to the `myriarch-updater` that is linked to the collector (see
[Agents](agents.md#kinds-of-agents)), because the upstream supervisor cannot replace binaries. For each target the
server resolves the linked updater, which must be online, makes sure the artifact for the distribution, version,
operating system and architecture is in its cache and verified (SHA-256 and a Sigstore bundle), and offers it as an OpAMP
package: version with a leading `v`, a download URL with a short-lived download token, the SHA-256 and the signature.
The updater downloads, verifies, installs, restarts the collector, checks its health and rolls back by itself if the new
version does not become healthy. Its steps are reported as the [live progress](rollouts.md#live-progress-per-target) of
the target. If the artifact is not available yet, the target waits and is retried on the next tick. It does not fail.

The package format is described in
[ADR-0007](../adr/0007-package-offers-in-upstream-format-with-sigstore-bundles.md), the host side in the
[updater protocol](../design/updater-protocol.md), and the container variant in
[container mode](../design/container-mode.md) and
[ADR-0014](../adr/0014-binary-updates-in-the-pod-via-container-mode.md).

## Manual

The `manual` executor makes **no** external call. It reports every target as started immediately. `myriarchd` creates the
version rollout and, like for every executor, waits for the version the collector reports, bounded by the policy's
`applyTimeout`. It fits when the version changes outside of `myriarchd`, for example by an image tag in a merge request
or by an operator on the host, and `myriarchd` should only follow the progress. A rollback of such a rollout starts with
the previous version and again waits for the reported version. Together with
[`update.selection`](fleets.md#target-version-from-the-ui-within-the-git-frame) the UI chooses the target version and
`myriarchd` waits for the change from outside.

## The Executor object

The built-in executors `updater` and `manual` need no object. `webhook` and `awx` are described by an `Executor` object.
Secrets are never stored in it. It only names them.

```yaml
apiVersion: myriarch.dev/v1alpha1
kind: Executor
metadata: {name: k8s-image-bump}
spec:
  type: webhook
  webhook:
    url: https://hooks.example.com/myriarch/k8s-image-bump
    method: POST                        # POST (default), PUT or PATCH
    secretEnv: MYRIARCH_EXEC_WEBHOOK_SECRET_K8S   # HMAC secret, read from the myriarchd environment
    headers: {X-Team: observability}
    timeout: 30s                        # default 30s, at most 5m
    batchSize: 10                       # targets per call, default 100
---
apiVersion: myriarch.dev/v1alpha1
kind: Executor
metadata: {name: awx-linux}
spec:
  type: awx
  awx:
    url: https://awx.example.com
    jobTemplateId: 42
    apiPrefix: /api/v2/                 # default; Automation Platform 2.5 and later: /api/controller/v2/
    tokenEnv: MYRIARCH_EXEC_AWX_TOKEN   # or tokenFile
    limitAttribute: host.name           # default; becomes the AWX "limit"
    extraVars:
      otelcol_version: "[[ .toVersion ]]"
    caFile: ""                          # an additional CA for AWX (optional)
    timeout: 30s
    batchSize: 50                       # hosts per job launch, default 100
```

The fields are listed in the [schema reference](../reference/gitops-schema.md#executor).

### Validation in two steps

GitOps validation, in `myriarchd` **and** in `myrctl`, checks executors only **statically**: the type, the URL syntax
(an absolute `http` or `https` URL without credentials), required fields (such as `jobTemplateId`), that exactly one of
`secretEnv` and `secretFile` (or `tokenEnv` and `tokenFile`) is named, that custom headers are allowed, and that
`timeout` is at most 5 minutes. These checks do not depend on the environment, so CI and the server agree.

Whether the secret exists on the `myriarchd` host and whether the target host is allowed is checked by the executor only
when a version rollout starts. A failure there fails the targets with a message. At run time `myriarchd` additionally
caps every call to `executors.maxTimeout` and `rollouts.executorTimeout`.

### Webhook

The call is `POST <url>` (or the configured method) with a JSON body, `executor.WebhookPayload`:

```json
{
  "event": "collector.update",
  "deliveryId": "6c3e0b1a-...",
  "rolloutId": "f3a1c2d4-...",
  "wave": 0,
  "fleet": "linux-hosts",
  "distribution": "otelcol-contrib",
  "toVersion": "0.162.0",
  "params": {},
  "targets": [
    {"agentId": "...", "hostname": "linux-edge-01.example.com", "osType": "linux", "arch": "amd64",
     "currentVersion": "0.161.0", "attributes": {"site.id": "edge-001"}}
  ]
}
```

`params` is the `spec.update.params` of the fleet, passed on unchanged. Three headers are added:

| Header | Content |
|---|---|
| `X-Myriarch-Delivery` | A deterministic UUID for this rollout, wave, attempt and target set. The same call repeated carries the same ID, so the receiver can de-duplicate an at-least-once delivery. |
| `X-Myriarch-Timestamp` | Unix time in seconds. |
| `X-Myriarch-Signature` | `sha256=` followed by the hexadecimal HMAC-SHA256 of `<timestamp>.<body>` with the shared secret. |

A receiver should verify the signature over the exact bytes it received and reject timestamps outside plus or minus
300 seconds of its own clock (the window of the notification webhooks). Within that window it drops repeated
`X-Myriarch-Delivery` values: the same call repeated carries the same delivery ID, so an at-least-once delivery is
processed once. The signature covers the timestamp and the body only: the HTTP method, the URL and the other headers are
not signed, so a receiver should accept the one method and path it serves and must not treat other headers as
authenticated. The answer `2xx`
means accepted, and anything else fails **all** targets of that call. Redirects are not followed. Custom headers must not
use the reserved prefix `X-Myriarch-` and must not set credentials (`Authorization`, `Cookie`, `Proxy-Authorization`,
`Host`, `Content-Length`), because credentials belong in the secret.

**Cancel.** If the rollout is aborted or superseded, the webhook receives the same payload with the event
`collector.update.cancel` (best effort).

### AWX

The call is `POST <url><apiPrefix>job_templates/<id>/launch/` with `Authorization: Bearer <token>` and the body

```json
{"limit": "host1,host2", "extra_vars": {"otelcol_version": "0.162.0", "myriarch": {"event": "collector.update"}}}
```

where `myriarch` carries the same payload as for webhooks. The job template must have "prompt on launch" enabled for the
limit and for variables, because the executor fails the targets, and cancels the job it just launched, if AWX reports that it ignored `limit` or
`extra_vars` (without the limit the job would run on all hosts).
The job ID is stored as `awx-job:<id>` with each target, and a cancel is `POST <url><apiPrefix>jobs/<id>/cancel/`
(best effort).

**The limit.** The `limit` list is built from an agent attribute, `limitAttribute` (default `host.name`). Each value must
be a plain host name matching `^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`. That excludes `,` `:` `*` `!` `&` `~` `[` `]`, white
space and the group names `all` and `ungrouped`. A value that does not fit fails exactly that target with a message and
is never passed on.

**The template context for `extraVars`.** String values may use `[[ ... ]]` with these fields: `.toVersion`,
`.distribution`, `.fleet`, `.rolloutId`, `.wave`, `.hostnames` (a list) and `.params`. The functions `attr` and `param`
see empty contexts. The [limits of templates](blueprints.md#templates) apply.

## Allowed hosts

The executors are **fail closed**: every target host must be listed in `executors.allowedWebhookHosts` of the server
configuration, and an empty list refuses every webhook and AWX target in every mode. It does not stop the server from
starting. An entry is a host name (`hooks.example.com`), a host with a port (`hooks.example.com:8443`) or a suffix
(`*.example.com`). Plain HTTP is allowed only for a host that is listed explicitly as `http://host`, so credentials and
signed payloads never travel unencrypted by accident.

## Secrets

The repository chooses only a **name**. Which environment variables and files can be read is decided by the configuration
of the `myriarchd` host, so that Git can never read arbitrary variables or paths.

- `secretEnv` and `tokenEnv` must start with `executors.secretEnvPrefix` (default `MYRIARCH_EXEC_`). No other variable of
  the process can be read.
- `secretFile`, `tokenFile` and `caFile` are file names **relative to `executors.secretDir`**: no absolute path, no `..`,
  and no escape through symbolic links. Without `secretDir`, file references are switched off.
- Outside dev mode every secret referenced like this must be **bound to destinations**: `executors.secretEnvHosts`
  (`{VARIABLE: [hosts]}`) or `executors.secretFileHosts` (`{file: [hosts]}`). Without a binding the reference is refused.
  With one, the secret is sent only to these hosts (checked in Validate, Start and Cancel).
- Alternatively the name is an entry of the registry `executors.secrets`, where the operator defines the source (`env` or
  `file`) and the `allowedHosts` of the secret. The secret is sent only to these hosts.
- A webhook HMAC secret must be at least 32 bytes long.

The complete list of settings is in the server configuration (`myriarchd config print` shows the effective values). The
security view is in the [security model](../security/model.md).

## Completion

A version update is finished when the collector **reports the target version** and stays healthy for `healthWait`. This
holds for every executor and is independent of callbacks from the external system, see
[Rollouts](rollouts.md#targets-and-success).
