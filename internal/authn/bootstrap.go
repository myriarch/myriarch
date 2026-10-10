// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/httpserver/realip"
	"github.com/myriarch/myriarch/internal/secretfile"
	"github.com/myriarch/myriarch/internal/store"
)

// The bootstrap admin is one administrator account whose credentials come from
// files in the server configuration (auth.bootstrapAdmin): a bcrypt hash of a
// password and/or a one-time token. It exists for the first sign-in, before
// single sign-on is configured, and as a break-glass account afterwards.
//
// The configuration is the only source of truth. EnsureBootstrapAdmin
// reconciles the account at startup (create, enable, disable); sessions and API
// tokens of the account are additionally refused on every replica that does not
// consider it active (sessionFromCookie, authAPIToken), so a replica running an
// older configuration cannot keep it alive. The credential files are read again
// on every sign-in attempt: rotating a Secret needs no restart.

const (
	// maxCredentialFile bounds what is read from a credential file.
	maxCredentialFile = 4 << 10
	// failureDelay slows down online guessing of the bootstrap credentials.
	bootstrapFailureDelay = 300 * time.Millisecond
	// maxBootstrapUsername bounds the user name echoed into audit entries.
	maxBootstrapUsername = 64
)

// bootstrapSubject is the subject of the bootstrap admin's user row.
func (s *Service) bootstrapSubject() string {
	return domain.BootstrapSubjectPrefix + s.cfg.BootstrapAdmin.Username
}

// bootstrapUserActive reports whether this replica accepts u as the active
// bootstrap admin.
func (s *Service) bootstrapUserActive(u *domain.User) bool {
	return u.Provider == domain.ProviderBootstrap && s.cfg.BootstrapActive() && u.Subject == s.bootstrapSubject()
}

// bootstrapCredentials lists the configured credential kinds.
func (s *Service) bootstrapCredentials() []string {
	var kinds []string
	if s.cfg.BootstrapAdmin.PasswordHashFile != "" {
		kinds = append(kinds, "password")
	}
	if s.cfg.BootstrapAdmin.TokenFile != "" {
		kinds = append(kinds, "token")
	}
	return kinds
}

// readCredentialFile reads a credential file (at most maxCredentialFile bytes)
// and returns its content without surrounding whitespace. The error names the
// file and the operating system error, never the content.
func readCredentialFile(file string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", file, pathErr(err))
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxCredentialFile+1))
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", file, pathErr(err))
	}
	if len(b) > maxCredentialFile {
		return "", fmt.Errorf("file %s is larger than %d bytes", file, maxCredentialFile)
	}
	return strings.TrimSpace(string(b)), nil
}

// warnLooseMode logs a warning when a credential file can be read or written
// by "others" (see secretfile.WarnLooseMode). The files of the bootstrap admin
// and of the declarative enrollment tokens are checked here, when they are
// read; secretfile.WarnLooseModes covers the other secret files of the server.
func (s *Service) warnLooseMode(setting, file string) {
	secretfile.WarnLooseMode(s.log, setting, file)
}

