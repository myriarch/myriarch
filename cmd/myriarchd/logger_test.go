// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/myriarch/myriarch/internal/config"
)

// TestNewLoggerEscapesControlCharacters pins the property that the log-injection
// review depends on: request-derived values are logged only as slog attributes
// next to constant messages, and the handlers that newLogger returns escape
// control characters in attribute values, so one log call is always one line.
// Only characters that change the line structure or drive a terminal are checked (not DEL).
// A future handler (an OTLP bridge, a custom formatter) has to keep this test green.
func TestNewLoggerEscapesControlCharacters(t *testing.T) {
	const hostile = "a\rb\nc\u2028d\u2029e\x1bfg\x00h"
	for _, format := range []string{"json", "text", ""} {
		t.Run("format="+format, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			orig := os.Stderr
			os.Stderr = w
			logger := newLogger(config.TelemetryConfig{LogLevel: "info", LogFormat: format})
			os.Stderr = orig
			logger.Info("constant message", "value", hostile, hostile, "key")
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(r)
			_ = r.Close()
			if err != nil {
				t.Fatal(err)
			}
			out := string(raw)
			line, ok := strings.CutSuffix(out, "\n")
			if !ok || line == "" {
				t.Fatalf("expected exactly one newline-terminated record, got %q", out)
			}
			for i, c := range line {
				if c < 0x20 || c == '\u2028' || c == '\u2029' {
					t.Fatalf("raw control character U+%04X at offset %d of %q", c, i, line)
				}
			}
			if !strings.Contains(line, "constant message") {
				t.Fatalf("record lost its message: %q", line)
			}
		})
	}
}
