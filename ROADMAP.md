# Roadmap

This roadmap lists what myriarch contains in its first release, what comes next, what is intended for later and what is deliberately not planned. It describes direction, not commitments: items move when contributors, users and upstream projects move. Decisions that shape the roadmap are recorded in the [decision records](docs/adr/README.md), and the current design is in [docs/architecture.md](docs/architecture.md).

Support levels apply to every item. In v0.1.0, container mode and Linux hosts are **beta**, Windows hosts are **experimental**, and the Helm chart is **experimental** ([ADR-0021](docs/adr/0021-publication-gates-and-v0-1-0-scope.md)).

## Released: v0.1.0 (alpha)

The scope of the first release is fixed by [ADR-0021](docs/adr/0021-publication-gates-and-v0-1-0-scope.md).

### Server (`myriarchd`)

- OpAMP server, agent inventory, live web UI.
- Assignment of agents to fleets, server-side rendering of blueprints, config rollouts and version rollouts with waves, approvals, maintenance windows, and pause, abort and rollback.
- Release tracking with breaking changes from the upstream changelogs, and notifications to Microsoft Teams, Slack and generic webhooks.
- The target version of a fleet can be chosen in the UI within a frame that Git declares, with live progress per target ([ADR-0013](docs/adr/0013-target-version-from-the-ui-within-the-git-frame.md)).
- Audit log, sign-in with OIDC or the bootstrap admin ([ADR-0025](docs/adr/0025-login-without-enterprise-idp.md)), roles and API tokens.
- High availability with several replicas on one PostgreSQL database.

### Agents and tools

- `myriarch-updater` for Linux hosts (systemd), Windows hosts (service) and container mode in a pod. Container mode is beta and needs a collector image that you build yourself until the collector chart of v0.2.0 (see [container mode](docs/design/container-mode.md)).
- Executors `updater`, `webhook`, `awx` and `manual`.
- `myrctl validate` and `myrctl render` for CI of the GitOps repository, and `myriarch-sim` as a load simulator.

### Delivery and project

- A Helm chart that installs with default values, Docker Compose files, installation scripts for Linux and Windows hosts.
- CI and signed multi-architecture images (`linux/amd64` and `linux/arm64`) of the server and the updater, built from the release binaries and running as user 65532 ([ADR-0023](docs/adr/0023-keyless-release-signing.md), [ADR-0024](docs/adr/0024-container-registry-and-image-set.md)).
- English README and quickstart, the community files, and the English core documentation, including tutorials for a first fleet and rollout and for Docker Compose, a guide for the installation with Helm, and generated references for the command line, the REST API, the server configuration, the environment variables and the metrics.

**Not in v0.1.0:** the signed OCI chart, the kind quickstart and the collector chart (see v0.2.0), a published collector image, a separate image of `myrctl` (the binary is in the release archives and in the server image), the `gitops-pr` executor, and the MCP server.

## Next

**v0.2.0, "Kubernetes-ready"** ([ADR-0021](docs/adr/0021-publication-gates-and-v0-1-0-scope.md))

- Publication of the Helm chart as a signed OCI artifact.
- A quickstart on kind.
- A chart for the collector.
- A production Docker Compose setup with TLS and a Git source (the Compose file of v0.1.0 is for evaluation only).

**Version updates on Kubernetes** ([ADR-0026](docs/adr/0026-collector-version-updates-on-kubernetes.md))

- The `gitops-pr` executor: an update opens a pull or merge request in the deployment repository that bumps the collector image. Completion is still decided by the version the agent reports.
- Container mode stays an explicit opt-in for air-gapped and edge clusters.

**MCP access, stage 1** ([ADR-0027](docs/adr/0027-mcp-principles.md))

- A separate binary `myriarch-mcp` over stdio, read-only, authenticated with a personal API token that has a dedicated `mcp` purpose and acting strictly as the token owner. A design record with the tool catalog comes with the implementation.

**Runtime branding** ([ADR-0022](docs/adr/0022-white-label-and-neutral-examples.md))

- Product name, logo, favicon, primary color and an environment banner set through configuration, served by the server itself without loosening the content security policy. The feature gets its own control audit.

### Documentation

- Operations guides: upgrades, backup and restore, high availability, monitoring, air-gapped installation and an artifact mirror.

## From alpha to beta

Myriarch as a whole stays **alpha** until all of the following hold. The change to beta is recorded in a decision record that replaces the support levels of [ADR-0021](docs/adr/0021-publication-gates-and-v0-1-0-scope.md), and the README, the security policy and the release notes change with it. The support level of a single component, such as Linux hosts or container mode today, can change earlier.

- **Stable interfaces.** The GitOps format moves to `myriarch.dev/v1beta1` with a documented migration from `v1alpha1`. The REST API and the configuration keys of `myriarchd` change incompatibly only in a minor version, never in a patch release, and the changelog says how to migrate.
- **A tested upgrade path.** The upgrade from the previous minor version (Helm chart, database migrations, `myriarch-updater`) is documented and tested automatically.
- **Operations guides.** Upgrades, backup and restore, high availability and monitoring are documented.
- **Real hosts.** `myriarch-updater` and the installers are tested on real Linux and Windows hosts, not only in CI.
- **Feedback from users.** Installations by people outside the maintainers have reported back, and no report of data loss is open.

## Later

- **Pull and merge requests from the UI for other changes**, for example raising a fleet's Git target version (GitLab and GitHub APIs), beyond the Kubernetes image update.
- **MCP, later stage:** an embedded `/mcp` endpoint with OAuth 2.1 and no token passthrough, and write tools after channel-independent controls exist: four-eyes approval for risky operations across UI, API, CLI and MCP, a distinct audit channel attribute, rate limits, a dry-run mode and a control audit. Wave approvals are never possible through MCP.
- **Supervisor binary updates** through `myriarch-updater` (package name `opampsupervisor`), and a supervisor-native executor once the upstream supervisor supports packages ([opentelemetry-collector-contrib #47272](https://github.com/open-telemetry/opentelemetry-collector-contrib/issues/47272)).
- **OpenTelemetry Operator integration** with the OpAMP Bridge as an agent type "cluster", and image rollouts through `spec.image`. Until then, agents identified as Bridge-managed receive no remote configuration unless explicitly opted in ([ADR-0026](docs/adr/0026-collector-version-updates-on-kubernetes.md)).
- **Compatibility check before sending a configuration:** mark an agent `Incompatible` when the components a configuration needs are missing.
- **Caches or relays near edge sites** to reduce download traffic for large fleets.
- **Own telemetry of the collectors** through the connection settings offer (`own_*`).
- **Notifications:** localized messages, more channels (for example e-mail and an incident-management service), and an event for many agents going offline at once (`agents.offline_bulk`).
- **Multi-tenancy** with namespaces.
- **Foundation path:** an application to the CNCF Sandbox once its criteria are met, among them several maintainers from different organizations and public adopters ([ADR-0028](docs/adr/0028-relationship-to-opentelemetry-and-foundation-path.md)).

## Not planned

- A Terraform provider.
- A project-owned collector distribution, or a proprietary in-process client that replaces the OpAMP Supervisor. The project builds on the upstream supervisor and the upstream collector.
- Updating the supervisor and `myriarch-updater` themselves in containers over OpAMP. Their image tag stays authoritative; the collector binary in the pod is updated by the updater in container mode ([ADR-0014](docs/adr/0014-binary-updates-in-the-pod-via-container-mode.md)).
- Editing declarative objects directly in the UI. They stay in Git ([ADR-0003](docs/adr/0003-gitops-as-source-of-truth.md)).
