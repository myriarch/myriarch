// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package blueprint

import (
	"strings"
	"testing"

	"github.com/myriarch/myriarch/internal/domain"
)

// The server signs every webhook delivery with its own X-Myriarch-* headers,
// so an executor may not set headers with that prefix (compared case
// insensitively).
func TestExecutorWebhookReservedHeaders(t *testing.T) {
	validate := func(header string) domain.Issues {
		snap := &domain.GitOpsSnapshot{Executors: []domain.Executor{{
			ObjectMeta: domain.ObjectMeta{Name: "hook"},
			ExecutorSpec: domain.ExecutorSpec{Type: domain.ExecutorWebhook, Webhook: &domain.WebhookExecutorSpec{
				URL: "https://ci.example.com/hook", SecretEnv: "MYRIARCH_EXEC_X",
				Headers: map[string]string{header: "x"}}},
		}}}
		return NewRenderer(Options{}).ValidateSnapshot(snap, ValidateOptions{})
	}
	reserved := func(is domain.Issues) bool {
		for _, i := range is {
			if i.Code == "executor_invalid" && strings.Contains(i.Message, "is reserved") {
				return true
			}
		}
		return false
	}
	for _, h := range []string{"X-Myriarch-Signature", "x-myriarch-delivery", "X-MYRIARCH-Timestamp", "X-Myriarch-Custom"} {
		if !reserved(validate(h)) {
			t.Errorf("header %q must be reported as reserved", h)
		}
	}
	if is := validate("X-Request-Source"); reserved(is) {
		t.Errorf("an ordinary header must be accepted: %+v", is)
	}
}
