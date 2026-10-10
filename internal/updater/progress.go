// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/myriarch/myriarch/internal/brand"
)

// Contract constants for the update progress report (docs/design/updater-protocol.md,
// Update health component). They are deliberately defined here again and
// not imported from internal/domain: myriarchd matches the same literals.
const (
	// UpdateComponent is the key of the update attempt in the updater's
	// ComponentHealth.component_health_map.
	UpdateComponent = brand.UpdateHealthComponent
	// updateStepPrefix prefixes the per-step children ("step:downloading").
	updateStepPrefix = "step:"
	// updateChildVersion / updateChildFrom carry the offered and the
	// previously active version (both with a leading "v").
	updateChildVersion = "version"
	updateChildFrom    = "from"

	maxAttemptErrorLen = 1000
)

// Update steps in their fixed order. rolling_back only appears when the
// attempt had to be rolled back.
const (
	StepDownloading = "downloading"
	StepVerifying   = "verifying"
	StepInstalling  = "installing"
	StepRestarting  = "restarting"
	StepHealthCheck = "health_check"
	StepRollingBack = "rolling_back"
)

// Step statuses (a step that has not begun is simply absent from the report).
const (
	StepRunning = "running"
	StepDone    = "done"
	StepFailed  = "failed"
	StepSkipped = "skipped"
)

// Attempt outcomes (final phases).
const (
	OutcomeSucceeded  = "succeeded"
	OutcomeFailed     = "failed"
	OutcomeRolledBack = "rolled_back"
)

var stepOrder = []string{StepDownloading, StepVerifying, StepInstalling, StepRestarting, StepHealthCheck, StepRollingBack}

// Progress receives the steps of one update attempt from the Installer. A
// nil Progress switches reporting off.
type Progress interface {
	// Begin starts a new attempt for the offered version (without "v"); from
	// is the active version before the attempt ("" if none).
	Begin(version, from string)
	// Step records the status of one step (one of the Step* constants).
	Step(name, status string, err error)
	// End finishes the attempt: succeeded, failed or rolled_back. A failed
	// attempt is only unhealthy when healthy is false (the rollback failed or
	// was impossible, so the host may be broken).
	End(outcome string, err error, healthy bool)
}

// stepRecord is one step of an attempt.
type stepRecord struct {
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
	Error    string    `json:"error,omitempty"`
}

// attemptRecord is the latest update attempt as reported in the health tree
// and persisted in <stateDir>/last-attempt.json.
type attemptRecord struct {
	Version   string       `json:"version"` // "v0.162.0"
	From      string       `json:"from"`    // "v0.161.0" or ""
	Started   time.Time    `json:"started"`
	Phase     string       `json:"phase"` // running step name, or a final outcome
	PhaseAt   time.Time    `json:"phaseAt"`
	Final     bool         `json:"final"`
	Unhealthy bool         `json:"unhealthy,omitempty"`
	Error     string       `json:"error,omitempty"`
	Steps     []stepRecord `json:"steps"`
}

// attemptTracker implements Progress, keeps the latest attempt and
// persists it. It is safe for concurrent use.
type attemptTracker struct {
	mu       sync.Mutex
	cur      *attemptRecord
	path     string // "" = do not persist
	now      func() time.Time
	onChange func() // called without the lock held
}

var _ Progress = (*attemptTracker)(nil)

func lastAttemptPath(stateDir string) string { return filepath.Join(stateDir, "last-attempt.json") }

// newAttemptTracker restores the previous attempt from stateDir. An attempt
// that was cut off by a restart is closed as failed ("interrupted").
func newAttemptTracker(stateDir string, now func() time.Time, onChange func()) *attemptTracker {
	t := &attemptTracker{now: now, onChange: onChange}
	if stateDir == "" {
		return t // no persistence
	}
	t.path = lastAttemptPath(stateDir)
	data, err := os.ReadFile(t.path)
	if err != nil {
		return t
	}
	var a attemptRecord
	if json.Unmarshal(data, &a) != nil || a.Version == "" {
		return t // unreadable: start without history
	}
	t.cur = &a
	if !a.Final {
		t.interruptLocked()
		t.persistLocked()
	}
	return t
}

func (t *attemptTracker) interruptLocked() {
	a := t.cur
	ts := t.now().UTC()
	msg := "myriarch-updater restarted while the update was in progress (interrupted)"
	for i := range a.Steps {
		if a.Steps[i].Status == StepRunning {
			a.Steps[i].Status, a.Steps[i].Finished, a.Steps[i].Error = StepFailed, ts, msg
		}
	}
	a.Phase, a.PhaseAt, a.Final, a.Error = OutcomeFailed, ts, true, msg
}

// clip limits an error text for the report: single line, bounded.
func clipError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > maxAttemptErrorLen {
		s = s[:maxAttemptErrorLen-3] + "..."
	}
	return s
}

func (t *attemptTracker) persistLocked() {
	if t.path == "" || t.cur == nil {
		return
	}
	data, err := json.Marshal(t.cur)
	if err != nil {
		return
	}
	_ = writeFileAtomic(t.path, data, 0o600) // best effort: the report works without it
}

func (t *attemptTracker) changed() {
	if t.onChange != nil {
		t.onChange()
	}
}

// Begin implements Progress.
func (t *attemptTracker) Begin(version, from string) {
	ts := t.now().UTC()
	a := &attemptRecord{Version: "v" + version, Started: ts, Phase: StepVerifying, PhaseAt: ts}
	if from != "" {
		a.From = "v" + from
	}
	t.mu.Lock()
	t.cur = a
	t.persistLocked()
	t.mu.Unlock()
	t.changed()
}

