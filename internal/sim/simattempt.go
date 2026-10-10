// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"sync"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/myriarch/myriarch/internal/brand"
)

// The update progress report of the simulated updater has exactly the form
// of the real myriarch-updater (docs/design/updater-protocol.md, Update health component):
// health component `dev.myriarch.update` with the children "version", "from"
// and "step:<name>". The literals are intentionally defined here again
// (internal/sim does not import internal/updater).
const (
	updateComponent  = brand.UpdateHealthComponent
	updateStepPrefix = "step:"

	stepDownloading = "downloading"
	stepVerifying   = "verifying"
	stepInstalling  = "installing"
	stepRestarting  = "restarting"
	stepHealthCheck = "health_check"
	stepRollingBack = "rolling_back"

	stepRunning = "running"
	stepDone    = "done"
	stepFailed  = "failed"
	stepSkipped = "skipped"

	outcomeSucceeded  = "succeeded"
	outcomeFailed     = "failed"
	outcomeRolledBack = "rolled_back"

	// attributes of the updater (README 7.3)
	attrServerEndpoint = brand.AttrUpdaterServerEndpoint
	attrInstallMethod  = brand.AttrUpdaterInstallMethod
)

var simStepOrder = []string{stepDownloading, stepVerifying, stepInstalling, stepRestarting, stepHealthCheck}

type simStep struct {
	name           string
	status         string
	started, ended time.Time
	err            string
}

// simAttempt is the latest simulated update attempt.
type simAttempt struct {
	mu        sync.Mutex
	version   string // "v0.163.0"
	from      string
	started   time.Time
	phase     string
	phaseAt   time.Time
	final     bool
	unhealthy bool
	err       string
	steps     []simStep
}

func (a *simAttempt) step(name string) *simStep {
	for i := range a.steps {
		if a.steps[i].name == name {
			return &a.steps[i]
		}
	}
	a.steps = append(a.steps, simStep{name: name})
	return &a.steps[len(a.steps)-1]
}

func newSimAttempt(version, from string, now time.Time) *simAttempt {
	return &simAttempt{version: "v" + version, from: from, started: now, phase: stepVerifying, phaseAt: now}
}

// set records the status of one step.
func (a *simAttempt) set(name, status, errText string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.final {
		return
	}
	s := a.step(name)
	switch status {
	case stepRunning:
		s.started, s.ended, s.err = now, time.Time{}, ""
		a.phase, a.phaseAt = name, now
	default:
		if s.started.IsZero() {
			s.started = now
		}
		s.ended = now
		if status == stepFailed {
			s.err = errText
		}
	}
	s.status = status
}

// end finishes the attempt; steps that never began are skipped.
func (a *simAttempt) end(outcome, errText string, unhealthy bool, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.final {
		return
	}
	for _, n := range simStepOrder {
		if s := a.step(n); s.status == "" {
			s.status, s.started, s.ended = stepSkipped, now, now
		}
	}
	a.phase, a.phaseAt, a.final, a.unhealthy, a.err = outcome, now, true, unhealthy, errText
}

func nanos(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	return uint64(t.UnixNano())
}

// component renders the attempt as ComponentHealth.
func (a *simAttempt) component() *protobufs.ComponentHealth {
	a.mu.Lock()
	defer a.mu.Unlock()
	kids := map[string]*protobufs.ComponentHealth{
		"version": {Healthy: true, Status: a.version},
		"from":    {Healthy: true, Status: a.from},
	}
	for _, s := range a.steps {
		c := &protobufs.ComponentHealth{
			Healthy: s.status != stepFailed, Status: s.status, LastError: s.err,
			StartTimeUnixNano: nanos(s.started), StatusTimeUnixNano: nanos(s.started),
		}
		if !s.ended.IsZero() {
			c.StatusTimeUnixNano = nanos(s.ended)
		}
		kids[updateStepPrefix+s.name] = c
	}
	return &protobufs.ComponentHealth{
		Healthy: !a.unhealthy, Status: a.phase, LastError: a.err,
		StartTimeUnixNano: nanos(a.started), StatusTimeUnixNano: nanos(a.phaseAt),
		ComponentHealthMap: kids,
	}
}

// snapshotSteps returns name -> status (tests).
func (a *simAttempt) snapshotSteps() map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := map[string]string{}
	for _, s := range a.steps {
		m[s.name] = s.status
	}
	return m
}
