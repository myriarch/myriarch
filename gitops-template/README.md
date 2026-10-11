# Myriarch GitOps repository (generic starter)

This directory is the starting point for **your own GitOps repository**. myriarchd reads all declarative objects from it
(blueprints, fleets, rollout policies, executors, inventory). Git is the source of truth; the web UI only displays
them. The object format is described in `docs/reference/gitops-schema.md` of the myriarch repository.

The starter is deliberately small and generic: three fleets (`linux-hosts`, `windows-hosts`, `k8s-gateway`), one
optional AWX fleet, a canary-then-all rollout policy for configuration changes and one for version changes. Replace
the fleets, selectors, gateway endpoints and site IDs with your own; keep the structure.

## Layout

```text
.gitlab-ci.yml                 pipeline: validate -> render -> notify-myriarchd (GitLab variant; GitHub follows later)
ci/components-otelcol-contrib-0.162.0.json   component list (check "unknown_component")
config/                        <- gitops.path of the myriarchd configuration
  blueprints/                  base, linux-host, windows-host, k8s-gateway
  fleets/                      linux-hosts, linux-hosts-awx (optional), windows-hosts, k8s-gateway
  policies/                    canary-then-all (config rollouts), version-canary-then-all (version rollouts)
  executors/                   awx-linux (optional; the host awx.example.com is a placeholder)
  inventory/                   waves (canary wave assignment by site.id)
  samples/agents/              sample agents for myrctl (not myriarchd objects)
```

> **Important:** myriarchd reads **every** `*.yaml`/`*.yml` file below `config/` as a GitOps object (except directories
> with a leading dot and `config/samples/`). That is why `.gitlab-ci.yml` and `ci/` live in the **root** and never
> under `config/`. An additional YAML file in `config/` (a CI file or a note, for example) makes the whole state
> invalid - myriarchd then does not accept it.

## Fleets and assignment

| Fleet | Matches | Blueprint | Collector | Update path |
|---|---|---|---|---|
| `linux-hosts` | `os.type=linux`, `site.type` datacenter or office | `linux-host` | 0.161.0 | myriarch-updater |
| `linux-hosts-awx` (optional) | like above + `labels.update-via=awx` | `linux-host` | 0.161.0 | Ansible AWX |
| `windows-hosts` | `os.type=windows` | `windows-host` | 0.161.0 | myriarch-updater |
| `k8s-gateway` | `site.type=k8s-gateway` | `k8s-gateway` | 0.162.0 | none (image digest) |

* **Server labels** (for example `update-via` or a staging label) are set by the **enrollment token**
  (UI *Settings -> Enrollment tokens*) or by the inventory; a host cannot claim them for itself. Self-reported
  attributes (`os.type`, `site.*`) are fine for coarse selection, not for security-relevant decisions.
* **Waves** of rollouts use the inventory label `wave` (`inventory/waves.yaml`): the wave `canary` contains the
  first host of each type; after approval in the UI the rest follows.
* Two fleets with the same priority that match the same agent leave it unassigned (`fleet_ambiguous`). Always give
  new fleets a unique priority when their selectors can overlap (the higher priority wins).

## Start your own GitOps repository

1. Create an **empty** repository (no README or ignore file, default branch `main`) on your Git server.
2. Copy the **contents** of this directory (not the directory itself) into the root of the new repository:

   ```bash
   cp -R gitops-template/. /path/to/your-gitops-repo/
   cd /path/to/your-gitops-repo
   git add -A && git commit -m "Start from the myriarch GitOps starter" && git push origin main
   ```

3. **Protect the branch**: `main` is protected and nobody pushes to it directly; everything goes through merge
   requests with a green pipeline and at least one approval. The protection is also required for the CI variable
   `MYRIARCH_GITOPS_WEBHOOK_SECRET` (it is available on protected branches only).
4. **CI/CD variables** (*Settings -> CI/CD -> Variables*):

   | Variable | Value | Options |
   |---|---|---|
   | `MYRCTL_IMAGE` | `registry.example.com/myriarch/myrctl:<version>`: an image of your own with `myrctl`, `sh` and `curl`, built from the `myrctl` release archive (the project publishes no `myrctl` image) | - (placeholder default in `.gitlab-ci.yml`) |
   | `MYRIARCH_URL` | `https://fleet.example.com` | - |
   | `MYRIARCH_GITOPS_WEBHOOK_SECRET` | content of `gitops-webhook-secret` in the myriarchd Secret (`openssl rand -hex 32`) | **masked**, **protected** |
   | `MYRIARCH_CA_FILE` | PEM of your CA | type **File**, only needed if the runner/image does not trust the CA |

   The runner must be able to reach your registry and pull the `myrctl` image you built.
5. **Read-only token for myriarchd**: create a deploy token with the scope **`read_repository`** only. User name and
   token belong into the myriarchd configuration (`gitops.auth.username` and the Secret key `git-token`, see
   `deploy/examples/production`). Never write the token into this repository.
6. Check in myriarchd: `GET /api/v1/gitops/status` shows `state: ok` and the fleets appear in the UI under *Fleets*.

## Sync: webhook, CI job, polling

myriarchd picks up changes in three ways; the first hit wins and duplicate triggers are harmless:

1. **CI job `notify-myriarchd`** (default in this starter): after every successful run on `main` the pipeline calls
   `POST $MYRIARCH_URL/api/v1/gitops/webhook` with the header `X-Gitlab-Token`. **HTTP 202** is expected. Advantage:
   validation has run before, and the Git server needs no webhook setting.
