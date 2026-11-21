// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package webhookexec

import (
	"net/url"

	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
)

// Access applies the allow list of executors.allowedWebhookHosts and the
// secret rules of executors.secrets, executors.secretDir and
// executors.secretEnvPrefix. It is shared with the gitops-pr executor so that
// all HTTP executors apply the same allow list and secret rules.
type Access struct{ e *exec }

// NewAccess returns an Access for the executor configuration. getenv reads
// environment variables (nil: the process environment).
func NewAccess(cfg config.ExecutorsConfig, getenv func(string) string) *Access {
	e := newBase(domain.ExecutorWebhook, cfg, nil, nil)
	if getenv != nil {
		e.getenv = getenv
	}
	return &Access{e: e}
}

// HostAllowed checks the host of u against executors.allowedWebhookHosts. It
// fails closed: an empty list refuses every host.
func (a *Access) HostAllowed(u *url.URL) error { return a.e.hostAllowed(u) }

// Secret resolves a secret reference (an environment variable name or a file
// name) for use against the destination u. Error messages name the reference,
// never the value.
func (a *Access) Secret(env, file, what string, u *url.URL) ([]byte, error) {
	return a.e.secret(env, file, what, u)
}
