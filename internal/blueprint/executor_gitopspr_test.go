// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package blueprint

import (
	"testing"

	"github.com/myriarch/myriarch/internal/domain"
)

func gitHubSpec() *domain.GitOpsPRExecutorSpec {
	return &domain.GitOpsPRExecutorSpec{
		Provider: "github", Repository: "example/deploy", BaseBranch: "main",
		Path: `clusters/[[ attr "k8s.cluster.name" ]]/values.yaml`, Field: "image.tag", TokenEnv: "MYRIARCH_EXEC_GH",
	}
}

func gitLabSpec() *domain.GitOpsPRExecutorSpec {
	return &domain.GitOpsPRExecutorSpec{
		Provider: "gitlab", Repository: "example/platform/deploy", BaseBranch: "main",
		Path: "k/deploy.yml", Field: "spec.template.spec.containers[0].image", ValueFormat: "image", TokenFile: "gl-token",
	}
}

func validateExecutorObj(e domain.Executor) domain.Issues {
	return NewRenderer(Options{}).ValidateSnapshot(&domain.GitOpsSnapshot{Executors: []domain.Executor{e}}, ValidateOptions{})
}

func hasIssue(is domain.Issues, code, path string) bool {
	for _, i := range is {
		if i.Code == code && i.Path == path {
			return true
		}
	}
	return false
}

func TestExecutorGitOpsPRValidation(t *testing.T) {
	mk := func(g *domain.GitOpsPRExecutorSpec) domain.Executor {
		return domain.Executor{ObjectMeta: domain.ObjectMeta{Name: "pr"}, ExecutorSpec: domain.ExecutorSpec{Type: domain.ExecutorGitOpsPR, GitOpsPR: g}}
	}
	for name, g := range map[string]*domain.GitOpsPRExecutorSpec{"github": gitHubSpec(), "gitlab": gitLabSpec()} {
		if is := validateExecutorObj(mk(g)); len(is) != 0 {
			t.Errorf("%s: valid object has issues: %+v", name, is)
		}
	}
	app := gitHubSpec()
	app.TokenEnv, app.AppID, app.PrivateKeyFile = "", "12345", "app.pem"
	if is := validateExecutorObj(mk(app)); len(is) != 0 {
		t.Errorf("app login: %+v", is)
	}

	cases := []struct {
		name, code, path string
		edit             func(g *domain.GitOpsPRExecutorSpec)
	}{
		{"wrong provider", "executor_invalid", "spec.gitopsPr.provider", func(g *domain.GitOpsPRExecutorSpec) { g.Provider = "bitbucket" }},
		{"no provider", "missing_field", "spec.gitopsPr.provider", func(g *domain.GitOpsPRExecutorSpec) { g.Provider = "" }},
		{"apiUrl credentials", "executor_invalid", "spec.gitopsPr.apiUrl", func(g *domain.GitOpsPRExecutorSpec) { g.APIURL = "https://user:pass@ghe.example.com/api/v3" }},
		{"github three segments", "executor_invalid", "spec.gitopsPr.repository", func(g *domain.GitOpsPRExecutorSpec) { g.Repository = "a/b/c" }},
		{"no repository", "missing_field", "spec.gitopsPr.repository", func(g *domain.GitOpsPRExecutorSpec) { g.Repository = "" }},
		{"bad base branch", "executor_invalid", "spec.gitopsPr.baseBranch", func(g *domain.GitOpsPRExecutorSpec) { g.BaseBranch = "a..b" }},
		{"path escapes", "executor_invalid", "spec.gitopsPr.path", func(g *domain.GitOpsPRExecutorSpec) { g.Path = "../x.yaml" }},
		{"path absolute", "executor_invalid", "spec.gitopsPr.path", func(g *domain.GitOpsPRExecutorSpec) { g.Path = "/x.yaml" }},
		{"path template", "executor_invalid", "spec.gitopsPr.path", func(g *domain.GitOpsPRExecutorSpec) { g.Path = "[[ .fleet ]]/x.yaml" }},
		{"path extension", "executor_invalid", "spec.gitopsPr.path", func(g *domain.GitOpsPRExecutorSpec) { g.Path = "x.json" }},
		{"field grammar", "executor_invalid", "spec.gitopsPr.field", func(g *domain.GitOpsPRExecutorSpec) { g.Field = "a..b" }},
		{"no field", "missing_field", "spec.gitopsPr.field", func(g *domain.GitOpsPRExecutorSpec) { g.Field = "" }},
		{"value format", "executor_invalid", "spec.gitopsPr.valueFormat", func(g *domain.GitOpsPRExecutorSpec) { g.ValueFormat = "digest" }},
		{"two tokens", "executor_invalid", "spec.gitopsPr.", func(g *domain.GitOpsPRExecutorSpec) { g.TokenFile = "x" }},
		{"no token", "executor_invalid", "spec.gitopsPr.", func(g *domain.GitOpsPRExecutorSpec) { g.TokenEnv = "" }},
		{"appId on gitlab", "executor_invalid", "spec.gitopsPr.appId", func(g *domain.GitOpsPRExecutorSpec) {
			*g = *gitLabSpec()
			g.TokenFile, g.AppID, g.PrivateKeyFile = "", "1", "k.pem"
		}},
		{"appId without key", "executor_invalid", "spec.gitopsPr.appId", func(g *domain.GitOpsPRExecutorSpec) { g.TokenEnv, g.AppID = "", "1" }},
		{"key without appId", "executor_invalid", "spec.gitopsPr.privateKeyFile", func(g *domain.GitOpsPRExecutorSpec) { g.TokenEnv, g.PrivateKeyFile = "", "k.pem" }},
		{"label with comma", "executor_invalid", "spec.gitopsPr.labels", func(g *domain.GitOpsPRExecutorSpec) { g.Labels = []string{"a,b"} }},
		{"empty reviewer", "executor_invalid", "spec.gitopsPr.reviewers", func(g *domain.GitOpsPRExecutorSpec) { g.Reviewers = []string{""} }},
	}
	for _, c := range cases {
		g := gitHubSpec()
		c.edit(g)
		if is := validateExecutorObj(mk(g)); !hasIssue(is, c.code, c.path) {
			t.Errorf("%s: want %s at %s, got %+v", c.name, c.code, c.path, is)
		}
	}

	noBlock := mk(nil)
	if is := validateExecutorObj(noBlock); !hasIssue(is, "missing_field", "spec.gitopsPr") {
		t.Errorf("missing block: %+v", is)
	}
	foreign := mk(gitHubSpec())
	foreign.Webhook = &domain.WebhookExecutorSpec{URL: "https://ci.example.com/hook", SecretEnv: "X"}
	if is := validateExecutorObj(foreign); !hasIssue(is, "executor_invalid", "spec.webhook") {
		t.Errorf("foreign block: %+v", is)
	}
	hook := domain.Executor{ObjectMeta: domain.ObjectMeta{Name: "hook"}, ExecutorSpec: domain.ExecutorSpec{Type: domain.ExecutorWebhook,
		Webhook: &domain.WebhookExecutorSpec{URL: "https://ci.example.com/hook", SecretEnv: "X"}, GitOpsPR: gitHubSpec()}}
	if is := validateExecutorObj(hook); !hasIssue(is, "executor_invalid", "spec.gitopsPr") {
		t.Errorf("gitopsPr on webhook: %+v", is)
	}
}

