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
| `myriarch_beta_leader` | gauge |  | `beta` | Whether this replica is the leader. |
| `myriarch_sample_agents` | gauge | `status` | `alpha` | Agents by status. |
| `myriarch_sample_connections` | gauge |  | `alpha` | Open connections. |
| `myriarch_sample_duration_seconds` | histogram |  | `alpha` | Duration of one run. Buckets: 0.005, 0.5, 1, 30. Series: `_bucket`, `_sum`, `_count`. |
| `myriarch_sample_events_total` | counter |  | `alpha` | Events \| handled by \<the\> sample\_package. |
| `myriarch_sample_latency_seconds` | summary |  | `alpha` | Latency of one call. Quantiles: 0.5, 0.9. Series: the quantiles, `_sum`, `_count`. |
| `myriarch_sample_requests_total` | counter | `route`, `result` | `alpha` | Requests by route and result. |
| `myriarch_sample_size_bytes` | histogram | `direction` | `alpha` | Size of one message. Buckets: 1024, 65536. Series: `_bucket`, `_sum`, `_count`. |
| `myriarch_tool_runs_total` | counter |  | `cmd/tool` | Runs of the tool. |

## myriarch-sim

| Metric | Type | Labels | Package | Description |
|---|---|---|---|---|
| `myriarch_sim_agents_connected` | gauge |  | `sim` | Simulated agents with an open connection. |
| `myriarch_sim_messages_total` | counter | `direction` | `sim` | Messages by direction. |

## Runtime metrics

The registries of `myriarchd` and `myriarch-sim` also contain the Go runtime collector (`go_*`) and the process collector (`process_*`) of the Prometheus Go client. They are not listed here.
