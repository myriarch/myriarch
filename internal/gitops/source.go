// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package gitops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	gogitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/netguard"
)

// fetched is a local copy of the repository at one revision.
type fetched struct {
	// fsys is rooted at the checkout (os.Root): paths cannot leave it, also not
	// through symbolic links.
	fsys fs.FS
	// root is the GitOps path inside fsys.
	root     string
	revision string
	commit   domain.GitCommit
	// closeFn releases the checkout handle.
	closeFn func()
}

// release closes the checkout handle; call it when the revision is processed.
func (f *fetched) release() {
	if f != nil && f.closeFn != nil {
		f.closeFn()
		f.closeFn = nil
	}
}

func newSource(cfg config.GitOpsConfig, policy *netguard.Policy) (source, error) {
	repo := strings.TrimSpace(cfg.RepoURL)
	if repo == "" {
		return nil, errors.New("gitops.repoUrl is required (or set gitops.enabled=false)")
	}
	if strings.HasPrefix(repo, "dir:") {
		p := strings.TrimPrefix(repo, "dir:")
		p = strings.TrimPrefix(p, "//") // dir:///abs/path -> /abs/path
		if p == "" {
			return nil, errors.New("gitops.repoUrl dir: needs a path")
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", p, err)
		}
		return &dirSource{dir: abs, path: cfg.Path, display: "dir:" + p}, nil
	}
	return newGitSource(cfg, policy)
}

// ------------------------------------------------------------- dir source --

type dirSource struct {
	dir     string
	path    string
	display string
}

func (d *dirSource) describe() string { return d.display }

