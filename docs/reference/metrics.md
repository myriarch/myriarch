---
title: "Metrics"
weight: 70
description: "The Prometheus metrics of the server and the simulator, with type, labels and help text."
---

This page is generated from the metric definitions in the Go sources by `make docs-gen`. Do not edit it by hand: `make docs-check` fails when it differs from the code.

## Endpoints

- `myriarchd` serves `/metrics` in the Prometheus text format. The address is `server.metricsListenAddr` (default `":9090"`). When it is empty, `/metrics` is served on `server.listenAddr`, which is what `myriarchd dev` does. `telemetry.metrics` switches the endpoint off. The endpoint has no authentication: restrict access to it with a network policy or a firewall. The Helm chart exposes it as the service port `metrics` (9090) and creates a `ServiceMonitor` when `serviceMonitor.enabled` is `true`.
- `myriarch-sim` serves its metrics on the address of the flag `--metrics-listen`; without the flag it serves none.
- `myriarch-updater` and `myrctl` serve no metrics.
- The gauges `myriarch_agents` and `myriarch_config_sync` are computed by the leader replica only. Aggregate them across replicas with `max()`.

## myriarchd

| Metric | Type | Labels | Package | Description |
|---|---|---|---|---|
| `myriarch_agents` | gauge | `status`, `kind` | `opamp` | Agents by status and kind (refreshed by the leader). |
| `myriarch_artifact_downloads_total` | counter | `result` | `artifact` | Artifact downloads served to agents by result. |
| `myriarch_assignment_duration_seconds` | histogram | `result` | `assignment` | Duration of fleet matching and rendering for one agent. Buckets: 0.001, 0.0025, 0.005, 0.01, 0.02, 0.05, 0.1, 0.25, 1. Series: `_bucket`, `_sum`, `_count`. |
| `myriarch_auth_agent_requests_total` | counter | `result` | `authn` | Agent (OpAMP) authentication results. |
| `myriarch_auth_logins_total` | counter | `method`, `result` | `authn` | User login attempts by method and result. |
| `myriarch_auth_token_failures_total` | counter | `reason` | `authn` | Rejected API token authentications by reason. |
| `myriarch_config_sync` | gauge | `state` | `opamp` | Agents by config sync state (refreshed by the leader). |
| `myriarch_eventbus_dropped_total` | counter | `subscriber` | `eventbus/pgbus` | Events dropped because a local subscriber was too slow. |
| `myriarch_gitops_last_success_timestamp_seconds` | gauge |  | `gitops` | Unix time of the last successfully applied or confirmed GitOps sync. |
| `myriarch_gitops_sync_total` | counter | `result` | `gitops` | GitOps sync attempts by result (applied, unchanged, invalid, error). |
| `myriarch_housekeeping_deleted_total` | counter | `job` | `housekeeping` | Rows deleted by housekeeping jobs. |
| `myriarch_housekeeping_failures_total` | counter | `job` | `housekeeping` | Failed housekeeping job runs. |
| `myriarch_housekeeping_last_run_timestamp_seconds` | gauge |  | `housekeeping` | Unix time of the last completed housekeeping run. |
| `myriarch_http_request_duration_seconds` | histogram | `route`, `method` | `httpserver` | HTTP request duration by route and method. Buckets: 0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30. Series: `_bucket`, `_sum`, `_count`. |
| `myriarch_http_requests_total` | counter | `route`, `method`, `code` | `httpserver` | HTTP requests by route, method and status code. |
| `myriarch_install_plans_total` | counter | `result` | `fleetapi` | Requests for an install plan by result (ok, a refusal reason such as fleet\_mismatch or preparing, unauthorized, rate\_limited, invalid\_request). |
| `myriarch_job_cycle_duration_seconds` | histogram | `job` | `metrics` | Duration of one cycle of a leader job (rollout tick/plan, GitOps sync, release poll, artifact prefetch). Buckets: 0.05, 0.25, 1, 5, 15, 30, 60, 120, 300, 600. Series: `_bucket`, `_sum`, `_count`. |
| `myriarch_leader` | gauge |  | `leader/pglock` | 1 while this replica holds the leader lock, else 0. |
| `myriarch_notifications_total` | counter | `channel`, `result` | `notify/channels` | Notification deliveries by channel and result. |
| `myriarch_opamp_config_reoffers_total` | counter | `reason` | `opamp` | Remote configs offered to an agent again although it reported the hash, by reason (stale\_applying: the agent kept reporting APPLYING after a reconnect or restart). |
| `myriarch_opamp_connect_rejected_total` | counter | `reason` | `opamp` | Rejected OpAMP connection attempts by reason. |
| `myriarch_opamp_connections` | gauge | `transport` | `opamp` | Live OpAMP connections on this replica by transport. |
| `myriarch_opamp_dropped_total` | counter | `queue` | `opamp` | Internal queue overflows (events, bus publications, pushes). |
| `myriarch_opamp_message_duration_seconds` | histogram |  | `opamp` | Processing time of one AgentToServer message. Buckets: 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10. Series: `_bucket`, `_sum`, `_count`. |
| `myriarch_opamp_messages_total` | counter | `direction` | `opamp` | OpAMP messages by direction (in: from agents, out: replies and pushes). |
| `myriarch_opamp_push_errors_total` | counter |  | `opamp` | Failed or timed out pushes to agents. |
| `myriarch_release_latest_info` | gauge | `distribution`, `version` | `release` | Latest stable upstream release per distribution (value 1). |
| `myriarch_release_polls_total` | counter | `result` | `release` | Release tracker polls by result (ok, not\_modified, error, rate\_limited). |
| `myriarch_rollout_targets` | gauge | `status` | `rollout` | Rollout targets of active rollouts by status. |
| `myriarch_rollout_transitions_total` | counter | `to` | `rollout` | Rollout status transitions by new status. |
| `myriarch_rollouts_active` | gauge | `kind` | `rollout` | Number of active (non-terminal) rollouts by kind. |

## myriarch-sim

| Metric | Type | Labels | Package | Description |
|---|---|---|---|---|
| `myriarch_sim_agents_connected` | gauge |  | `sim` | Simulated OpAMP connections currently established (collectors and updaters). |
| `myriarch_sim_agents_total` | gauge |  | `sim` | Number of simulated collectors. |
| `myriarch_sim_configs_applied_total` | counter |  | `sim` | Remote configs applied (APPLIED). |
| `myriarch_sim_configs_failed_total` | counter |  | `sim` | Remote configs that failed (FAILED). |
| `myriarch_sim_connect_failures_total` | counter |  | `sim` | Failed connection attempts. |
| `myriarch_sim_restarts_total` | counter |  | `sim` | Simulated collector restarts (command, upgrade, failed update). |
| `myriarch_sim_updaters_connected` | gauge |  | `sim` | Simulated myriarch-updater connections currently established. |
| `myriarch_sim_updates_total` | counter | `result` | `sim` | Simulated package installs by result (installed\|failed). |

## Runtime metrics

The registries of `myriarchd` and `myriarch-sim` also contain the Go runtime collector (`go_*`) and the process collector (`process_*`) of the Prometheus Go client. They are not listed here.
