// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package beta

import prom "github.com/prometheus/client_golang/prometheus"

type wrapper struct{}

func register(c prom.Collector) prom.Collector { return c }

// New registers through a helper and with an import alias; the same definition in two packages counts once.
func New() {
	register(prom.NewCounterVec(prom.CounterOpts{Name: "myriarch_sample_requests_total", Help: "Requests by route and result."}, []string{"route", "result"}))
	register(prom.NewGauge(prom.GaugeOpts{Name: "myriarch_beta_leader", Help: "Whether this replica is the leader."}))
}