func (d *dirSource) fetch(ctx context.Context) (*fetched, error) {
	st, err := os.Stat(d.dir)
	if err != nil {
		return nil, fmt.Errorf("gitops directory: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("gitops directory %q is not a directory", d.dir)
	}
	rootHandle, err := os.OpenRoot(d.dir)
	if err != nil {
		return nil, fmt.Errorf("open gitops directory: %w", err)
	}
	fsys := rootHandle.FS()
	rev, newest, err := dirRevision(ctx, fsys)
	if err != nil {
		rootHandle.Close()
		return nil, err
	}
	root := d.path
	if root == "" {
		root = "."
	}
	return &fetched{fsys: fsys, root: root, revision: rev, closeFn: func() { rootHandle.Close() },
		commit: domain.GitCommit{SHA: rev, Author: "directory", Message: "plain directory source " + d.display, Time: newest}}, nil
}

// dirRevision hashes all paths and contents (excluding dot directories).
func dirRevision(ctx context.Context, fsys fs.FS) (string, time.Time, error) {
	var files, symlinks []string
	err := fs.WalkDir(fsys, ".", func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if de.IsDir() {
			if p != "." && strings.HasPrefix(de.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if notRegular(de) {
			// never read: the parser reports it; the name still counts for the revision
			symlinks = append(symlinks, p)
			return nil
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("walk gitops directory: %w", err)
	}
	sort.Strings(files)
	sort.Strings(symlinks)
	h := sha256.New()
	for _, l := range symlinks {
		fmt.Fprintf(h, "link:%d:%s\n", len(l), l)
	}
	var newest time.Time
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return "", time.Time{}, err
		}
		data, err := fs.ReadFile(fsys, f)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("read %s: %w", f, err)
		}
		if info, err := fs.Stat(fsys, f); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		fmt.Fprintf(h, "%d:%s\n%d\n", len(f), f, len(data))
		h.Write(data)
	}
	if newest.IsZero() {
		newest = time.Now()
	}
	return "dir:" + hex.EncodeToString(h.Sum(nil)), newest.UTC(), nil
}

// -------------------------------------------------------------- git source --

type gitSource struct {
	url      string
	branch   string
	path     string
	dir      string
	auth     transport.AuthMethod
	caBundle string
	// tokenFile/username configure HTTPS basic auth (read per fetch).
	tokenFile string
	username  string
	// maxRepoBytes limits the size of the clone directory (0 = no limit).
	maxRepoBytes int64
	// proxy routes SSH connections through the destination policy (zero =
	// direct connection).
	proxy transport.ProxyOptions
}

func newGitSource(cfg config.GitOpsConfig, policy *netguard.Policy) (*gitSource, error) {
	g := &gitSource{url: cfg.RepoURL, branch: cfg.Branch, path: cfg.Path, dir: cfg.CloneDir, caBundle: cfg.Auth.CAFile, maxRepoBytes: cfg.MaxRepoBytes}
	if g.branch == "" {
		g.branch = "main"
	}
	if g.dir == "" {
		sum := sha256.Sum256([]byte(cfg.RepoURL))
		g.dir = filepath.Join(os.TempDir(), brand.ServerBinary+"-gitops-"+hex.EncodeToString(sum[:6]))
	}
	isSSH, isHTTP, isFile := classifyRepo(cfg.RepoURL)
	switch {
	case isFile:
	case isSSH:
		if cfg.Auth.SSHKeyFile == "" {
			return nil, errors.New("ssh repository requires gitops.auth.sshKeyFile")
		}
		if cfg.Auth.KnownHostsFile == "" {
			return nil, errors.New("ssh repository requires gitops.auth.knownHostsFile (host key verification is mandatory)")
		}
		user := "git"
		if u, err := url.Parse(cfg.RepoURL); err == nil && u.User != nil && u.User.Username() != "" {
			user = u.User.Username()
		} else if i := strings.Index(cfg.RepoURL, "@"); i > 0 && !strings.Contains(cfg.RepoURL, "://") {
			user = cfg.RepoURL[:i]
		}
		keys, err := gitssh.NewPublicKeysFromFile(user, cfg.Auth.SSHKeyFile, "")
		if err != nil {
			return nil, fmt.Errorf("load ssh key: %w", err)
		}
		cb, err := gitssh.NewKnownHostsCallback(cfg.Auth.KnownHostsFile)
		if err != nil {
			return nil, fmt.Errorf("load known_hosts: %w", err)
		}
		keys.HostKeyCallback = cb
		g.auth = keys
		// every SSH connection goes through the outbound destination policy
		if !policy.Permissive() {
			g.proxy = sshGuardOptions(policy)
		}
	case isHTTP:
		if cfg.Auth.TokenFile != "" {
			// The token file is read per fetch (rotation); validate once here.
			if _, err := os.ReadFile(cfg.Auth.TokenFile); err != nil {
				return nil, fmt.Errorf("read git token file: %w", err)
			}
		}
	default:
		return nil, fmt.Errorf("unsupported repository URL %q (use https://, ssh://, git@host:path or dir:)", redactRepoURL(cfg.RepoURL))
	}
	g.tokenFile, g.username = cfg.Auth.TokenFile, cfg.Auth.Username
	return g, nil
}

// classifyRepo tells the kind of a Git repository URL: SSH (ssh:// or the
// scp-like git@host:path), HTTP(S) or file:// (local mirrors and tests, needs
// the git binary).
func classifyRepo(repo string) (isSSH, isHTTP, isFile bool) {
	isSSH = strings.HasPrefix(repo, "ssh://") || (!strings.Contains(repo, "://") && strings.Contains(repo, "@") && strings.Contains(repo, ":"))
	isHTTP = strings.HasPrefix(repo, "https://") || strings.HasPrefix(repo, "http://")
	isFile = strings.HasPrefix(repo, "file://")
	return isSSH, isHTTP, isFile
}

// CheckConfig reports why NewSyncer would reject the repository settings (the
// URL kind, the credentials an SSH source needs). Unless skipFiles is set it
// also reads the credential files (token, SSH key, known_hosts, CA), like
// NewSyncer does. It does not touch the network.
func CheckConfig(cfg config.GitOpsConfig, skipFiles bool) error {
	if !cfg.Enabled {
		return nil
	}
	repo := strings.TrimSpace(cfg.RepoURL)
	if repo == "" {
		return domain.SettingError("gitops.repoUrl", "is required (or set gitops.enabled=false)")
	}
	if strings.HasPrefix(repo, "dir:") {
		if strings.TrimPrefix(strings.TrimPrefix(repo, "dir:"), "//") == "" {
			return domain.SettingError("gitops.repoUrl", "dir: needs a path")
		}
		return nil
	}
	isSSH, isHTTP, isFile := classifyRepo(repo)
	switch {
	case isFile:
	case isSSH:
		if cfg.Auth.SSHKeyFile == "" {
			return domain.SettingError("gitops.auth.sshKeyFile", "is required for an ssh repository")
		}
		if cfg.Auth.KnownHostsFile == "" {
			return domain.SettingError("gitops.auth.knownHostsFile", "is required for an ssh repository (host key verification is mandatory)")
		}
		if !skipFiles {
			if _, err := gitssh.NewPublicKeysFromFile("git", cfg.Auth.SSHKeyFile, ""); err != nil {
				return domain.SettingError("gitops.auth.sshKeyFile", fmt.Sprintf("cannot load the ssh key: %v", pathError(err)))
			}
			if _, err := gitssh.NewKnownHostsCallback(cfg.Auth.KnownHostsFile); err != nil {
				return domain.SettingError("gitops.auth.knownHostsFile", fmt.Sprintf("cannot load known_hosts: %v", pathError(err)))
			}
		}
	case isHTTP:
		if cfg.Auth.TokenFile != "" && !skipFiles {
			b, err := os.ReadFile(cfg.Auth.TokenFile)
			if err != nil {
				return domain.SettingError("gitops.auth.tokenFile", fmt.Sprintf("cannot read %s: %v", cfg.Auth.TokenFile, pathError(err)))
			}
			if len(strings.TrimSpace(string(b))) == 0 {
				return domain.SettingError("gitops.auth.tokenFile", "file "+cfg.Auth.TokenFile+" is empty")
			}
		}
	default:
		return domain.SettingError("gitops.repoUrl", fmt.Sprintf("unsupported repository URL %q (use https://, ssh://, git@host:path or dir:)", redactRepoURL(repo)))
	}
	if cfg.Auth.CAFile != "" && !skipFiles {
		b, err := os.ReadFile(cfg.Auth.CAFile)
		if err != nil {
			return domain.SettingError("gitops.auth.caFile", fmt.Sprintf("cannot read %s: %v", cfg.Auth.CAFile, pathError(err)))
		}
		if _, err := netguard.AppendRoots(nil, b); err != nil {
			return domain.SettingError("gitops.auth.caFile", fmt.Sprintf("file %s: %v", cfg.Auth.CAFile, err))
		}
	}
	return nil
}

// pathError removes the file path from an *os.PathError (the message names the
// setting instead).
func pathError(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

func (g *gitSource) describe() string { return redactRepoURL(g.url) }

func (g *gitSource) authMethod() (transport.AuthMethod, error) {
	if g.auth != nil {
		return g.auth, nil
	}
	if g.tokenFile == "" {
		return nil, nil
	}
	b, err := os.ReadFile(g.tokenFile)
	if err != nil {
		return nil, fmt.Errorf("read git token file: %w", err)
	}
	user := g.username
	if user == "" {
		user = "oauth2"
	}
	return &githttp.BasicAuth{Username: user, Password: strings.TrimSpace(string(b))}, nil
}

func (g *gitSource) fetch(ctx context.Context) (*fetched, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	auth, err := g.authMethod()
	if err != nil {
		return nil, err
	}
	var ca []byte
	if g.caBundle != "" {
		if ca, err = os.ReadFile(g.caBundle); err != nil {
			return nil, fmt.Errorf("read git CA bundle: %w", err)
		}
	}
	refName := plumbing.NewBranchReferenceName(g.branch)
	remoteRef := plumbing.NewRemoteReferenceName("origin", g.branch)

	repo, err := git.PlainOpen(g.dir)
	if err == nil && !g.originMatches(repo) {
		// The clone belongs to another repository (gitops.repoUrl changed or the directory was
		// prepared elsewhere): never fetch from or apply it.
		if rerr := os.RemoveAll(g.dir); rerr != nil {
			return nil, fmt.Errorf("remove clone of another repository: %w", rerr)
		}
		err = git.ErrRepositoryNotExists
	}
	switch {
	case err == nil:
		ferr := repo.FetchContext(ctx, &git.FetchOptions{
			RemoteName: "origin", RemoteURL: g.url,
			RefSpecs: []gogitconfig.RefSpec{gogitconfig.RefSpec(fmt.Sprintf("+%s:%s", refName, remoteRef))},
			Auth:     auth, CABundle: ca, Force: true, Tags: git.NoTags, Depth: 1, ProxyOptions: g.proxy,
		})
		if ferr != nil && !errors.Is(ferr, git.NoErrAlreadyUpToDate) {
			return nil, fmt.Errorf("git fetch %s: %w", redactRepoURL(g.url), ferr)
		}
	case errors.Is(err, git.ErrRepositoryNotExists):
		repo, err = g.clone(ctx, auth, ca, refName)
		if err != nil {
			return nil, err
		}
	default:
		// Unreadable local clone: start over.
		if rerr := os.RemoveAll(g.dir); rerr != nil {
			return nil, fmt.Errorf("remove broken clone: %w", rerr)
		}
		repo, err = g.clone(ctx, auth, ca, refName)
		if err != nil {
			return nil, err
		}
	}

	ref, err := repo.Reference(remoteRef, true)
	if err != nil {
		_ = os.RemoveAll(g.dir) // re-clone on the next poll
		return nil, fmt.Errorf("branch %q not found in %s: %w", g.branch, redactRepoURL(g.url), err)
	}
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		_ = os.RemoveAll(g.dir)
		return nil, fmt.Errorf("read commit %s: %w", ref.Hash(), err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("worktree: %w", err)
	}
	if g.maxRepoBytes > 0 {
		// checked before the working tree is written
		if size, err := dirSize(g.dir); err == nil && size > g.maxRepoBytes {
			_ = os.RemoveAll(g.dir)
			return nil, fmt.Errorf("the repository clone exceeds gitops.maxRepoBytes (%d bytes)", g.maxRepoBytes)
		}
	}
	if err := wt.Reset(&git.ResetOptions{Commit: ref.Hash(), Mode: git.HardReset}); err != nil {
		_ = os.RemoveAll(g.dir)
		return nil, fmt.Errorf("reset to %s: %w", ref.Hash(), err)
	}
	_ = wt.Clean(&git.CleanOptions{Dir: true})
	if g.maxRepoBytes > 0 {
		if size, err := dirSize(g.dir); err == nil && size > g.maxRepoBytes {
			_ = os.RemoveAll(g.dir)
			return nil, fmt.Errorf("the repository exceeds gitops.maxRepoBytes (%d bytes)", g.maxRepoBytes)
		}
	}
	rootHandle, err := os.OpenRoot(g.dir)
	if err != nil {
		return nil, fmt.Errorf("open checkout: %w", err)
	}

	root := g.path
	if root == "" {
		root = "."
	}
	msg := strings.TrimSpace(commit.Message)
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return &fetched{
		fsys: rootHandle.FS(), closeFn: func() { rootHandle.Close() }, root: root, revision: ref.Hash().String(),
		commit: domain.GitCommit{SHA: ref.Hash().String(), Author: commit.Author.Name, Message: msg, Time: commit.Author.When.UTC()},
	}, nil
}

// originMatches reports whether the remote "origin" of an existing clone is the
// configured repository (the clone always follows gitops.repoUrl).
func (g *gitSource) originMatches(repo *git.Repository) bool {
	remote, err := repo.Remote("origin")
	if err != nil {
		return false
	}
	urls := remote.Config().URLs
	return len(urls) == 1 && urls[0] == g.url
}

func (g *gitSource) clone(ctx context.Context, auth transport.AuthMethod, ca []byte, refName plumbing.ReferenceName) (*git.Repository, error) {
	if err := os.MkdirAll(filepath.Dir(g.dir), 0o750); err != nil {
		return nil, fmt.Errorf("create clone parent dir: %w", err)
	}
	repo, err := git.PlainCloneContext(ctx, g.dir, false, &git.CloneOptions{
		URL: g.url, RemoteName: "origin", ReferenceName: refName, SingleBranch: true,
		Auth: auth, CABundle: ca, Tags: git.NoTags, Depth: 1, ProxyOptions: g.proxy,
	})
	if err != nil {
		_ = os.RemoveAll(g.dir)
		return nil, fmt.Errorf("git clone %s: %w", redactRepoURL(g.url), err)
	}
	return repo, nil
}

// dirSize returns the total size of all regular files below dir.
func dirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total, err
}
