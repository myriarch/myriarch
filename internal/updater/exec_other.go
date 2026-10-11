// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package updater

import "errors"

func canDropPrivileges() bool { return false }

func osExecutorAs(account string) (Executor, error) {
	return nil, errors.New("running as another account is not supported on this platform")
}
