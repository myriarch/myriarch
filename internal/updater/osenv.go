// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"crypto/sha256"
	"hash"
	"os"
)

// currentEnviron is os.Environ, indirected for tests.
var currentEnviron = os.Environ

func newSHA256() hash.Hash { return sha256.New() }