func (a *attemptRecord) step(name string) *stepRecord {
	for i := range a.Steps {
		if a.Steps[i].Name == name {
			return &a.Steps[i]
		}
	}
	a.Steps = append(a.Steps, stepRecord{Name: name})
	return &a.Steps[len(a.Steps)-1]
}

// Step implements Progress.
func (t *attemptTracker) Step(name, status string, err error) {
	ts := t.now().UTC()
	t.mu.Lock()
	if t.cur == nil || t.cur.Final {
		t.mu.Unlock()
		return
	}
	a := t.cur
	s := a.step(name)
	switch status {
	case StepRunning:
		s.Started, s.Finished, s.Error = ts, time.Time{}, ""
		a.Phase, a.PhaseAt = name, ts
	case StepDone, StepSkipped:
		if s.Started.IsZero() {
			s.Started = ts
		}
		s.Finished = ts
	case StepFailed:
		if s.Started.IsZero() {
			s.Started = ts
		}
		s.Finished, s.Error = ts, clipError(err)
	}
	s.Status = status
	t.persistLocked()
	t.mu.Unlock()
	t.changed()
}

// End implements Progress. Steps that never began are reported as skipped
// (except rolling_back, which only exists when a rollback ran).
func (t *attemptTracker) End(outcome string, err error, healthy bool) {
	ts := t.now().UTC()
	t.mu.Lock()
	if t.cur == nil || t.cur.Final {
		t.mu.Unlock()
		return
	}
	a := t.cur
	for _, n := range stepOrder {
		if n == StepRollingBack {
			continue
		}
		if s := a.step(n); s.Status == "" {
			s.Status, s.Started, s.Finished = StepSkipped, ts, ts
		}
	}
	for i := range a.Steps {
		if a.Steps[i].Status == StepRunning {
			// A step still running at the end: done on success, else it did not complete.
			a.Steps[i].Finished = ts
			if outcome == OutcomeSucceeded {
				a.Steps[i].Status = StepDone
			} else {
				a.Steps[i].Status = StepFailed
				if err != nil {
					a.Steps[i].Error = clipError(err)
				}
			}
		}
	}
	a.Phase, a.PhaseAt, a.Final = outcome, ts, true
	a.Unhealthy = outcome == OutcomeFailed && !healthy
	if outcome == OutcomeSucceeded {
		a.Error = ""
	} else {
		a.Error = clipError(err)
	}
	t.persistLocked()
	t.mu.Unlock()
	t.changed()
}

// snapshot returns a copy of the latest attempt (nil if none).
func (t *attemptTracker) snapshot() *attemptRecord {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cur == nil {
		return nil
	}
	c := *t.cur
	c.Steps = append([]stepRecord(nil), t.cur.Steps...)
	return &c
}

func unixNano(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	return uint64(t.UnixNano())
}

func childStatus(status string) *protobufs.ComponentHealth {
	return &protobufs.ComponentHealth{Healthy: true, Status: status}
}

// healthComponent renders the attempt in the form myriarchd reads (README 7.2).
func (a *attemptRecord) healthComponent() *protobufs.ComponentHealth {
	children := map[string]*protobufs.ComponentHealth{
		updateChildVersion: childStatus(a.Version),
		updateChildFrom:    childStatus(a.From),
	}
	for _, s := range a.Steps {
		c := &protobufs.ComponentHealth{
			Healthy:            s.Status != StepFailed,
			Status:             s.Status,
			StartTimeUnixNano:  unixNano(s.Started),
			StatusTimeUnixNano: unixNano(s.Started),
			LastError:          s.Error,
		}
		if !s.Finished.IsZero() {
			c.StatusTimeUnixNano = unixNano(s.Finished)
		}
		children[updateStepPrefix+s.Name] = c
	}
	return &protobufs.ComponentHealth{
		Healthy:            !a.Unhealthy,
		Status:             a.Phase,
		LastError:          a.Error,
		StartTimeUnixNano:  unixNano(a.Started),
		StatusTimeUnixNano: unixNano(a.PhaseAt),
		ComponentHealthMap: children,
	}
}

// installRun tracks one Install call for the Progress reporting: the step
// that is currently running and the outcome decided by the swap/rollback
// logic. All methods are no-ops when no Progress is configured.
type installRun struct {
	p       Progress
	stage   string // currently running step
	outcome string // set by the swap/rollback code; "" = derive from the result
	healthy bool   // only meaningful for outcome failed
}

func (in *Installer) newRun() *installRun { return &installRun{p: in.Progress, healthy: true} }

// begin starts a step; a previously running step must have been closed by done.
func (r *installRun) begin(name string) {
	r.stage = name
	if r.p != nil {
		r.p.Step(name, StepRunning, nil)
	}
}

func (r *installRun) done(name string) {
	if r.stage == name {
		r.stage = ""
	}
	if r.p != nil {
		r.p.Step(name, StepDone, nil)
	}
}

func (r *installRun) skip(names ...string) {
	if r.p == nil {
		return
	}
	for _, n := range names {
		r.p.Step(n, StepSkipped, nil)
	}
}

func (r *installRun) fail(name string, err error) {
	if name == "" {
		return
	}
	if r.stage == name {
		r.stage = ""
	}
	if r.p != nil {
		r.p.Step(name, StepFailed, err)
	}
}

// finish closes the attempt from Install's result.
func (r *installRun) finish(err error) {
	if r.p == nil {
		return
	}
	switch {
	case err == nil:
		r.p.End(OutcomeSucceeded, nil, true)
	case r.outcome != "":
		r.p.End(r.outcome, err, r.healthy)
	default:
		if r.stage != "" {
			r.p.Step(r.stage, StepFailed, err)
		}
		r.p.End(OutcomeFailed, err, true)
	}
}