func TestFleetGitOpsPRExecutorRef(t *testing.T) {
	validate := func(ref string, execs ...domain.Executor) domain.Issues {
		f := domain.Fleet{ObjectMeta: domain.ObjectMeta{Name: "edge"}}
		f.Update = domain.UpdateSpec{Executor: domain.ExecutorGitOpsPR, ExecutorRef: ref}
		return NewRenderer(Options{}).ValidateSnapshot(&domain.GitOpsSnapshot{Fleets: []domain.Fleet{f}, Executors: execs}, ValidateOptions{})
	}
	if is := validate(""); !hasIssue(is, "missing_field", "spec.update.executorRef") {
		t.Errorf("no ref: %+v", is)
	}
	hook := domain.Executor{ObjectMeta: domain.ObjectMeta{Name: "hook"}, ExecutorSpec: domain.ExecutorSpec{Type: domain.ExecutorWebhook,
		Webhook: &domain.WebhookExecutorSpec{URL: "https://ci.example.com/hook", SecretEnv: "X"}}}
	if is := validate("hook", hook); !hasIssue(is, "executor_invalid", "spec.update.executorRef") {
		t.Errorf("wrong type: %+v", is)
	}
	pr := domain.Executor{ObjectMeta: domain.ObjectMeta{Name: "pr"}, ExecutorSpec: domain.ExecutorSpec{Type: domain.ExecutorGitOpsPR, GitOpsPR: gitHubSpec()}}
	for _, i := range validate("pr", pr) {
		if i.Path == "spec.update.executorRef" || i.Path == "spec.update.executor" {
			t.Errorf("valid reference flagged: %+v", i)
		}
	}
}
