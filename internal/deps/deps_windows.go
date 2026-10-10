// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package deps

import (
	// Windows service control (myriarch-updater restarts the supervisor service).
	_ "golang.org/x/sys/windows"
	_ "golang.org/x/sys/windows/svc"
	_ "golang.org/x/sys/windows/svc/mgr"
)
