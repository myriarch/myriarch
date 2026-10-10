---
title: "Git as the source of truth"
weight: 10
description: "How a Git repository describes blueprints, fleets, policies, executors and inventory, and how myriarchd reads, validates and applies it."
---

Git is the **source of truth** for every declarative object: blueprints (configuration templates), fleets (which agents
get which blueprint and which collector version), rollout policies, executor definitions and the inventory.
`myriarchd` reads the repository by polling and by webhook, validates the whole revision and takes it over only if it is
**completely valid**. Operators change the system by merging a change in Git, not by editing objects in the web
interface.

A complete, valid example repository is [`examples/quickstart/`](../../examples/quickstart/). The ADRs behind this
design are
[ADR-0003 (GitOps as the source of truth)](../adr/0003-gitops-as-source-of-truth.md) and
[ADR-0013 (target version from the UI within the Git frame)](../adr/0013-target-version-from-the-ui-within-the-git-frame.md).

## Git sets the frame, the UI operates within it

The web interface shows the Git objects read-only, with the commit and the source file they come from and with a
render preview. It offers only **operational** actions:

| Operated in the UI or API | Decided in Git |
|---|---|
| Pause, resume, abort, roll back, retry or approve a rollout | Which blueprint, parameters and collector version a fleet has |
| Restart an agent, resync its configuration | Which agents belong to a fleet (selectors, priority) |
| Acknowledge a release notice | Waves, thresholds and maintenance windows (rollout policies) |
| Choose the target collector version of a fleet, **if Git allows it** and only **inside the range Git defines** | Whether the UI may choose a version at all, and from which range or list |

Every UI action is audited, and a version chosen in the UI is marked as "set in the UI" with who, when and why. The
rules for the version choice are described in [Fleets](fleets.md#target-version-from-the-ui-within-the-git-frame).

## Repository layout

```text
gitops-repo/
├── blueprints/        # kind: Blueprint
├── fleets/            # kind: Fleet
├── policies/          # kind: RolloutPolicy
├── executors/         # kind: Executor
├── inventory/         # kind: Inventory
└── samples/agents/    # sample agents for myrctl (not objects of myriarchd)
```

The directory names are a convention. `myriarchd` reads **every** `*.yaml` and `*.yml` file below the configured
repository path (`gitops.path`, empty means the repository root), except directories whose name starts with `.` and the
`samples/` directory directly below that path. One file may contain several objects, written as YAML documents
separated by `---`. The sample agents in `samples/agents/` are used only by `myrctl` and by validation, see
[Validate a GitOps repository](../tasks/validate-a-gitops-repository.md).

## The object envelope

Every object has the same envelope, in the style of Kubernetes:

```yaml
apiVersion: myriarch.dev/v1alpha1   # required, exactly this value
kind: Fleet                         # Fleet | Blueprint | RolloutPolicy | Executor | Inventory
metadata:
  name: linux-hosts                 # required, unique per kind
  description: Linux hosts at edge sites and in data centers   # optional
  labels: {team: edge-ops}          # optional, free text
spec: {}                            # specific to the kind
```

The `name` must be a DNS label: lower-case letters, digits and `-`, 1 to 63 characters, starting and ending with a
letter or digit. Names are unique per kind, so a blueprint and a fleet may share a name. The `metadata.labels` of a
fleet are not used for matching; templates can read them as `.fleet.labels`. All fields of the envelope and of every
`spec` are listed in the [GitOps schema reference](../reference/gitops-schema.md).

## Rules for the repository

- **Strict decoding.** An unknown field in `metadata` or `spec` is an error (`unknown_field`, with a "did you mean"
  hint). This catches typing mistakes early. The exceptions are the free-form areas: `Blueprint.spec.config`, the
  `config` of a fragment, `Fleet.spec.parameters`, `Fleet.spec.update.params`, `Inventory.spec.entries[].parameters`
  and `Executor.spec.awx.extraVars`.
- **Unique names.** Two objects of the same kind with the same name are an error (`duplicate_object`).
- **YAML features.** Anchors, aliases and merge keys (`<<`) are allowed. Tags are interpreted as in YAML 1.2.
- **No secrets in Git.** Blueprints reference secrets with the collector syntax `${env:VAR}` or `${file:/path}`. The
  collector resolves them on the host, from the environment of the supervisor service or from mounted files, and
  `myriarchd` never interprets them. Executors name their secrets by environment variable or file name that exists on
  the `myriarchd` host (`secretEnv`, `tokenFile`, and so on). The repository only names a secret. Which names `myriarchd`
  may read is decided by its own configuration, see [Executors](executors.md#secrets).
- **No symbolic links.** The repository may contain neither symbolic links nor other non-regular files
  (`not_regular_file`).
- **Limits.** A file may be at most 4 MiB, the repository at most `gitops.maxFiles` YAML files (default 2000) with
  `gitops.maxYamlBytes` in total (default 64 MiB). Parsing and validating one revision has a time budget. The limits and
  their error codes are listed in the [schema reference](../reference/gitops-schema.md#limits).

## How a revision is read and applied

1. **Fetch.** `myriarchd` polls the repository every `gitops.pollInterval` (default 60 seconds). A webhook at
   `POST /api/v1/gitops/webhook` triggers an immediate sync. It accepts the GitLab `X-Gitlab-Token` header or the GitHub
   `X-Hub-Signature-256` signature, both checked against `gitops.webhookSecret` (or `gitops.webhookSecretFile`), and it
   is rate limited.
2. **Validate the whole revision.** The parser reads all files, checks the schema, references between objects, extends
   chains, parameters, templates, selectors, policies and executors, and renders every fleet for every matching sample
   agent. This is the same code that `myrctl validate` runs, so a merge request that passes CI is accepted by the
   server (`myrctl` can additionally check component names, see the schema reference).
3. **Apply atomically.** If there is any error, nothing is applied. The previous snapshot stays in force, the sync
   state becomes `error`, the issues are stored and shown in the UI, and a notification is sent for an invalid revision.
   If the revision is valid, the new snapshot replaces the old one in a single step.
4. **Reconcile.** After a new snapshot is applied, the leader replica reassigns all agents (selectors, rendering,
   target versions), reconciles the version choices made in the UI with the new Git state, and runs the rollout planner.
   If the leader fails in between, the next sync cycle of whichever replica leads completes the work.

Warnings do not block a revision. Errors in the sync itself (for example the repository is not reachable) are recorded
with the code `sync_failed` and also keep the previous snapshot. The apply is audited as `gitops.apply` with the commit
and the added, changed and removed objects.

## Which changes cause a rollout

Applying a revision does not push anything to running collectors by itself, except for agents that have no
configuration yet. A change of a blueprint, parameter or collector version leads to a **rollout** that the planner
creates and that follows the fleet's [rollout policy](rollouts.md). See
[Fleets](fleets.md#when-does-the-offered-configuration-change) for the exact rule.
