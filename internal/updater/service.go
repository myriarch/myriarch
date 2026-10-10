// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ServiceState is the state of an OS service.
type ServiceState int

// Service states.
const (
	ServiceUnknown ServiceState = iota
	ServiceStopped
	ServiceStarting
	ServiceRunning
	ServiceStopping
)

func (s ServiceState) String() string {
	switch s {
	case ServiceStopped:
		return "stopped"
	case ServiceStarting:
		return "starting"
	case ServiceRunning:
		return "running"
	case ServiceStopping:
		return "stopping"
	default:
		return "unknown"
	}
}

// ErrServiceNotFound is returned when the service does not exist.
var ErrServiceNotFound = errors.New("service not found")

// ServiceController controls the OS service of the supervisor (systemd unit
// on Linux, Windows service via SCM). Implementations must honor ctx.
type ServiceController interface {
	// Exists reports whether the service is installed.
	Exists(ctx context.Context, name string) (bool, error)
	// State returns the current state.
	State(ctx context.Context, name string) (ServiceState, error)
	// Stop stops the service and returns when it is stopped (error on
	// timeout). Stopping a stopped service is not an error.
	Stop(ctx context.Context, name string) error
	// Start starts the service and returns when it is running (error on
	// timeout). Starting a running service is not an error.
	Start(ctx context.Context, name string) error
	// Environment returns the environment (KEY=VALUE) the service runs with,
	// as far as it can be determined (systemd Environment/EnvironmentFile,
	// Windows service registry Environment). Used for the config preflight.
	Environment(ctx context.Context, name string) ([]string, error)
}

// ServiceUserResolver is implemented by controllers that know the account a
// service runs as ("" = root/SYSTEM). The preflight uses it to run the new
// binary with the supervisor's rights instead of the updater's.
type ServiceUserResolver interface {
	ServiceUser(ctx context.Context, name string) (string, error)
}

// FakeServiceController is an in-memory ServiceController for tests (and for
// running the updater on a developer machine without a service manager).
type FakeServiceController struct {
	mu       sync.Mutex
	services map[string]*fakeService
	calls    []string

	// Hooks (all optional); called without the lock held.
	OnStop  func(name string) error
	OnStart func(name string) error
}

type fakeService struct {
	state ServiceState
	env   []string
}

// NewFakeServiceController returns a controller with the given services
// installed and running.
func NewFakeServiceController(names ...string) *FakeServiceController {
	f := &FakeServiceController{services: map[string]*fakeService{}}
	for _, n := range names {
		f.services[n] = &fakeService{state: ServiceRunning}
	}
	return f
}

// SetState forces the state of a service (creating it if needed).
func (f *FakeServiceController) SetState(name string, s ServiceState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	svc := f.services[name]
	if svc == nil {
		svc = &fakeService{}
		f.services[name] = svc
	}
	svc.state = s
}

// SetEnvironment sets the environment returned by Environment.
func (f *FakeServiceController) SetEnvironment(name string, env []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.services[name] == nil {
		f.services[name] = &fakeService{}
	}
	f.services[name].env = env
}

// Calls returns the recorded operations ("stop:name", "start:name").
func (f *FakeServiceController) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *FakeServiceController) record(op, name string) {
	f.mu.Lock()
	f.calls = append(f.calls, op+":"+name)
	f.mu.Unlock()
}

// Exists implements ServiceController.
func (f *FakeServiceController) Exists(_ context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.services[name]
	return ok, nil
}

// State implements ServiceController.
func (f *FakeServiceController) State(_ context.Context, name string) (ServiceState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	svc, ok := f.services[name]
	if !ok {
		return ServiceUnknown, ErrServiceNotFound
	}
	return svc.state, nil
}

// Stop implements ServiceController.
func (f *FakeServiceController) Stop(_ context.Context, name string) error {
	f.record("stop", name)
	if f.OnStop != nil {
		if err := f.OnStop(name); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	svc, ok := f.services[name]
	if !ok {
		return ErrServiceNotFound
	}
	svc.state = ServiceStopped
	return nil
}

// Start implements ServiceController.
func (f *FakeServiceController) Start(_ context.Context, name string) error {
	f.record("start", name)
	if f.OnStart != nil {
		if err := f.OnStart(name); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	svc, ok := f.services[name]
	if !ok {
		return ErrServiceNotFound
	}
	svc.state = ServiceRunning
	return nil
}

// Environment implements ServiceController.
func (f *FakeServiceController) Environment(_ context.Context, name string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	svc, ok := f.services[name]
	if !ok {
		return nil, ErrServiceNotFound
	}
	return append([]string(nil), svc.env...), nil
}

// waitState polls until the service reaches want or the timeout expires.
func waitState(ctx context.Context, sc ServiceController, name string, want ServiceState, timeout, poll time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		st, err := sc.State(ctx, name)
		if err != nil {
			return err
		}
		if st == want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("service %s did not reach state %s within %s (state: %s)", name, want, timeout, st)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}
