// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package alpha

import "github.com/prometheus/client_golang/prometheus"

// Metrics registers the collectors of the sample package.
func Metrics() []prometheus.Collector {
	return []prometheus.Collector{
		prometheus.NewCounter(prometheus.CounterOpts{
			Name: "myriarch_sample_events_total", Help: "Events | handled by <the> sample_package.",
		}),
		prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "myriarch_sample_requests_total",
			Help: "Requests by route and result.",
		}, []string{"route", "result"}),
		prometheus.NewGauge(prometheus.GaugeOpts{Name: "myriarch_sample_connections", Help: "Open connections."}),
		prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "myriarch_sample_agents", Help: "Agents by status."}, []string{"status"}),
		prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "myriarch_sample_duration_seconds",
			Help:    "Duration of one run.",
			Buckets: []float64{.005, 0.5, 1, 30},
		}),
		prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "myriarch_sample_size_bytes",
			Help:    "Size of one message.",
			Buckets: []float64{1024, 65536},
		}, []string{"direction"}),
		prometheus.NewSummary(prometheus.SummaryOpts{
			Name:       "myriarch_sample_latency_seconds",
			Help:       "Latency of one call.",
			Objectives: map[float64]float64{0.9: 0.01, 0.5: 0.05},
		}),
	}
}

// Ignored is no metric definition.
func Ignored() *prometheus.Registry { return prometheus.NewRegistry() }
