// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package blueprint

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/myriarch/myriarch/internal/domain"
)

var (
	gitHubRepoRE     = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	gitLabSegmentRE  = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	gitOpsAttrExprRE = regexp.MustCompile(`\[\[\s*attr\s+"[^"]+"\s*\]\]`)
	gitOpsFieldRE    = regexp.MustCompile(`^[A-Za-z0-9_-]+(\[[0-9]+\])*(\.[A-Za-z0-9_-]+(\[[0-9]+\])*)*$`)
	digitsRE         = regexp.MustCompile(`^[0-9]+$`)
)

// checkGitOpsPR validates the gitopsPr block of an Executor. report adds an
// issue below spec.gitopsPr.
func checkGitOpsPR(g *domain.GitOpsPRExecutorSpec, report func(code, msg, field string)) {
	switch g.Provider {
	case domain.GitOpsPRProviderGitHub, domain.GitOpsPRProviderGitLab:
	case "":
		report("missing_field", "provider is required (github or gitlab)", "provider")
	default:
		report("executor_invalid", fmt.Sprintf("provider %q is not supported (github or gitlab)", g.Provider), "provider")
	}

	switch {
	case g.Repository == "":
		report("missing_field", "repository is required", "repository")
	case g.Provider == domain.GitOpsPRProviderGitHub && !gitHubRepoRE.MatchString(g.Repository):
		report("executor_invalid", "repository must be owner/name for provider github", "repository")
	case g.Provider == domain.GitOpsPRProviderGitLab && !validGitLabProject(g.Repository):
		report("executor_invalid", "repository must be a project path with at least two segments for provider gitlab", "repository")
	}

	switch b := g.BaseBranch; {
	case b == "":
		report("missing_field", "baseBranch is required", "baseBranch")
	case strings.ContainsAny(b, " \t\n~^:?*[\\") || strings.Contains(b, "..") || strings.HasPrefix(b, "-") || strings.HasPrefix(b, "/"):
		report("executor_invalid", "baseBranch is not a valid branch name", "baseBranch")
	}

	checkGitOpsPath(g.Path, report)

	switch {
	case g.Field == "":
		report("missing_field", "field is required", "field")
	case !gitOpsFieldRE.MatchString(g.Field):
		report("executor_invalid", "field must be a dotted path such as spec.template.spec.containers[0].image", "field")
	}

	switch g.ValueFormat {
	case "", domain.ValueFormatTag, domain.ValueFormatImage:
	default:
		report("executor_invalid", fmt.Sprintf("valueFormat %q is not supported (tag or image)", g.ValueFormat), "valueFormat")
	}

	n := 0
	for _, s := range []string{g.TokenEnv, g.TokenFile} {
		if s != "" {
			n++
		}
	}
	if g.AppID != "" || g.PrivateKeyFile != "" {
		n++
	}
	if n != 1 {
		report("executor_invalid", "set exactly one of tokenEnv, tokenFile or appId with privateKeyFile", "")
	}
	if g.AppID != "" {
		if g.Provider != domain.GitOpsPRProviderGitHub {
			report("executor_invalid", "appId is only supported for provider github", "appId")
		}
		if !digitsRE.MatchString(g.AppID) {
			report("executor_invalid", "appId must be the numeric GitHub App ID", "appId")
		}
		if g.PrivateKeyFile == "" {
			report("executor_invalid", "appId needs privateKeyFile", "appId")
		}
	} else if g.PrivateKeyFile != "" {
		report("executor_invalid", "privateKeyFile needs appId", "privateKeyFile")
	}

	for _, l := range []struct {
		field string
		list  []string
	}{{"labels", g.Labels}, {"reviewers", g.Reviewers}} {
		for i, s := range l.list {
			if strings.TrimSpace(s) == "" || strings.Contains(s, ",") {
				report("executor_invalid", fmt.Sprintf("%s[%d] must not be empty or contain a comma", l.field, i), l.field)
			}
		}
	}
}

func validGitLabProject(p string) bool {
	segs := strings.Split(p, "/")
	if len(segs) < 2 {
		return false
	}
	for _, s := range segs {
		if !gitLabSegmentRE.MatchString(s) || s == "." || s == ".." {
			return false
		}
	}
	return true
}

func checkGitOpsPath(p string, report func(code, msg, field string)) {
	if p == "" {
		report("missing_field", "path is required", "path")
		return
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		report("executor_invalid", "path must be relative to the repository root and use /", "path")
	}
	rest := gitOpsAttrExprRE.ReplaceAllString(p, "x")
	if strings.Contains(rest, "[[") {
		report("executor_invalid", `path may only contain [[ attr "key" ]] expressions`, "path")
	}
	for _, seg := range strings.Split(rest, "/") {
		if seg == ".." {
			report("executor_invalid", `path must not contain ".." segments`, "path")
			break
		}
	}
	if !strings.HasSuffix(p, ".yaml") && !strings.HasSuffix(p, ".yml") {
		report("executor_invalid", "path must end in .yaml or .yml", "path")
	}
}
