# 0052. The gitops-pr executor bumps the collector image through a pull or merge request

- Status: Accepted
- Date: 2026-11-17
- Related: [ADR-0003](0003-gitops-as-source-of-truth.md), [ADR-0006](0006-version-updates-via-exchangeable-executors.md), [ADR-0013](0013-target-version-from-the-ui-within-the-git-frame.md), [ADR-0026](0026-collector-version-updates-on-kubernetes.md), [ADR-0048](0048-release-scope-of-v0-3-0-to-v0-6-0.md)

## Context

[ADR-0026](0026-collector-version-updates-on-kubernetes.md) decided that `gitops-pr` becomes the default executor on
Kubernetes, but not how it works. [ADR-0048](0048-release-scope-of-v0-3-0-to-v0-6-0.md) schedules it for v0.3.0. This record
fixes the design before code is written.

What exists today: the executor interface in `internal/executor` has Validate, Start and Cancel. The executors `webhook` and
`awx` are described by an `Executor` object that a fleet selects with `spec.update.executor` and `spec.update.executorRef`.
Secrets are references only (`tokenEnv` or `tokenFile`, with `executors.secretEnvPrefix` and `executors.secretDir`), bound to
target hosts through `executors.secretEnvHosts` and `executors.secretFileHosts` or through the registry `executors.secrets`
with `allowedHosts`. Target hosts are checked fail closed against `executors.allowedWebhookHosts`. Completion is decided only
by the version the agent reports ([ADR-0006](0006-version-updates-via-exchangeable-executors.md)). The rollout and target
states are those of [rollouts](../concepts/rollouts.md): a target is `pending`, `in_progress`, `verifying`, `succeeded`,
`failed`, `cancelled` or `rolled_back`.

One image field usually describes a whole workload, so one change affects many agents at once.

## Decision

1. **Configuration.** We will add an `Executor` object with `type: gitops-pr` and a `gitopsPr` block. A fleet selects it with
   `spec.update.executor: gitops-pr` and `spec.update.executorRef`, as for `webhook` and `awx`. The setting is per fleet; a
   blueprint carries no executor setting, because it describes configuration and several fleets share it. The fields are:
   - `provider`: `github` or `gitlab`.
   - `apiUrl`: optional, default `https://api.github.com` or `https://gitlab.com`; for GitHub Enterprise Server or a
     self-managed GitLab.
   - `repository`: `owner/name` or the project path.
   - `baseBranch`.
   - `path`: a file relative to the repository root. It may use `[[ attr "..." ]]` with agent attributes, for example one file
     per cluster. If the attribute is missing, exactly this target fails with a message.
   - `field`: the path to one YAML scalar in the file, for example `image.tag` in Helm values or a container image in a manifest.
   - `valueFormat`: `tag` (the scalar is only the tag) or `image` (default; the scalar is an image reference, only the tag is
     replaced, registry and name stay). A reference with a digest is rejected; digest pinning needs a later decision.
   - credentials by reference only (decision 2), and optional `labels` and `reviewers`.

   The static check in `myriarchd` and `myrctl` (type, URL syntax without credentials, required fields, exactly one secret
   reference) follows the existing two-step model; whether secret and host are allowed is checked by Validate when a rollout
   starts. Only YAML files are supported, and the file changes minimally: only the one scalar, comments and order stay.

2. **Credentials and Kubernetes.** `myriarchd` gets no Kubernetes API access and holds no cluster credentials
   ([ADR-0026](0026-collector-version-updates-on-kubernetes.md)). The Helm chart grants no RBAC for workloads; the GitOps
   controller in the cluster applies the change. The token or app installation is never stored in Git, in the database, in
   logs or in the audit trail. The repository names only a reference: `tokenEnv` or `tokenFile`, or an entry of the registry
   `executors.secrets`, under the rules that apply today (prefix, `secretDir`, no absolute paths, binding to target hosts
   outside development mode).
   - GitHub: we prefer a GitHub App installed on the one deployment repository only, with the permissions `contents: write`,
     `pull_requests: write` and `metadata: read`. App ID and private key are references (`appId`, `privateKeyFile` relative to
     `secretDir`, or a registry entry). The server derives short-lived installation tokens from them and keeps them in memory
     only. Alternatively a fine-grained token limited to that one repository, with the same permissions.
   - GitLab: a project access token of the deployment project with the role Developer and the scope `api` (required for merge
     requests), with an expiry date.
   - The API host must be in `executors.allowedWebhookHosts` (fail closed). Plain HTTP needs an explicit `http://` allowance.
     Redirects are not followed.
   - Protecting the base branch (required review) stays a matter for the repository. The executor never merges.

