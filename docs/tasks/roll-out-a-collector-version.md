---
title: "Roll out a collector version"
weight: 40
description: "Update the collector version of a fleet with the updater, webhook or AWX executor, and choose the version in the UI within the frame set by Git."
---

A **version rollout** brings the collectors of a fleet to a target version. The upstream OpAMP Supervisor cannot replace
binaries, so an exchangeable **executor** does the installation. The rollout engine decides which hosts update when
(waves, approval, maintenance windows) and the collector itself proves the result: a target is complete only when the
collector reports the new `service.version`. The answer of an executor does not count.

| Executor | What installs the new version |
|---|---|
| `updater` | The host agent `myriarch-updater` on Linux and Windows hosts, or in container mode in a Kubernetes pod. |
| `webhook` | A call to a URL, for example a pipeline that bumps an image tag. |
| `awx` | A job template in Ansible AWX. |
| `manual` | Nothing: the version is changed by hand and `myriarchd` only tracks the result. |
| `none` | The fleet is not updated by `myriarchd`; the fleet's `collector.version` only serves display and the outdated check. |

The executor is a property of the fleet (`spec.update.executor`). The concepts are described in [Executors](../concepts/executors.md),
[Fleets](../concepts/fleets.md) and [Rollouts](../concepts/rollouts.md).

## Update with the updater executor

The fleets `linux-hosts` and `windows-hosts` of the example repository use the executor `updater`. The flow for one host:

1. `myriarchd` fetches the release archive from `artifacts.sourceBaseUrl` (default: the GitHub release downloads of
   OpenTelemetry), verifies the SHA-256 checksum and the Sigstore signature and keeps the file in its artifact cache.
2. It offers the file to the `myriarch-updater` of the host as an OpAMP package offer with a short-lived download token.
3. The updater downloads the file from `myriarchd`, verifies it again, swaps the collector binary, restarts the supervisor
   service and checks the health of the collector. If the new version does not become healthy, the updater restores the
   previous one itself.
4. The collector reports the new version and the target is complete.

With the simulator running ([Simulate a fleet](simulate-a-fleet.md)), the simulated edge hosts start on 0.161.0, one version
below the Git target 0.162.0, so `myriarchd` creates version rollouts as soon as it starts. The first rollout downloads the
release archives from GitHub (several hundred megabytes for all platforms); `GET /api/v1/artifacts` shows each artifact with
its status (`pending`, `downloading`, `ready` or `failed` with the error) and `POST /api/v1/artifacts/actions/prefetch` loads versions in
advance.

### Maintenance windows

The version policy `canary-then-all` of the example repository lets a target **start** only inside a maintenance window:
Monday to Friday 22:00 to 05:00 in the local time of the host (attribute `site.timezone`) and the whole weekend in UTC.
Outside a window targets wait in the status `waiting_window`, so a rollout started during the day begins in the evening. To
watch an update immediately, copy the repository, set `versionPolicy: config-canary` in the fleets of the copy and start
`myriarchd dev` with `--gitops-dir` on the copy. `config-canary` has no windows and rolls back on a failure.

### Failures and retries

The policy `canary-then-all` has `onFailure: pause`: the updater already restored the failed host, and a person decides
whether the rest of the fleet is updated (`resume`, `retry-failed`, `abort` or `rollback` on the rollout, buttons in the UI
or the API actions described in [Roll out a configuration change](roll-out-a-config-change.md)). After a failed installation
the updater does not retry the same content for 10 minutes (`update.failedRetryBackoff` in `updater.yaml`); the package status
`INSTALL_FAILED` then carries the message "install of v... failed ... ago (...); not retrying for another ...". "Retry failed" on the rollout is the short way.

### Policies on the host

The host side has its own limits in `updater.yaml`, which no server message can override:

| Key | Meaning |
|---|---|
| `update.minimumVersion` | Lower bound: the updater never installs an older version. |
| `update.allowDowngrade` | Default `false`: only forward updates and the step back to the previous version are accepted. |
| `update.trustedRootFile` | Sigstore trusted root for offline verification. |
| `server.allowInsecureTransport` | Allows `ws://` and `http://` endpoints in a lab. |

The protocol and the checks are specified in [updater-protocol.md](../design/updater-protocol.md). The settings of the
server side that matter here are `artifacts.*` (source, verification, cache) and `rollouts.executorTimeout` (default 2
minutes for one executor call); see the [production checklist](../operations/production-checklist.md).

## Choose the version in the UI

Git sets the frame, the UI chooses within it. A fleet that enables selection lists the versions an operator may pick:

```yaml
spec:
  collector:
    distribution: otelcol-contrib
    version: 0.162.0              # the Git target
  update:
    executor: updater
    selection:
      enabled: true
      versionRange: ">=0.161.0, <=0.162.0"
```

On the page of such a fleet, the section "Target version" offers **Update version**. The dialog lists the selectable
versions, newest first, with the number of collectors that would be updated and the breaking changes of each version, and
asks for a reason (stored in the audit log; mandatory when the frame sets `requireReason`). **Update to X** stores the
choice, and the leader creates a version rollout with the fleet's version policy, or with `selection.rolloutPolicy` if the
frame names one. A running version rollout to another version is superseded. The role *operator* is required.

The same operation over the REST API. The example frame above is the one of the quickstart fleet `linux-hosts`, whose Git
target is already `0.162.0`: it marks `0.162.0` as `current_target` and `0.161.0` as `downgrade_not_allowed`, so nothing is
selectable. To try a choice, widen the frame in your working copy, for example with `allowDowngrade: true` (then
`0.161.0` is selectable), or with a `versionRange` that includes a newer release. The call below assumes such a working copy
whose Git target is `0.162.0` and a selection of `0.161.0`:

