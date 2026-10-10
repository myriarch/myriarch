// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type simMetrics struct {
	reg               *prometheus.Registry
	agentsConnected   prometheus.Gauge
	updatersConnected prometheus.Gauge
	configsApplied    prometheus.Counter
	configsFailed     prometheus.Counter
	updates           *prometheus.CounterVec
	restarts          prometheus.Counter
	connectFailures   prometheus.Counter
	agentsTotal       prometheus.Gauge
}

func newSimMetrics(reg *prometheus.Registry) *simMetrics {
	m := &simMetrics{
		reg: reg,
		agentsConnected: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "myriarch_sim_agents_connected", Help: "Simulated OpAMP connections currently established (collectors and updaters)."}),
		updatersConnected: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "myriarch_sim_updaters_connected", Help: "Simulated myriarch-updater connections currently established."}),
		configsApplied: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "myriarch_sim_configs_applied_total", Help: "Remote configs applied (APPLIED)."}),
		configsFailed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "myriarch_sim_configs_failed_total", Help: "Remote configs that failed (FAILED)."}),
		updates: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "myriarch_sim_updates_total", Help: "Simulated package installs by result (installed|failed)."}, []string{"result"}),
		restarts: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "myriarch_sim_restarts_total", Help: "Simulated collector restarts (command, upgrade, failed update)."}),
		connectFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "myriarch_sim_connect_failures_total", Help: "Failed connection attempts."}),
		agentsTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "myriarch_sim_agents_total", Help: "Number of simulated collectors."}),
	}
	reg.MustRegister(m.agentsConnected, m.updatersConnected, m.configsApplied, m.configsFailed,
		m.updates, m.restarts, m.connectFailures, m.agentsTotal,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	// make the result series exist from the start
	m.updates.WithLabelValues("installed")
	m.updates.WithLabelValues("failed")
	return m
}

func newMetricsHandler(m *simMetrics) http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