3. **Life cycle and mapping to states.** Start opens, per rollout, wave and executor object, a branch
   `myriarch/rollout-<rolloutId>/wave-<n>` (the prefix comes from `internal/brand`) from the current head of the base branch,
   one commit that changes the rendered files of all targets in the wave (targets with the same file share the change), and a
   pull or merge request with rollout ID, fleet, version and a link to the UI. If the file already holds the target version, no
   request is opened and the target counts as started. A retry is idempotent: an existing branch or request of the same wave is
   reused (at least once). The reference is stored per target (`github-pr:<owner>/<repo>#<n>` or `gitlab-mr:<project>!<iid>`,
   like `awx-job:<id>`).
   - Open: targets are `in_progress`; the progress shows "waiting for merge" with the link.
   - Merged: the merge commit is recorded in the audit trail and in the progress. Targets stay `in_progress` until the agent
     reports the version, then `verifying`, health wait, `succeeded`.
   - Closed without merge: the targets of that request become `failed` with a message, and the failure policy of the wave
     applies.

   `applyTimeout` applies as for every executor and must cover the review time. Opening, merging and closing are audit events.

4. **Abort, supersede and rollback.** Cancel (abort, superseded) closes every request of the rollout that is still open, with a
   comment, and deletes only branches that the executor created itself (prefix and rollout ID). A request that is already merged
   is not reverted by an abort; as with the other executors, abort changes no version. Rollback closes open requests first. For
   targets whose request was never merged, the base branch still holds the old version, so no new request is made. For merged
   changes the executor opens a new request `.../wave-<n>-rollback` that sets the field to the previous version, and again waits
   for the reported version. When a rollout reaches an end state, the executor closes remaining open requests and deletes their
   branches (best effort).

5. **Merge detection.** The leader polls the status of open requests through the provider API on the engine tick, at most every
   30 seconds per request, with backoff on rate-limit responses. There are no incoming provider webhooks in this version. The
   executor interface gets a query for the request status; the details belong to the change that builds the executor, together
   with the design document, the schema reference and `docs/concepts/executors.md`.

6. **Completion.** A target is finished only when the agent reports the target version and stays healthy
   ([ADR-0006](0006-version-updates-via-exchangeable-executors.md)). A merge, the status of the request or a signal of the
   GitOps controller never marks a target as finished. If the agent reports the version without a merge, the target still
   succeeds.

## Consequences

### Positive

- The declared image and the running binary stay the same.
- Review and audit also exist in the deployment repository.
- No cluster credentials in the server.
- One token with the smallest scope per repository.
- Rollback needs no special path.

### Negative

- Latency depends on review and merge, so `applyTimeout` must cover it.
- Polling costs API quota and delays the display by up to one tick interval.
- Two providers mean two test paths.
- One workload per file does not fit waves that share a workload; waves should cut along file or cluster.
- Digest references are not supported at first.
- A GitOps controller is a prerequisite.

## Alternatives considered

- **Provider webhooks for merge detection**: they need an incoming endpoint and a second secret per repository; completion
  depends on the reported version anyway, so polling is enough for display and for detecting closed requests.
- **Commit directly to the base branch**: no review, contradicts git as the frame
  ([ADR-0003](0003-gitops-as-source-of-truth.md), [ADR-0026](0026-collector-version-updates-on-kubernetes.md)) and needs wider
  write permissions.
- **Patch the workload through the Kubernetes API**: needs cluster credentials and RBAC, which
  [ADR-0026](0026-collector-version-updates-on-kubernetes.md) rejected.
- **Keep the generic webhook executor and an external pipeline**: every user would have to build branch, request, abort and
  rollback on their own, and the server would not see the state of the request.
- **Configuration in the blueprint**: a blueprint describes configuration and is shared by several fleets; the executor
  belongs to `spec.update` of the fleet.
- **Store the token in the database through the UI**: contradicts the rule that secrets come only from files or the
  environment and are only named.
