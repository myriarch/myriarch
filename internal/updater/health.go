// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HealthChecker decides whether the supervisor service and the collector are
// healthy after a restart.
type HealthChecker struct {
	Services       ServiceController
	ServiceName    string
	SupervisorURL  string // optional; GET must return 200
	CollectorURL   string // optional; GET must return 200
	HTTPClient     *http.Client
	Interval       time.Duration // between probes
	Successes      int           // consecutive successful probes required
	Timeout        time.Duration // overall limit
	RequestTimeout time.Duration // per HTTP probe
}

// probe runs one round of checks.
func (h *HealthChecker) probe(ctx context.Context) error {
	st, err := h.Services.State(ctx, h.ServiceName)
	if err != nil {
		return fmt.Errorf("service %s: %w", h.ServiceName, err)
	}
	if st != ServiceRunning {
		return fmt.Errorf("service %s is %s", h.ServiceName, st)
	}
	if h.SupervisorURL != "" {
		if err := h.httpOK(ctx, h.SupervisorURL); err != nil {
			return fmt.Errorf("supervisor health: %w", err)
		}
	}
	if h.CollectorURL != "" {
		if err := h.httpOK(ctx, h.CollectorURL); err != nil {
			return fmt.Errorf("collector health: %w", err)
		}
	}
	return nil
}

func (h *HealthChecker) httpOK(ctx context.Context, url string) error {
	rt := h.RequestTimeout
	if rt <= 0 {
		rt = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, rt)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := h.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return nil
}

// WaitHealthy blocks until Successes consecutive probes passed or the timeout
// expired. A failed probe resets the counter, so a crash-looping service never
// passes.
func (h *HealthChecker) WaitHealthy(ctx context.Context) error {
	need := h.Successes
	if need < 1 {
		need = 1
	}
	interval := h.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(h.Timeout)
	ok := 0
	var last error
	for {
		err := h.probe(ctx)
		if err == nil {
			ok++
			if ok >= need {
				return nil
			}
		} else {
			ok = 0
			last = err
			if errors.Is(ctx.Err(), context.Canceled) {
				return ctx.Err()
			}
		}
		if !time.Now().Before(deadline) {
			if last == nil {
				last = fmt.Errorf("only %d of %d consecutive probes succeeded", ok, need)
			}
			return fmt.Errorf("not healthy within %s: %w", h.Timeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// describe summarizes what is checked (for logs).
func (h *HealthChecker) describe() string {
	var parts []string
	parts = append(parts, "service "+h.ServiceName+" running")
	if h.SupervisorURL != "" {
		parts = append(parts, "supervisor "+h.SupervisorURL)
	}
	if h.CollectorURL != "" {
		parts = append(parts, "collector "+h.CollectorURL)
	}
	return strings.Join(parts, ", ")
}
