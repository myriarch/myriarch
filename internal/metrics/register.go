// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"time"

	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// Register registers c on reg and returns it. If an identical collector is
// already registered (several instances sharing one registry, repeated
// construction in tests) the existing collector is returned instead, so the
// callers never panic. A nil registerer returns c unregistered.
func Register[T prometheus.Collector](reg prometheus.Registerer, c T) T {
	if reg == nil {
		return c
	}
	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(T); ok {
				return existing
			}
		}
		panic(err)
	}
	return c
}

// CycleTimer returns a function that starts timing one cycle of a background
// job and returns the function that records it in the histogram
// myriarch_job_cycle_duration_seconds{job}. Several callers share the
// histogram when they use the same registerer.
func CycleTimer(reg prometheus.Registerer) func(job string) func() {
	h := Register(reg, prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "myriarch_job_cycle_duration_seconds", Help: "Duration of one cycle of a leader job (rollout tick/plan, GitOps sync, release poll, artifact prefetch).",
		Buckets: []float64{.05, .25, 1, 5, 15, 30, 60, 120, 300, 600},
	}, []string{"job"}))
	return func(job string) func() {
		start := time.Now()
		return func() { h.WithLabelValues(job).Observe(time.Since(start).Seconds()) }
	}
}
