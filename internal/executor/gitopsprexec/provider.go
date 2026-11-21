// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitopsprexec

import (
	"context"

	"github.com/myriarch/myriarch/internal/domain"
)

// remoteRequest is a pull or merge request as the provider reports it.
type remoteRequest struct {
	ID          string // number (GitHub) or iid (GitLab)
	Ref         string // github-pr:<owner>/<repo>#<n> or gitlab-mr:<project>!<iid>
	URL         string
	Head        string // source branch
	State       domain.ChangeRequestState
	MergeCommit string
}

// provider is the part of a Git hosting API that the executor needs.
type provider interface {
	// headSHA returns the commit at the head of a branch.
	headSHA(ctx context.Context, branch string) (sha string, exists bool, err error)
	// readFile reads a file at a ref (branch); at most 1 MiB.
	readFile(ctx context.Context, ref, path string) (content []byte, found bool, err error)
	// commit writes files in one commit onto branch. A missing branch is
	// created from createFrom (a branch name); otherwise the commit goes on
	// the head of the branch.
	commit(ctx context.Context, branch, base, createFrom, msg string, files map[string][]byte) (sha string, err error)
	// findRequest returns the latest request with this head branch, in any state.
	findRequest(ctx context.Context, head string) (*remoteRequest, error)
	openRequest(ctx context.Context, head, base, title, body string, labels, reviewers []string) (*remoteRequest, error)
	getRequest(ctx context.Context, id string) (*remoteRequest, error)
	closeRequest(ctx context.Context, id, comment string) error
	deleteBranch(ctx context.Context, branch string) error
}
