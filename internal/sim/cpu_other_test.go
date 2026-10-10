// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package sim

import "time"

func cpuTime() time.Duration { return 0 }
