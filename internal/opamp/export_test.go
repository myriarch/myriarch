// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package opamp

import "time"

// SetTimeouts shortens the connection guards for tests and returns a restore function.
func SetTimeouts(firstMessage, tokenRecheck time.Duration) (restore func()) {
	f, r := firstMessageTimeout, tokenRecheckInterval
	if firstMessage > 0 {
		firstMessageTimeout = firstMessage
	}
	if tokenRecheck > 0 {
		tokenRecheckInterval = tokenRecheck
	}
	return func() { firstMessageTimeout, tokenRecheckInterval = f, r }
}

// HTTPSessionCount is the number of pooled HTTP sessions.
func (s *Server) HTTPSessionCount() int {
	s.httpMu.Lock()
	defer s.httpMu.Unlock()
	return len(s.httpSessions)
}
