// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package updater

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// NewServiceController returns the Windows SCM based controller.
func NewServiceController() ServiceController {
	return &SCMController{api: winSCM{}}
}

type winSCM struct{}

func (winSCM) Open(name string) (scmHandle, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, fmt.Errorf("connect to service control manager: %w", err)
	}
	s, err := m.OpenService(name)
	if err != nil {
		_ = m.Disconnect()
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil, ErrServiceNotFound
		}
		return nil, fmt.Errorf("open service %s: %w", name, err)
	}
	return &winHandle{m: m, s: s}, nil
}

func (winSCM) Environment(name string) ([]string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\`+name, registry.QUERY_VALUE)
	if err != nil {
		return nil, fmt.Errorf("open service registry key: %w", err)
	}
	defer k.Close()
	v, _, err := k.GetStringsValue("Environment")
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read service Environment: %w", err)
	}
	return v, nil
}

type winHandle struct {
	m *mgr.Mgr
	s *mgr.Service
}

func (h *winHandle) Query() (ServiceState, error) {
	st, err := h.s.Query()
	if err != nil {
		return ServiceUnknown, err
	}
	switch st.State {
	case svc.Stopped:
		return ServiceStopped, nil
	case svc.StartPending:
		return ServiceStarting, nil
	case svc.StopPending:
		return ServiceStopping, nil
	case svc.Running, svc.ContinuePending:
		return ServiceRunning, nil
	default:
		return ServiceUnknown, nil
	}
}

func (h *winHandle) RequestStop() error {
	_, err := h.s.Control(svc.Stop)
	return err
}

func (h *winHandle) RequestStart() error { return h.s.Start() }

func (h *winHandle) Close() error {
	err := h.s.Close()
	_ = h.m.Disconnect()
	return err
}
