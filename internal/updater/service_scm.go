// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// scmAPI abstracts the Windows Service Control Manager so the stop/start/wait
// logic is unit-testable on every OS. The real implementation lives in
// service_windows.go (golang.org/x/sys/windows/svc/mgr).
type scmAPI interface {
	// Open opens a service; ErrServiceNotFound if it does not exist.
	Open(name string) (scmHandle, error)
	// Environment reads the service environment from the registry
	// (HKLM\SYSTEM\CurrentControlSet\Services\<name>\Environment).
	Environment(name string) ([]string, error)
}

// scmHandle is one opened service.
type scmHandle interface {
	Query() (ServiceState, error)
	RequestStop() error
	RequestStart() error
	Close() error
}

// SCMController controls Windows services via the SCM. Stop waits until the
// service is Stopped (default 60s), Start waits until it is Running.
type SCMController struct {
	api     scmAPI
	Timeout time.Duration
	Poll    time.Duration
}

func (c *SCMController) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 60 * time.Second
}

func (c *SCMController) poll() time.Duration {
	if c.Poll > 0 {
		return c.Poll
	}
	return 300 * time.Millisecond
}

// Exists implements ServiceController.
func (c *SCMController) Exists(_ context.Context, name string) (bool, error) {
	h, err := c.api.Open(name)
	if errors.Is(err, ErrServiceNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_ = h.Close()
	return true, nil
}

// State implements ServiceController.
func (c *SCMController) State(_ context.Context, name string) (ServiceState, error) {
	h, err := c.api.Open(name)
	if err != nil {
		return ServiceUnknown, err
	}
	defer func() { _ = h.Close() }()
	return h.Query()
}

func (c *SCMController) waitFor(ctx context.Context, h scmHandle, name string, want ServiceState) error {
	deadline := time.Now().Add(c.timeout())
	for {
		st, err := h.Query()
		if err != nil {
			return fmt.Errorf("query service %s: %w", name, err)
		}
		if st == want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("service %s did not reach state %s within %s (state: %s)", name, want, c.timeout(), st)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.poll()):
		}
	}
}

// Stop implements ServiceController.
func (c *SCMController) Stop(ctx context.Context, name string) error {
	h, err := c.api.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	st, err := h.Query()
	if err != nil {
		return fmt.Errorf("query service %s: %w", name, err)
	}
	switch st {
	case ServiceStopped:
		return nil
	case ServiceStopping:
	default:
		if err := h.RequestStop(); err != nil {
			return fmt.Errorf("stop service %s: %w", name, err)
		}
	}
	return c.waitFor(ctx, h, name, ServiceStopped)
}

// Start implements ServiceController.
func (c *SCMController) Start(ctx context.Context, name string) error {
	h, err := c.api.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	st, err := h.Query()
	if err != nil {
		return fmt.Errorf("query service %s: %w", name, err)
	}
	switch st {
	case ServiceRunning:
		return nil
	case ServiceStarting:
		return c.waitFor(ctx, h, name, ServiceRunning)
	case ServiceStopping:
		if err := c.waitFor(ctx, h, name, ServiceStopped); err != nil {
			return err
		}
	}
	if err := h.RequestStart(); err != nil {
		return fmt.Errorf("start service %s: %w", name, err)
	}
	return c.waitFor(ctx, h, name, ServiceRunning)
}

// Environment implements ServiceController.
func (c *SCMController) Environment(_ context.Context, name string) ([]string, error) {
	return c.api.Environment(name)
}
