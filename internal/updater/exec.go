// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// Executor runs a program (used for the preflight of the new binary) and
// returns its combined output (capped).
type Executor func(ctx context.Context, env []string, name string, args ...string) ([]byte, error)

const maxExecOutput = 1 << 20

type cappedBuffer struct {
	buf bytes.Buffer
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.buf.Len() < maxExecOutput {
		room := maxExecOutput - c.buf.Len()
		if len(p) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil // never fail the child on a full buffer
}

// osExecutor is the default Executor. env is appended to the current process
// environment (later entries win).
func osExecutor(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out cappedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if len(env) > 0 {
		cmd.Env = mergeEnv(env)
	}
	err := cmd.Run()
	if err != nil {
		return out.buf.Bytes(), fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out.buf.Bytes(), nil
}

// mergeEnv returns os.Environ() overlaid with extra (KEY=VALUE).
func mergeEnv(extra []string) []string {
	base := currentEnviron()
	idx := map[string]int{}
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		idx[envKey(k)] = len(out)
		out = append(out, kv)
	}
	for _, kv := range extra {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		if i, found := idx[envKey(k)]; found {
			out[i] = kv
			continue
		}
		idx[envKey(k)] = len(out)
		out = append(out, kv)
	}
	return out
}

// envKey normalizes environment variable names (case-insensitive on Windows).
func envKey(k string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(k)
	}
	return k
}
