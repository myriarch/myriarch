// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import "github.com/prometheus/client_golang/prometheus"

// New defines the metrics of the simulator.
func New() {
	prometheus.NewGauge(prometheus.GaugeOpts{Name: "myriarch_sim_agents_connected", Help: "Simulated agents with an open connection."})
	prometheus.NewCounterVec(prometheus.CounterOpts{Name: "myriarch_sim_messages_total", Help: "Messages by direction."}, []string{"direction"})
}
