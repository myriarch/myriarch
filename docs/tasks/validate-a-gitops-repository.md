---
title: "Validate a GitOps repository"
weight: 20
description: "Check a GitOps repository and render collector configurations with myrctl, on your machine and in a CI pipeline."
---

Git is the source of truth for blueprints, fleets, rollout policies, executors and inventory. `myrctl` runs the same
validation as `myriarchd` runs before it applies a revision, so a merge request that passes `myrctl validate` is accepted
by the server. `myriarchd` accepts a state only if it is completely valid: an invalid state is never applied, the previous
one stays active, and the notification event `gitops.sync_failed` is sent.

## Get myrctl

- From a release: the archive `myrctl_<version>_<os>_<arch>` contains the binary.
- From source: `make build` creates `bin/myrctl`.

`myr` is an optional alias of `myrctl` that you create yourself (`alias myr=myrctl`); it is not shipped as a separate binary.

## Validate

Run the command from the root of your repository, or pass the directory:

```bash
myrctl validate --strict --components ci/components-otelcol-contrib-0.162.0.json config
```

`validate` loads all objects below the directory, checks them against the rules of the format, renders every fleet for the
matching sample agents in `samples/agents/` and prints one line per finding followed by a summary:

```text
fleets/dev-laptop.yaml:7: error blueprint_not_found: blueprint "nope" does not exist (Fleet/dev-laptop, spec.blueprint)
FAILED: 4 Blueprint, 0 Executor, 4 Fleet, 1 Inventory, 2 RolloutPolicy; 4 samples; 1 errors, 0 warnings
```

A successful run prints `OK:` with the same summary. Useful options:

| Option | Effect |
|---|---|
| `--strict` | Treats warnings as errors. Use it in CI. |
| `--components FILE` | A component list (JSON). It enables the `unknown_component` check for all sample renderings. Export the list of a real collector from `GET /api/v1/agents/<id>/components`, or start from `examples/sim/components-otelcol-contrib-0.162.0.json`. |
| `-o json` | Machine-readable report with `valid`, `errors`, `warnings`, the object counts and the list of issues (`severity`, `code`, `message`, `file`, `line`, `path`, `object`). |

Exit codes: `0` valid (warnings are allowed unless `--strict`), `1` validation errors, `2` usage or internal error, for
example a directory that does not exist. Every finding carries a stable code such as `blueprint_not_found` or
`unknown_component`. The [GitOps schema reference](../reference/gitops-schema.md) lists all codes with their meaning and
fix. The complete option list is in the [`myrctl validate` reference](../reference/cli/myrctl_validate.md).

## Render a configuration

`render` shows what an agent would receive. Give it a sample agent from `samples/agents/` (the file name without `.yaml`),
or describe the agent with attributes:

```bash
myrctl render --dir examples/quickstart --sample linux-edge-01
myrctl render --dir examples/quickstart --sample linux-edge-01 --param collectJournald=true --param 'logPaths=[/var/log/syslog]'
myrctl render --dir examples/quickstart --attr site.type=edge --attr os.type=linux --label wave=canary
myrctl render --dir examples/quickstart --sample windows-edge-01 -o json
```

Without `--fleet` the fleet is chosen like `myriarchd` does, by selectors and priority; inventory entries of the repository
are applied. `--param key=value` overrides a fleet parameter for the rendering. With `-o json` the output has the fleet,
the blueprint layers, the parameters with their sources, the content hash of the rendered configuration and the issues.
Exit codes: `0` ok, `1` render errors, `2` usage or internal error. See the
[`myrctl render` reference](../reference/cli/myrctl_render.md).

Sample agents imitate real hosts: the attributes that the supervisor and the installer report (`site.*`, `os.type`,
`host.name`, `host.arch`, `k8s.*` on Kubernetes) and the server labels that would come from the enrollment token. Add one
sample agent for every kind of host, so that every fleet is rendered at least once.

## Run it in CI

The directory [gitops-template/](../../gitops-template/README.md) contains a pipeline for GitLab CI,
[`.gitlab-ci.yml`](../../gitops-template/.gitlab-ci.yml), that you copy into your own GitOps repository. It has three jobs:

1. `validate` runs `myrctl validate --strict --components ...` twice, once with `--output json` for an artifact and once
   for the job log, and fails the pipeline on exit code `1`.
2. `render` renders the configuration of every sample agent and publishes the files as the artifact "Rendered configs".
   Reviewers read the rendered difference in the merge request, not only the blueprint source.
3. `notify-myriarchd` runs on the default branch only and calls the webhook `POST /api/v1/gitops/webhook`, so that
   `myriarchd` syncs at once instead of at its next poll.

Any other CI system needs only the validate step:

```bash
myrctl version
myrctl validate config --strict --components ci/components-otelcol-contrib-0.162.0.json --output json > validate.json
```

Keep these rules in mind for the pipeline:

- Run it on merge requests and require it to pass before merging into the protected default branch.
- Keep CI files outside the directory that `myriarchd` reads (`gitops.path`): the server reads every YAML file below it
  as an object.
- Never put secrets into the repository. Collector secrets are references such as `${env:NAME}`; executor secrets are
  environment variable names.

Read [GitOps](../concepts/gitops.md) for the layout and sync rules and [Blueprints](../concepts/blueprints.md) for the
rendering rules that `validate` checks. The next step is to [roll out a configuration change](roll-out-a-config-change.md).
