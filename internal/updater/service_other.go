// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package updater

import "time"

// NewServiceController returns the systemd based controller (Linux). On
// other platforms it still returns the systemctl controller; operations fail
// with a clear error when systemctl is not available.
func NewServiceController() ServiceController {
	return &SystemdController{Timeout: 60 * time.Second}
}