2. **Native webhook** of your Git server (alternative): URL `https://fleet.example.com/api/v1/gitops/webhook`,
   secret token = content of `gitops-webhook-secret`, trigger "push events" for the branch `main`, TLS verification on.
   The Git server must reach myriarchd and trust its certificate. Then remove the job `notify-myriarchd` from
   `.gitlab-ci.yml`, otherwise there are two triggers (harmless, but unnecessary).
3. **Polling** (fallback): myriarchd polls the repository itself every 60 s. Without a webhook a change takes up to one
   minute.

myriarchd accepts a state **only if it is completely valid**. An invalid state on `main` is never applied, the old one
stays active, and the notification channel reports `gitops.sync_failed`. The pipeline should prevent that
beforehand; `myrctl` and myriarchd use the same code.

## Working with the repository

```text
branch -> change in config/ -> merge request -> pipeline (validate, render) -> review -> merge to main
      -> notify-myriarchd -> myriarchd syncs -> planner creates rollouts -> waves/approval in the UI
```

* In the merge request the link **"Rendered configs"** (artifact `rendered/`) shows the collector config per sample
  agent. Reviewers check the difference to the previous state, not only the blueprint source.
* A change to a blueprint, fleet parameters or the inventory creates one **config rollout** per affected fleet
  (`canary-then-all`: canary wave, then approval in the UI under *Rollouts*, then the rest). A changed
  `spec.collector.version` creates a **version rollout** (`version-canary-then-all`).
* **Never change config and version in the same merge request.** If a version update restarts the collector while
  the config target of a host is in its probation time, the config target fails.
* A rollback (`onFailure: rollback` or manual) contradicts the Git state: the agent shows `drift` until Git is
  reset or corrected (new commit).
* `git revert` is the normal way to take a change back.

### Typical changes

| Goal | File | Change |
|---|---|---|
| Change the gateway address | `fleets/*.yaml` | `parameters.gatewayEndpoint` |
| Host metric interval | `fleets/linux-hosts.yaml` | `parameters.hostMetricsInterval: 60s` |
| Enable journald/log files | `fleets/linux-hosts.yaml` | `parameters.collectJournald: true`, `logPaths: [...]` (host preparation: see the header of `blueprints/linux-host.yaml`) |
| Collector version | `fleets/*.yaml` | `collector.version: 0.162.0` (a version rollout only, no config change in the same merge request) |
| Backend at the gateway | `fleets/k8s-gateway.yaml` | `parameters.backendEndpoint: https://...` |
| Put a host into the canary wave | `inventory/waves.yaml` | entry with `labels: {wave: canary}` |
| New fleet | `fleets/` | own name, **own priority**, a matching sample agent in `config/samples/agents/` |

## Rules for blueprints (GIT-4)

* **Never put lists or maps directly into an interpolation.** `"http://[[ .params.hosts ]]/x"` is a
  `template_error`. Either write the whole value as one expression (`"[[ .params.hosts ]]"` - stays a list) or
  convert it: `[[ join "," .params.hosts ]]`, `[[ toJson .params.hosts ]]`, `[[ str .params.hosts ]]`.
* Values that start with `[[` go **in quotes** (otherwise YAML reads a list: `unquoted_template`).
* Always quote number-like strings: `site.id: "0001"`, labels `wave: "1"`.
* Lists are **replaced completely** when merging, not extended. Whoever changes `service.pipelines.*.receivers`
  writes the complete list (see the disjoint fragments in `blueprints/linux-host.yaml`).
* Every component must be in the component list and used in a pipeline or extension list
  (`unknown_component`/`unused_component`). The pipeline runs with `--strict`, warnings are errors.
* No secrets in Git: collector secrets as `${env:NAME}`/`${file:/path}` (resolved by the collector on the host),
  executor secrets by name only (`tokenEnv: MYRIARCH_EXEC_AWX_TOKEN`).

## Validate locally

With the `myrctl` of a release (archive `myrctl_<version>_<os>_<arch>`), from the repository root:

```bash
myrctl validate --strict --components ci/components-otelcol-contrib-0.162.0.json config      # exit 0 = valid
myrctl render --dir config --sample linux-01                                                  # rendered config
myrctl render --dir config --sample linux-01 --param collectJournald=true --param 'logPaths=[/var/log/syslog]'
myrctl render --dir config --sample windows-01 -o json | head                                 # fleet, layers, hash, issues
```

`myrctl render --sample <name>` takes the file name without `.yaml` (= the field `name`). New sample agents imitate
a real host: attributes as the supervisor and installer report them (`site.*`, `os.type`, `host.name`, `host.arch`,
`k8s.*` on Kubernetes) and `labels` as they would come from the enrollment token. Every fleet needs at least one
matching sample agent, and every sample agent must match a fleet (otherwise `sample_unassigned`, an error with
`--strict`).

## Switch the collector version

The component list belongs to one collector version. When you change the target version:

1. Export the list of the new version from an agent (`GET /api/v1/agents/<id>/components`) or take it from the code
   project (`examples/sim/components-otelcol-contrib-<version>.json`).
2. Update `ci/components-...json` and the variable `COMPONENTS` in `.gitlab-ci.yml`.
3. Only then set the fleets to the new version.
