// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package sim

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// RaiseFileLimit raises RLIMIT_NOFILE (soft) as far as the OS allows (macOS
// defaults to 256, which is not enough for 2000 websocket connections). It
// returns the new soft limit.
func RaiseFileLimit() (uint64, error) {
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &lim); err != nil {
		return 0, fmt.Errorf("getrlimit: %w", err)
	}
	// Hard limits can be RLIM_INFINITY; macOS additionally caps the soft limit
	// at kern.maxfilesperproc (often 10240 or 61440), so try a ladder.
	candidates := []uint64{lim.Max, 1 << 20, 1 << 18, 65536, 24576, 10240, 8192, 4096}
	for _, c := range candidates {
		if c > lim.Max || c <= lim.Cur {
			continue
		}
		next := unix.Rlimit{Cur: c, Max: lim.Max}
		if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &next); err == nil {
			return c, nil
		}
	}
	return lim.Cur, nil
}
