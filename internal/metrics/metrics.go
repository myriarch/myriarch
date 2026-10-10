// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package metrics provides the Prometheus registry of myriarchd.
//
// Metric naming: myriarch_<area>_<name>_<unit>, e.g.
// myriarch_opamp_connections (gauge), myriarch_opamp_messages_total{direction},
// myriarch_agents{status,kind} (gauge, refreshed by the leader),
// myriarch_rollouts_active (gauge), myriarch_gitops_sync_total{result},
// myriarch_eventbus_dropped_total, myriarch_http_requests_total{route,method,code}.
// Every package registers its own collectors on the Registerer passed in.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewRegistry returns a registry with Go runtime and process collectors.
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return reg
}

// Handler serves the registry in the Prometheus text format.
func Handler(reg *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg})
}
