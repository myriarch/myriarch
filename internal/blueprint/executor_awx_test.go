// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package blueprint

import (
	"testing"

	"github.com/myriarch/myriarch/internal/domain"
)

// spec.awx.apiPrefix selects the controller API root (Ansible Automation
// Platform 2.5 and later: /api/controller/v2/).
func TestExecutorAWXAPIPrefix(t *testing.T) {
	validate := func(prefix string) domain.Issues {
		snap := &domain.GitOpsSnapshot{Executors: []domain.Executor{{
			ObjectMeta: domain.ObjectMeta{Name: "awx"},
			ExecutorSpec: domain.ExecutorSpec{Type: domain.ExecutorAWX, AWX: &domain.AWXExecutorSpec{
				URL: "https://awx.example.com", JobTemplateID: 7, TokenEnv: "MYRIARCH_EXEC_AWX", APIPrefix: prefix}},
		}}}
		return NewRenderer(Options{}).ValidateSnapshot(snap, ValidateOptions{})
	}
	flagged := func(is domain.Issues) bool {
		for _, i := range is {
			if i.Code == "executor_invalid" && i.Path == "spec.awx.apiPrefix" {
				return true
			}
		}
		return false
	}
	for _, p := range []string{"", "/api/v2/", "/api/controller/v2", "/api/controller/v2/"} {
		if is := validate(p); flagged(is) {
			t.Errorf("apiPrefix %q must be accepted: %+v", p, is)
		}
	}
	for _, p := range []string{"api/v2/", "/api/../v2/", "/api/./v2/", "/api//v2/", "/api/v2/?x=1", "/api/v 2/", "/", "//"} {
		if !flagged(validate(p)) {
			t.Errorf("apiPrefix %q must be rejected with executor_invalid at spec.awx.apiPrefix", p)
		}
	}
}