```bash
api localhost:8080/api/v1/fleets/linux-hosts/version-options | jq '.options[] | {version, selectable}'
api -X POST -d '{"version":"0.161.0","reason":"known good version","expectedTargetVersion":"0.162.0"}' \
  localhost:8080/api/v1/fleets/linux-hosts/actions/select-version
api -X POST -d '{"reason":"back to Git"}' localhost:8080/api/v1/fleets/linux-hosts/actions/clear-version-selection
```

`expectedTargetVersion` is the `target.version` from `version-options` (the current effective target). It is optimistic concurrency: if someone changed the target in the meantime the call answers
`409 conflict`. A refused choice answers `422 validation_failed` with the issue code `version_not_allowed` (outside the
frame), `downgrade_not_allowed`, `unknown_version`, `prerelease_not_allowed` or `reason_required`; `412
precondition_failed` means that selection is not enabled, the fleet has no update executor, or a failed version rollout
blocks the fleet (abort it first). Clearing the selection makes the Git target apply again; the UI warns when that means a
downgrade.

When Git changes after a choice was made, fixed rules decide which one wins. They are described in
[Fleets](../concepts/fleets.md) and specified in [version-selection.md](../design/version-selection.md). The principle is
"the most recent explicit intent wins, and nothing downgrades a collector by surprise". Every choice is audited as
`fleet.version.select` with who, when and why.

## Update with the webhook or AWX executor

For fleets whose collectors are not managed by an updater, `myriarchd` calls your own tooling. Two examples from the
repository:

```yaml
# gitops-template/config/executors/awx-linux.yaml (shortened)
apiVersion: myriarch.dev/v1alpha1
kind: Executor
metadata:
  name: awx-linux
spec:
  type: awx
  awx:
    url: https://awx.example.com
    jobTemplateId: 1
    tokenEnv: MYRIARCH_EXEC_AWX_TOKEN
    limitAttribute: host.name
    extraVars:
      otelcol_version: "[[ .toVersion ]]"
      myriarch_rollout: "[[ .rolloutId ]]"
    batchSize: 10
```

```yaml
# adapted from internal/testdata/gitops/executors/k8s-image-bump.yaml
apiVersion: myriarch.dev/v1alpha1
kind: Executor
metadata:
  name: k8s-image-bump
spec:
  type: webhook
  webhook:
    url: https://ci-hooks.example.test/myriarch/k8s-image-bump
    method: POST
    secretEnv: MYRIARCH_EXEC_WEBHOOK_SECRET_K8S
    timeout: 30s
    batchSize: 10
```

A fleet refers to the executor with `update.executor: awx` (or `webhook`) and `update.executorRef: <name>`. The webhook
body is signed with HMAC-SHA-256 (header `X-Myriarch-Signature`, computed over timestamp and body); the receiver verifies
signature and timestamp. Payload, signature and the other fields are specified in [Executors](../concepts/executors.md).
The security rules of the server apply to every call:

- `executors.allowedWebhookHosts` lists the permitted targets. An empty list refuses every webhook and AWX target (fail
  closed) but does not stop the server from starting. `http://host` allows plain HTTP for one host.
- The destination address must also be allowed by the outbound policy: private and loopback ranges are blocked unless you
  list them in `outbound.allowedCidrs` (the error is "not allowed by the outbound policy").
- Secrets are never in Git. A secret is an environment variable that starts with `MYRIARCH_EXEC_` (or a registered name in
  `executors.secrets`) and is bound to its destination hosts (`executors.secretEnvHosts`, `executors.secretFileHosts`,
  or `allowedHosts` in `executors.secrets`). In production an unbound secret is refused ("is not bound to a destination").
- Redirects are not followed.

To try the executors without a pipeline, `scripts/e2e/mock_receiver.py` implements a receiver that verifies the signature
and lets the simulator "update" the version through its control API. The end-to-end test `scripts/e2e.sh` wires it up;
see the [development guide](../development.md).

## Work offline with a local mirror

`artifacts.sourceBaseUrl` can point to any server with the layout of the upstream releases,
`<base>/<tag>/<asset>` (for example `v0.162.0/otelcol-contrib_0.162.0_linux_amd64.tar.gz` and the matching `.sha256`). For
local experiments with the simulator, `scripts/e2e/mirror.py` serves synthetic archives for every version, so that no
network access is needed:

```bash
PORT=19998 python3 scripts/e2e/mirror.py &
cat > .dev/mirror.yaml <<'EOF'
artifacts:
  sourceBaseUrl: http://127.0.0.1:19998
  verify:
    requireSignature: false     # the synthetic archives carry no Sigstore bundle; dev mode only
EOF
bin/myriarchd dev --gitops-dir .dev/gitops-work --data-dir .dev -c .dev/mirror.yaml
bin/myriarch-sim run --server ws://localhost:8080/v1/opamp --profile examples/sim/mixed-fleet.yaml --fake-downloads=false
```

With `--fake-downloads=false` the simulated updaters really download the file from `myriarchd` with their download token
and check its SHA-256. The mirror is for the simulator only: a real host cannot run a synthetic archive.

For real hosts without internet access, mirror the official release files one to one into an internal artifact
repository and set `artifacts.sourceBaseUrl` (plus `artifacts.sourceAuth` for its credentials) and, behind a proxy, the
TUF settings under `artifacts.verify`. Signature verification stays on. The checks are listed in the
[production checklist](../operations/production-checklist.md); the helpers for mirroring and re-signing are in
[contrib/enterprise-mirror](../../contrib/enterprise-mirror/README.md).

To see the whole UI-triggered update with real release files on your machine (a local mirror of real collector releases,
the real supervisor and the `myriarch-updater` in container mode), run `make e2e-ui-updates`; it is described in the
[development guide](../development.md).
