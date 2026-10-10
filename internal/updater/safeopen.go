// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"fmt"
	"io"
)

// readRegularFile reads a file that is written by a less privileged account
// (the supervisor's service user): it never follows a symlink in the last
// path component, never blocks on a FIFO or device, and is bounded in size.
// The updater runs as root/SYSTEM, so such files are untrusted input.
func readRegularFile(path string, maxBytes int64) ([]byte, error) {
	f, err := openNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if st.Size() > maxBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxBytes)
	}
	return data, nil
}

// maxSupervisorFileBytes bounds the supervisor files read by the updater
// (persistent_state.yaml, effective.yaml).
const maxSupervisorFileBytes = 4 << 20
