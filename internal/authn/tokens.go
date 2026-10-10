// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn

import (
	"crypto/rand"
	"fmt"
)

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// randomBase62 returns n uniformly distributed characters from crypto/rand
// (rejection sampling, no modulo bias).
func randomBase62(n int) (string, error) {
	out := make([]byte, 0, n)
	buf := make([]byte, n*2)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("random: %w", err)
		}
		for _, b := range buf {
			if b >= 248 { // 248 = 62*4: values above would bias the distribution
				continue
			}
			out = append(out, base62[int(b)%62])
			if len(out) == n {
				break
			}
		}
	}
	return string(out), nil
}

// NewToken generates "<prefix><32 base62 characters>".
func NewToken(prefix string) (string, error) {
	r, err := randomBase62(32)
	if err != nil {
		return "", err
	}
	return prefix + r, nil
}

// displayPrefix is the first 12 characters of a token (shown in the UI).
func displayPrefix(token string) string {
	if len(token) <= 12 {
		return token
	}
	return token[:12]
}
