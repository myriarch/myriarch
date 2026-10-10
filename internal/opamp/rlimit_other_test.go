// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package opamp_test

import "testing"

func raiseNoFile(t *testing.T, want uint64) {}
