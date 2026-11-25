// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package fleetapi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/domain"
)

func TestExecutorSummaryGitOpsPR(t *testing.T) {
	cases := []struct {
		name string
		spec domain.GitOpsPRExecutorSpec
		want string
	}{
		{"github default", domain.GitOpsPRExecutorSpec{Provider: "github", Repository: "example/deploy"}, "https://api.github.com/repos/example/deploy"},
		{"github enterprise", domain.GitOpsPRExecutorSpec{Provider: "github", APIURL: "https://ghe.example.com/api/v3", Repository: "example/deploy"}, "https://ghe.example.com/api/v3/repos/example/deploy"},
		{"gitlab default", domain.GitOpsPRExecutorSpec{Provider: "gitlab", Repository: "example/group/deploy"}, "https://gitlab.com/api/v4/projects/example%2Fgroup%2Fdeploy"},
		{"gitlab self-managed", domain.GitOpsPRExecutorSpec{Provider: "gitlab", APIURL: "https://git.example.com/", Repository: "example/deploy"}, "https://git.example.com/api/v4/projects/example%2Fdeploy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := c.spec
			spec.BaseBranch, spec.Path, spec.Field = "main", "values.yaml", "image.tag"
			spec.TokenEnv, spec.TokenFile = "MYRIARCH_EXEC_SECRET_TOKEN_NAME", "secret-file-name"
			spec.AppID, spec.PrivateKeyFile = "12345", "secret-key-name"
			e := &domain.Executor{ObjectMeta: domain.ObjectMeta{Name: "pr"}, ExecutorSpec: domain.ExecutorSpec{Type: domain.ExecutorGitOpsPR, GitOpsPR: &spec}}
			sum := (&service{}).executorSummary(&domain.GitOpsSnapshot{}, e)
			assert.Equal(t, c.want, sum.Target)
			assert.Equal(t, domain.ExecutorGitOpsPR, sum.Type)

			b, err := json.Marshal(sum)
			require.NoError(t, err)
			for _, secret := range []string{"MYRIARCH_EXEC_SECRET_TOKEN_NAME", "secret-file-name", "secret-key-name", "tokenEnv", "tokenFile", "privateKeyFile"} {
				assert.NotContains(t, string(b), secret)
			}
		})
	}
}
