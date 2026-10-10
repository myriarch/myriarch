// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package rollout

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/myriarch/myriarch/internal/domain"
)

type metrics struct {
	active      *prometheus.GaugeVec
	targets     *prometheus.GaugeVec
	transitions *prometheus.CounterVec
}

var targetStatuses = []domain.TargetStatus{
	domain.TargetPending, domain.TargetWaitingWindow, domain.TargetInProgress, domain.TargetVerifying,
	domain.TargetSucceeded, domain.TargetFailed, domain.TargetSkipped, domain.TargetCancelled, domain.TargetRolledBack,
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		active: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "myriarch_rollouts_active", Help: "Number of active (non-terminal) rollouts by kind.",
		}, []string{"kind"}),
		targets: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "myriarch_rollout_targets", Help: "Rollout targets of active rollouts by status.",
		}, []string{"status"}),
		transitions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "myriarch_rollout_transitions_total", Help: "Rollout status transitions by new status.",
		}, []string{"to"}),
	}
	for _, k := range []domain.RolloutKind{domain.RolloutKindConfig, domain.RolloutKindVersion} {
		m.active.WithLabelValues(string(k)).Set(0)
	}
	for _, s := range targetStatuses {
		m.targets.WithLabelValues(string(s)).Set(0)
	}
	if reg != nil {
		m.active = registerVec(reg, m.active)
		m.targets = registerVec(reg, m.targets)
		m.transitions = registerVec(reg, m.transitions)
	}
	return m
}

func registerVec[T prometheus.Collector](reg prometheus.Registerer, c T) T {
	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(T); ok {
				return existing
			}
		}
	}
	return c
}

// observe refreshes the gauges from the active rollouts of a tick.
func (m *metrics) observe(active []domain.Rollout) {
	kinds := map[domain.RolloutKind]int{domain.RolloutKindConfig: 0, domain.RolloutKindVersion: 0}
	st := map[domain.TargetStatus]int{}
	for _, r := range active {
		kinds[r.Kind]++
		c := r.Counts
		st[domain.TargetPending] += c.Pending
		st[domain.TargetWaitingWindow] += c.WaitingWindow
		st[domain.TargetInProgress] += c.InProgress // in_progress + verifying
		st[domain.TargetSucceeded] += c.Succeeded
		st[domain.TargetFailed] += c.Failed
		st[domain.TargetSkipped] += c.Skipped
		st[domain.TargetCancelled] += c.Cancelled
		st[domain.TargetRolledBack] += c.RolledBack
	}
	for k, n := range kinds {
		m.active.WithLabelValues(string(k)).Set(float64(n))
	}
	for _, s := range targetStatuses {
		m.targets.WithLabelValues(string(s)).Set(float64(st[s]))
	}
}