// pathErr returns the operating system error without the path.
func pathErr(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// checkBootstrapFiles reads and checks the credential files once at startup
// (only while the bootstrap admin is active). A problem is reported with the
// setting, never with file content.
func (s *Service) checkBootstrapFiles() error {
	if !s.cfg.BootstrapActive() {
		return nil
	}
	b := s.cfg.BootstrapAdmin
	if !domain.ValidBootstrapUsername(b.Username) {
		return errors.New(`authn: auth.bootstrapAdmin.username: must be 1 to 64 characters: letters, digits, ".", "_", "@" or "-"`)
	}
	for _, c := range []struct {
		setting, file string
		problem       func(string) string
	}{
		{"passwordHashFile", b.PasswordHashFile, domain.BootstrapHashProblem},
		{"tokenFile", b.TokenFile, domain.BootstrapTokenProblem},
	} {
		if c.file == "" {
			continue
		}
		content, err := readCredentialFile(c.file)
		if err != nil {
			return fmt.Errorf("authn: auth.bootstrapAdmin.%s: %w", c.setting, err)
		}
		s.warnLooseMode("auth.bootstrapAdmin."+c.setting, c.file)
		if msg := c.problem(content); msg != "" {
			return fmt.Errorf("authn: auth.bootstrapAdmin.%s: file %s %s", c.setting, c.file, msg)
		}
	}
	return nil
}

// EnsureBootstrapAdmin makes the user table match auth.bootstrapAdmin. Every
// bootstrap user that is not the active one (the bootstrap admin is disabled
// or replaced by single sign-on, or the user name changed) is disabled and
// loses its sessions and API tokens (audited as auth.bootstrap.deactivate).
// The active account is created, or enabled again if an earlier configuration
// had disabled it (audited as auth.bootstrap.activate). A start that finds
// nothing to change writes nothing. Called at startup in serve and dev mode.
//
// The revocation converges across restarts: the steps of one account run in
// one transaction, an account that is disabled already is checked for
// leftovers on every start (an older build disabled accounts without revoking
// their API tokens), and the tokens of an account that comes back are revoked
// before it is enabled.
func (s *Service) EnsureBootstrapAdmin(ctx context.Context) error {
	ids := s.deps.Store.Identity()
	users, err := ids.ListUsers(ctx)
	if err != nil {
		return fmt.Errorf("authn: list users: %w", err)
	}
	active := s.cfg.BootstrapActive()
	for i := range users {
		u := &users[i]
		if u.Provider != domain.ProviderBootstrap || (active && u.Subject == s.bootstrapSubject()) {
			continue
		}
		var reason string
		switch {
		case !s.cfg.BootstrapAdmin.Enabled:
			reason = "disabled_in_config"
		case !active && s.cfg.OIDC.Enabled && !s.cfg.BootstrapAdmin.KeepWithOIDC:
			reason = "oidc_enabled"
		case !active:
			reason = "disabled_in_config"
		default:
			reason = "username_changed"
		}
		if err := s.deactivateBootstrapUser(ctx, u, reason); err != nil {
			return err
		}
	}
	if !active {
		return nil
	}

	newID := newUUID()
	u, err := ids.UpsertUser(ctx, domain.User{
		ID: newID, Subject: s.bootstrapSubject(), Provider: domain.ProviderBootstrap, Name: s.cfg.BootstrapAdmin.Username,
		Role: domain.RoleAdmin, RoleSource: "mapping",
	})
	if err != nil {
		return fmt.Errorf("authn: create the bootstrap admin: %w", err)
	}
	created, reenabled := u.ID == newID, false
	var removed int64
	var revoked int
	if u.Disabled {
		// The account was disabled earlier, by this or by an older build. What
		// it could authenticate with in that life must not return with it: the
		// API tokens are revoked BEFORE the account is enabled, so a failing
		// step leaves it disabled (and all of it is one transaction anyway).
		// u.Disabled is only a hint read outside the transaction: the row is
		// locked and read again inside it, so of several replicas that start
		// together only one finds the account disabled and does the work; the
		// others find it enabled, change nothing and write no audit entry
		// (and do not revoke what was issued after the first one enabled it).
		err := s.deps.Store.InTx(ctx, func(tx store.Store) error {
			txIdentity := tx.Identity()
			cur, err := txIdentity.LockUser(ctx, u.ID)
			if err != nil {
				return fmt.Errorf("authn: lock the bootstrap admin: %w", err)
			}
			if !cur.Disabled {
				return nil // enabled by another replica in the meantime
			}
			tokens, err := txIdentity.RevokeAPITokensByCreator(ctx, u.ID, s.now())
			if err != nil {
				return fmt.Errorf("authn: revoke the API tokens of the bootstrap user: %w", err)
			}
			revoked = len(tokens)
			if removed, err = txIdentity.DeleteSessionsByUser(ctx, u.ID); err != nil {
				return fmt.Errorf("authn: remove sessions of the bootstrap user: %w", err)
			}
			if err := txIdentity.SetUserDisabled(ctx, u.ID, false); err != nil {
				return fmt.Errorf("authn: enable the bootstrap admin: %w", err)
			}
			reenabled = true
			return nil
		})
		if err != nil {
			return err
		}
	}
	if created || reenabled {
		details := map[string]any{"username": u.Name, "credentials": s.bootstrapCredentials(), "reason": "created"}
		if reenabled {
			details["reason"] = "enabled_in_config"
			details["sessionsRemoved"], details["apiTokensRevoked"] = removed, revoked
			s.log.Info("the bootstrap admin was enabled again", "username", u.Name, "sessionsRemoved", removed, "apiTokensRevoked", revoked)
		}
		s.publishInvalidate(ctx, "user", u.ID.String())
		s.recordSystem(ctx, "auth.bootstrap.activate", "user", u.ID.String(), details)
	}
	return nil
}

// deactivateBootstrapUser disables u, a bootstrap user that is not the active
// one, and removes what it could still authenticate with: its sessions and the
// API tokens it created. The API tokens are revoked for good, so enabling the
// account later does not bring back credentials of an earlier life. The steps
// run in one transaction that starts with a lock on the user row, so
// concurrent starts of several replicas run one after the other and the later
// ones see what the first one did (the state of u, read outside the
// transaction, is not relied on). None of the steps fails on a repeat, so a
// user that is disabled already is checked for leftovers (an older build did
// not revoke the tokens). It writes an audit entry only when it changed
// something.
func (s *Service) deactivateBootstrapUser(ctx context.Context, u *domain.User, reason string) error {
	var removed int64
	var revoked int
	var wasDisabled bool
	err := s.deps.Store.InTx(ctx, func(tx store.Store) error {
		txIdentity := tx.Identity()
		cur, err := txIdentity.LockUser(ctx, u.ID)
		if err != nil {
			return fmt.Errorf("authn: lock the bootstrap user: %w", err)
		}
		wasDisabled = cur.Disabled
		tokens, err := txIdentity.RevokeAPITokensByCreator(ctx, u.ID, s.now())
		if err != nil {
			return fmt.Errorf("authn: revoke the API tokens of the bootstrap user: %w", err)
		}
		revoked = len(tokens)
		if removed, err = txIdentity.DeleteSessionsByUser(ctx, u.ID); err != nil {
			return fmt.Errorf("authn: remove sessions of the bootstrap user: %w", err)
		}
		if !wasDisabled {
			if err := txIdentity.SetUserDisabled(ctx, u.ID, true); err != nil {
				return fmt.Errorf("authn: deactivate bootstrap user: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if wasDisabled && removed == 0 && revoked == 0 {
		return nil // disabled earlier and nothing left behind
	}
	details := map[string]any{"username": u.Name, "reason": reason, "sessionsRemoved": removed, "apiTokensRevoked": revoked}
	if wasDisabled {
		details["alreadyDisabled"] = true
		s.log.Info("credentials left behind by the disabled bootstrap admin were removed", "username", u.Name, "reason", reason, "sessionsRemoved", removed, "apiTokensRevoked", revoked)
	} else {
		s.log.Info("the bootstrap admin was deactivated", "username", u.Name, "reason", reason, "sessionsRemoved", removed, "apiTokensRevoked", revoked)
	}
	s.publishInvalidate(ctx, "user", u.ID.String())
	s.recordSystem(ctx, "auth.bootstrap.deactivate", "user", u.ID.String(), details)
	return nil
}

// recordSystem writes an audit entry of the server itself.
func (s *Service) recordSystem(ctx context.Context, action, targetType, targetID string, details map[string]any) {
	if s.deps.Audit == nil {
		return
	}
	s.deps.Audit.Record(ctx, domain.AuditEvent{
		Time: s.now(), ActorType: domain.ActorSystem, ActorID: "system", ActorName: "system", Action: action,
		TargetType: targetType, TargetID: targetID, Result: "success", Details: details,
	})
}

// bootstrapLoginAudit builds the audit entry of a sign-in attempt.
func (s *Service) bootstrapLoginAudit(r *http.Request, actorID, actorName string, details map[string]any, err error) domain.AuditEvent {
	details["method"] = "bootstrap"
	e := domain.AuditEvent{
		Time: s.now(), ActorType: domain.ActorUser, ActorID: actorID, ActorName: actorName, Action: "auth.bootstrap.login",
		TargetType: "user", TargetID: actorID, Result: "success", Details: details,
		SourceIP: s.clientIP(r), RequestID: r.Header.Get(api.RequestIDHeader),
	}
	if err != nil {
		e.Result = "failure"
	}
	return e
}

// handleBootstrapLogin implements bootstrapLogin.
func (s *Service) handleBootstrapLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.BootstrapActive() {
		writeError(w, r, fmt.Errorf("%w: the bootstrap admin is not enabled", domain.ErrNotFound))
		return
	}
	var req dto.BootstrapLoginRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if (req.Password != "") == (req.Token != "") {
		writeError(w, r, fmt.Errorf("%w: exactly one of password and token is required", domain.ErrInvalidArgument))
		return
	}
	credential, actor := "password", "bootstrap:"+sanitizeAudit(truncate(req.Username, maxBootstrapUsername), maxBootstrapUsername)
	if req.Token != "" {
		credential, actor = "token", "bootstrap:token"
	}

	// Limits first: no file is read and no bcrypt runs for a throttled client.
	if ok, retry, audit := s.bootstrapIP.allow("ip:" + realip.LimiterKey(s.clientIP(r))); !ok {
		s.bootstrapThrottled(w, r, actor, credential, "client address", retry, audit)
		return
	}
	// Only one verification runs at a time per replica: acquire hands out the
	// single slot under the lock (check and use are one step), so parallel
	// guesses cannot pass the failure counter together.
	retry, audit, acquired := s.bootstrapBackoff.acquire()
	if !acquired {
		s.bootstrapThrottled(w, r, actor, credential, "account", retry, audit)
		return
	}
	defer s.bootstrapBackoff.release()

	fail := func(reason string, status error) {
		s.bootstrapBackoff.fail()
		s.metricLogin.WithLabelValues("bootstrap", "failure").Inc()
		s.record(r, s.bootstrapLoginAudit(r, actor, actor, map[string]any{"credential": credential, "reason": reason}, status))
		time.Sleep(bootstrapFailureDelay) // slow down online guessing
		writeError(w, r, status)
	}
	invalid := fmt.Errorf("%w: invalid bootstrap credentials", domain.ErrUnauthorized)
	unavailable := func(setting string, err error) {
		s.log.Warn("the bootstrap credential cannot be used", "setting", setting, "error", err)
		s.metricLogin.WithLabelValues("bootstrap", "failure").Inc()
		e := fmt.Errorf("%w: the bootstrap credential is not available", domain.ErrUnavailable)
		s.record(r, s.bootstrapLoginAudit(r, actor, actor, map[string]any{"credential": credential, "reason": "credential_unavailable"}, e))
		writeError(w, r, e)
	}

	var ok bool
	if credential == "token" {
		file := s.cfg.BootstrapAdmin.TokenFile
		if file == "" {
			fail("bad_credentials", invalid)
			return
		}
		want, err := readCredentialFile(file)
		if err == nil {
			if msg := domain.BootstrapTokenProblem(want); msg != "" {
				err = errors.New("the file content is not a valid token")
			}
		}
		if err != nil {
			unavailable("auth.bootstrapAdmin.tokenFile", err)
			return
		}
		given, expected := sha256.Sum256([]byte(req.Token)), sha256.Sum256([]byte(want))
		ok = subtle.ConstantTimeCompare(given[:], expected[:]) == 1
	} else {
		file := s.cfg.BootstrapAdmin.PasswordHashFile
		if file == "" {
			fail("bad_credentials", invalid)
			return
		}
		hash, err := readCredentialFile(file)
		if err == nil {
			if msg := domain.BootstrapHashProblem(hash); msg != "" {
				err = errors.New("the file content is not a valid bcrypt hash")
			}
		}
		if err != nil {
			unavailable("auth.bootstrapAdmin.passwordHashFile", err)
			return
		}
		// bcrypt runs for every attempt, also for a wrong user name, so the
		// response time does not tell whether the user name exists.
		userOK := subtle.ConstantTimeCompare([]byte(req.Username), []byte(s.cfg.BootstrapAdmin.Username)) == 1
		pwOK := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) == nil
		ok = userOK && pwOK
	}
	if !ok {
		fail("bad_credentials", invalid)
		return
	}

	// The credential is correct: the account must still be usable on this
	// database (another replica may have deactivated it) before a one-time
	// token is spent.
	user, err := s.deps.Store.Identity().UpsertUser(r.Context(), domain.User{
		ID: newUUID(), Subject: s.bootstrapSubject(), Provider: domain.ProviderBootstrap, Name: s.cfg.BootstrapAdmin.Username,
		Role: domain.RoleAdmin, RoleSource: "mapping",
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	if user.Disabled {
		s.bootstrapBackoff.fail()
		s.metricLogin.WithLabelValues("bootstrap", "failure").Inc()
		e := fmt.Errorf("%w: account is disabled", domain.ErrUnauthorized)
		s.record(r, s.bootstrapLoginAudit(r, user.ID.String(), userDisplayName(user), map[string]any{"credential": credential, "reason": "user_disabled"}, e))
		writeError(w, r, e)
		return
	}
	if credential == "token" {
		sum := sha256.Sum256([]byte(req.Token))
		fresh, err := s.deps.Store.KV().PutIfAbsent(r.Context(), store.KVPrefixBootstrapTokenUsed+hex.EncodeToString(sum[:]), []byte(s.now().Format(time.RFC3339)))
		if err != nil {
			unavailable(store.KVPrefixBootstrapTokenUsed, err)
			return
		}
		if !fresh {
			fail("token_used", invalid)
			return
		}
	}
	if err := s.startSession(w, r, user, "bootstrap"); err != nil {
		writeError(w, r, err)
		return
	}
	s.bootstrapBackoff.reset()
	s.metricLogin.WithLabelValues("bootstrap", "success").Inc()
	s.record(r, s.bootstrapLoginAudit(r, user.ID.String(), userDisplayName(user), map[string]any{"credential": credential}, nil))
	p := &authz.Principal{Type: authz.PrincipalUser, ID: user.ID.String(), Name: userDisplayName(user), Role: user.Role, AuthMethod: domain.ProviderBootstrap}
	api.WriteJSON(w, http.StatusOK, meFor(p, csrfFromResponse(w)))
}

// bootstrapThrottled answers a throttled sign-in attempt with 429 and audits it
// at most once per minute and key.
func (s *Service) bootstrapThrottled(w http.ResponseWriter, r *http.Request, actor, credential, by string, retry time.Duration, audit bool) {
	s.metricLogin.WithLabelValues("bootstrap", "rate_limited").Inc()
	what := "sign-in attempts for this " + by
	if by == "account" {
		what = "sign-in attempts for the bootstrap admin"
	}
	err := rateLimitedError(w, retry, what)
	if audit {
		s.record(r, s.bootstrapLoginAudit(r, actor, actor, map[string]any{"credential": credential, "reason": "rate_limited", "limitedBy": by}, err))
	}
	writeError(w, r, err)
}
