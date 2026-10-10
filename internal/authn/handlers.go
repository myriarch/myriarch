// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/authz"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/httpserver/realip"
)

func newUUID() uuid.UUID { return uuid.Must(uuid.NewV7()) }

// Module returns the REST routes of the tags auth, users and tokens:
// getAuthConfig, startOidcLogin, oidcCallback, devLogin, bootstrapLogin,
// logout, getMe,
// listUsers, updateUser, listApiTokens, createApiToken, revokeApiToken,
// listEnrollmentTokens, createEnrollmentToken, revokeEnrollmentToken.
func (s *Service) Module() api.Module {
	return api.ModuleFunc{ModuleName: "auth", RoutesFunc: func() []api.Route {
		return []api.Route{
			{Method: "GET", Pattern: "/auth/config", OperationID: "getAuthConfig", Auth: api.AuthPublic, Handler: s.handleAuthConfig},
			{Method: "GET", Pattern: "/auth/login", OperationID: "startOidcLogin", Auth: api.AuthPublic, Handler: s.handleStartOIDC},
			{Method: "GET", Pattern: "/auth/callback", OperationID: "oidcCallback", Auth: api.AuthPublic, Handler: s.handleOIDCCallback},
			{Method: "POST", Pattern: "/auth/dev-login", OperationID: "devLogin", Auth: api.AuthPublic, Handler: s.handleDevLogin, MaxBodyBytes: 16 << 10},
			{Method: "POST", Pattern: "/auth/bootstrap-login", OperationID: "bootstrapLogin", Auth: api.AuthPublic, Handler: s.handleBootstrapLogin, MaxBodyBytes: 16 << 10},
			{Method: "POST", Pattern: "/auth/logout", OperationID: "logout", Permission: authz.PermRead, Handler: s.handleLogout},
			{Method: "GET", Pattern: "/auth/me", OperationID: "getMe", Permission: authz.PermRead, Handler: s.handleMe},

			{Method: "GET", Pattern: "/users", OperationID: "listUsers", Permission: authz.PermUsersManage, Handler: s.handleListUsers},
			{Method: "PATCH", Pattern: "/users/{id}", OperationID: "updateUser", Permission: authz.PermUsersManage, Handler: s.handleUpdateUser},

			{Method: "GET", Pattern: "/tokens", OperationID: "listApiTokens", Permission: authz.PermTokensManage, Handler: s.handleListAPITokens},
			{Method: "POST", Pattern: "/tokens", OperationID: "createApiToken", Permission: authz.PermTokensManage, Handler: s.handleCreateAPIToken},
			{Method: "DELETE", Pattern: "/tokens/{id}", OperationID: "revokeApiToken", Permission: authz.PermTokensManage, Handler: s.handleRevokeAPIToken},

			{Method: "GET", Pattern: "/enrollment-tokens", OperationID: "listEnrollmentTokens", Permission: authz.PermEnrollmentManage, Handler: s.handleListEnrollmentTokens},
			{Method: "POST", Pattern: "/enrollment-tokens", OperationID: "createEnrollmentToken", Permission: authz.PermEnrollmentManage, Handler: s.handleCreateEnrollmentToken},
			{Method: "DELETE", Pattern: "/enrollment-tokens/{id}", OperationID: "revokeEnrollmentToken", Permission: authz.PermEnrollmentManage, Handler: s.handleRevokeEnrollmentToken},
		}
	}}
}

// ------------------------------------------------------------- sessions ---

// startSession creates a session for user and sets the session and CSRF cookies.
func (s *Service) startSession(w http.ResponseWriter, r *http.Request, user *domain.User, method string) error {
	secret, err := randomString(32)
	if err != nil {
		return err
	}
	csrf, err := randomString(32)
	if err != nil {
		return err
	}
	now := s.now()
	sess := domain.Session{
		ID: sessionIDFromCookie(secret), UserID: user.ID, CSRFToken: csrf, CreatedAt: now, LastSeenAt: now,
		ExpiresAt: now.Add(s.cfg.SessionTTL), UserAgent: truncate(r.UserAgent(), 256), RemoteAddr: s.clientIP(r),
	}
	if err := s.deps.Store.Identity().CreateSession(r.Context(), sess); err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	if err := s.deps.Store.Identity().TouchUserLogin(r.Context(), user.ID, now); err != nil {
		s.log.Warn("touch user login failed", "error", err)
	}
	// Session cookie without Max-Age: it lives until the browser session ends; the
	// server side enforces the sliding idle TTL and the absolute maximum age.
	http.SetCookie(w, &http.Cookie{Name: s.sessionCookieName(), Value: secret, Path: "/", HttpOnly: true,
		Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: CSRFCookie, Value: csrf, Path: "/", HttpOnly: false,
		Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
	return nil
}

func (s *Service) clearCookies(w http.ResponseWriter) {
	for _, c := range []struct {
		name     string
		httpOnly bool
	}{{s.sessionCookieName(), true}, {CSRFCookie, false}} {
		http.SetCookie(w, &http.Cookie{Name: c.name, Value: "", Path: "/", MaxAge: -1, HttpOnly: c.httpOnly,
			Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
	}
}

// sessionCookieName is the name of the session cookie: with Secure cookies (production) it carries the __Host-
// prefix, so that a sibling host cannot plant a cookie of the same name for the parent domain; the plain name is
// used where cookies are not Secure (development mode over plain HTTP), as browsers refuse the prefix there.
func (s *Service) sessionCookieName() string {
	if s.cfg.CookieSecure {
		return SessionCookieSecure
	}
	return SessionCookie
}

func (s *Service) loginAudit(r *http.Request, actorID, actorName, method string, details map[string]any, err error) domain.AuditEvent {
	if actorID == "" {
		actorID = "anonymous"
	}
	if actorName == "" {
		actorName = actorID
	}
	if details == nil {
		details = map[string]any{}
	}
	details["method"] = method
	e := domain.AuditEvent{
		Time: s.now(), ActorType: domain.ActorUser, ActorID: actorID, ActorName: actorName, Action: "auth.login",
		TargetType: "user", TargetID: actorID, Result: "success", Details: details,
		SourceIP: s.clientIP(r), RequestID: r.Header.Get(api.RequestIDHeader),
	}
	if err != nil {
		e.Result = "failure"
	}
	return e
}

func meFor(p *authz.Principal, csrf string) dto.Me {
	perms := authz.PermissionsFor(p.Role)
	names := make([]string, len(perms))
	for i, x := range perms {
		names[i] = string(x)
	}
	t := "user"
	if p.Type == authz.PrincipalToken {
		t = "token"
	}
	return dto.Me{Type: t, ID: p.ID, Name: p.Name, Email: p.Email, Role: p.Role, AuthMethod: p.AuthMethod, Permissions: names, CSRFToken: csrf}
}

// ------------------------------------------------------------- handlers ---

func (s *Service) handleAuthConfig(w http.ResponseWriter, r *http.Request) {
	active := s.cfg.BootstrapActive()
	info := dto.AuthConfigInfo{OIDCEnabled: s.cfg.OIDC.Enabled, DevLoginEnabled: s.cfg.DevLogin.Enabled,
		BootstrapPasswordEnabled: active && s.cfg.BootstrapAdmin.PasswordHashFile != "",
		BootstrapTokenEnabled:    active && s.cfg.BootstrapAdmin.TokenFile != "",
		UI:                       dto.UIConfigInfo{DefaultLanguage: s.deps.UI.DefaultLanguage, Languages: s.deps.UI.Languages}}
	if info.UI.DefaultLanguage == "" {
		info.UI.DefaultLanguage = "en"
	}
	if s.cfg.OIDC.Enabled {
		info.OIDCDisplayName = s.cfg.OIDC.DisplayName
	}
	api.WriteJSON(w, http.StatusOK, info)
}

func (s *Service) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.DevLogin.Enabled {
		writeError(w, r, fmt.Errorf("%w: dev login is not enabled", domain.ErrNotFound))
		return
	}
	var req dto.DevLoginRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.limitLogin(w, r, req.Username); err != nil {
		writeError(w, r, err)
		return
	}
	// Constant time over ALL configured users: no early exit on username or password.
	given := hashPassword(req.Password)
	var matched *devUser
	for i := range s.devUsers {
		u := &s.devUsers[i]
		userOK := subtle.ConstantTimeCompare([]byte(u.username), []byte(req.Username))
		pwOK := subtle.ConstantTimeCompare(u.pwHash[:], given[:])
		if userOK&pwOK == 1 {
			matched = u
		}
	}
	if matched == nil || req.Username == "" {
		s.metricLogin.WithLabelValues("dev", "failure").Inc()
		err := fmt.Errorf("%w: invalid username or password", domain.ErrUnauthorized)
		s.record(r, s.loginAudit(r, "local:"+truncate(req.Username, 64), truncate(req.Username, 64), "dev", map[string]any{"reason": "bad_credentials"}, err))
		time.Sleep(300 * time.Millisecond) // slow down online guessing
		writeError(w, r, err)
		return
	}
	user, err := s.deps.Store.Identity().UpsertUser(r.Context(), domain.User{
		ID: newUUID(), Subject: "local:" + matched.username, Provider: "local", Name: matched.username, Role: matched.role, RoleSource: "mapping",
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	if user.Disabled {
		s.metricLogin.WithLabelValues("dev", "failure").Inc()
		err := fmt.Errorf("%w: account is disabled", domain.ErrUnauthorized)
		s.record(r, s.loginAudit(r, user.ID.String(), userDisplayName(user), "dev", map[string]any{"reason": "user_disabled"}, err))
		writeError(w, r, err)
		return
	}
	if err := s.startSession(w, r, user, "dev"); err != nil {
		writeError(w, r, err)
		return
	}
	s.metricLogin.WithLabelValues("dev", "success").Inc()
	s.record(r, s.loginAudit(r, user.ID.String(), userDisplayName(user), "dev", map[string]any{"subject": user.Subject, "role": string(user.Role)}, nil))
	p := &authz.Principal{Type: authz.PrincipalUser, ID: user.ID.String(), Name: user.Name, Role: user.Role, AuthMethod: "local"}
	// the csrf token was just generated: read it back from the cookie we set
	api.WriteJSON(w, http.StatusOK, meFor(p, csrfFromResponse(w)))
}

// csrfFromResponse returns the CSRF cookie value set on w.
func csrfFromResponse(w http.ResponseWriter) string {
	resp := http.Response{Header: w.Header()}
	for _, c := range resp.Cookies() {
		if c.Name == CSRFCookie {
			return c.Value
		}
	}
	return ""
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	p := authz.FromContext(r.Context())
	if c, err := r.Cookie(s.sessionCookieName()); err == nil && c.Value != "" && (p == nil || p.AuthMethod != "api_token") {
		id := sessionIDFromCookie(c.Value)
		if err := s.deps.Store.Identity().DeleteSession(r.Context(), id); err != nil && !errors.Is(err, domain.ErrNotFound) {
			writeError(w, r, err)
			return
		}
		s.publishInvalidate(r.Context(), "session", id)
		s.clearCookies(w)
	}
	s.auditRequest(r, "auth.logout", "user", principalID(p), nil, nil)
	w.WriteHeader(http.StatusNoContent)
}

func principalID(p *authz.Principal) string {
	if p == nil {
		return ""
	}
	return p.ID
}

func (s *Service) handleMe(w http.ResponseWriter, r *http.Request) {
	p := authz.FromContext(r.Context())
	if p == nil {
		writeError(w, r, domain.ErrUnauthorized)
		return
	}
	csrf := ""
	if p.AuthMethod != "api_token" {
		if c, err := r.Cookie(s.sessionCookieName()); err == nil {
			if e, err := s.sessionFromCookie(r.Context(), c.Value); err == nil {
				csrf = e.csrf
			}
		}
	}
	api.WriteJSON(w, http.StatusOK, meFor(p, csrf))
}

// ----------------------------------------------------------------- users ---

func (s *Service) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.deps.Store.Identity().ListUsers(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.NewList(users))
}

func (s *Service) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, r, fmt.Errorf("%w: invalid user id", domain.ErrInvalidArgument))
		return
	}
	var req dto.UpdateUserRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.Role == nil && req.Disabled == nil {
		writeError(w, r, domain.NewValidationError(domain.Issue{Severity: domain.SeverityError, Code: "empty_update", Message: "role or disabled must be set"}))
		return
	}
	if req.Role != nil && !req.Role.Valid() {
		writeError(w, r, domain.NewValidationError(domain.Issue{Severity: domain.SeverityError, Code: "invalid_role", Path: "role", Message: fmt.Sprintf("unknown role %q", *req.Role)}))
		return
	}
	p := authz.FromContext(r.Context())
	if p != nil && p.ID == id.String() {
		writeError(w, r, fmt.Errorf("%w: you cannot change your own role or disable your own account", domain.ErrPrecondition))
		return
	}
	ids := s.deps.Store.Identity()
	before, err := ids.GetUser(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	details := map[string]any{"subject": before.Subject}
	if before.Provider == domain.ProviderBootstrap {
		// the configuration is the only source of truth for this account
		err := fmt.Errorf("%w: the bootstrap admin is managed by configuration (auth.bootstrapAdmin)", domain.ErrPrecondition)
		s.auditRequest(r, "user.update", "user", id.String(), details, err)
		writeError(w, r, err)
		return
	}
	if req.Role != nil {
		if err := ids.SetUserRole(r.Context(), id, *req.Role, "manual"); err != nil {
			s.auditRequest(r, "user.update", "user", id.String(), details, err)
			writeError(w, r, err)
			return
		}
		details["role"] = map[string]any{"from": string(before.Role), "to": string(*req.Role)}
	}
	if req.Disabled != nil {
		if err := ids.SetUserDisabled(r.Context(), id, *req.Disabled); err != nil {
			s.auditRequest(r, "user.update", "user", id.String(), details, err)
			writeError(w, r, err)
			return
		}
		details["disabled"] = *req.Disabled
		if *req.Disabled {
			// A disabled user loses every session and every API token created
			// by that user (SSE streams are closed through the invalidation).
			n, derr := ids.DeleteSessionsByUser(r.Context(), id)
			revoked, rerr := ids.RevokeAPITokensByCreator(r.Context(), id, s.now())
			if err := errors.Join(derr, rerr); err != nil {
				s.log.Error("revoking credentials of a disabled user failed", "user", id, "error", err)
			}
			details["sessionsRemoved"], details["apiTokensRevoked"] = n, len(revoked)
		}
	}
	// role changes and disabling take effect on every replica immediately
	s.publishInvalidate(r.Context(), "user", id.String())
	s.auditRequest(r, "user.update", "user", id.String(), details, nil)
	updated, err := ids.GetUser(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, updated)
}

// ---------------------------------------------------------------- tokens ---

func validateName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return domain.NewValidationError(domain.Issue{Severity: domain.SeverityError, Code: "invalid_name", Path: "name", Message: "name must be 1 to 100 characters"})
	}
	return nil
}

func validateExpiry(t *time.Time, now time.Time) error {
	if t != nil && !t.After(now) {
		return domain.NewValidationError(domain.Issue{Severity: domain.SeverityError, Code: "invalid_expiry", Path: "expiresAt", Message: "expiresAt must be in the future"})
	}
	return nil
}

func (s *Service) handleListAPITokens(w http.ResponseWriter, r *http.Request) {
	toks, err := s.deps.Store.Identity().ListAPITokens(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	for i := range toks {
		toks[i].ExpiresAt = s.effectiveExpiry(toks[i])
	}
	api.WriteJSON(w, http.StatusOK, api.NewList(toks))
}

// effectiveExpiry is the time from which the token is refused: min(expiresAt, createdAt + auth.apiTokenMaxTtl),
// the rule that authentication applies, also to tokens that were created before the maximum existed or before
// it was lowered. It is nil only for a token without an expiry where no maximum is configured.
func (s *Service) effectiveExpiry(t domain.APIToken) *time.Time {
	exp := t.ExpiresAt
	if max := s.cfg.APITokenMaxTTL; max > 0 {
		if limit := t.CreatedAt.Add(max).UTC(); exp == nil || limit.Before(*exp) {
			exp = &limit
		}
	}
	return exp
}

func (s *Service) handleCreateAPIToken(w http.ResponseWriter, r *http.Request) {
	var req dto.CreateAPITokenRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := validateName(req.Name); err != nil {
		writeError(w, r, err)
		return
	}
	if !req.Role.Valid() {
		writeError(w, r, domain.NewValidationError(domain.Issue{Severity: domain.SeverityError, Code: "invalid_role", Path: "role", Message: fmt.Sprintf("unknown role %q", req.Role)}))
		return
	}
	p := authz.FromContext(r.Context())
	if p == nil {
		writeError(w, r, domain.ErrUnauthorized)
		return
	}
	// A token never has more rights than the principal that creates it.
	if domain.RoleRank(req.Role) > domain.RoleRank(p.Role) {
		err := fmt.Errorf("%w: a token cannot have a higher role (%s) than its creator (%s)", domain.ErrForbidden, req.Role, p.Role)
		s.auditRequest(r, "token.create", "api_token", "", map[string]any{"name": strings.TrimSpace(req.Name), "role": string(req.Role)}, err)
		writeError(w, r, err)
		return
	}
	now := s.now()
	if err := validateExpiry(req.ExpiresAt, now); err != nil {
		writeError(w, r, err)
		return
	}
	expires := utcPtrTime(req.ExpiresAt)
	if max := s.cfg.APITokenMaxTTL; max > 0 {
		// maximum lifetime: no expiry means the maximum, a longer one is refused
		limit := now.Add(max)
		switch {
		case expires == nil:
			l := limit.UTC()
			expires = &l
		case expires.After(limit):
			writeError(w, r, domain.NewValidationError(domain.Issue{Severity: domain.SeverityError, Code: "expiry_too_long", Path: "expiresAt",
				Message: fmt.Sprintf("expiresAt must not be later than %s from now (auth.apiTokenMaxTtl)", max)}))
			return
		}
	}
	var creatorID *uuid.UUID
	switch p.Type {
	case authz.PrincipalUser:
		if uid, err := uuid.Parse(p.ID); err == nil {
			creatorID = &uid
		}
	case authz.PrincipalToken:
		// A token created with a token inherits the user it ultimately derives
		// from (it stops working, and is limited, together with that user) and
		// never outlives its parent.
		parent, err := s.parentToken(r.Context(), p.ID)
		if err != nil {
			writeError(w, r, err)
			return
		}
		creatorID = parent.CreatedByUserID
		if parent.ExpiresAt != nil && (expires == nil || expires.After(*parent.ExpiresAt)) {
			expires = utcPtrTime(parent.ExpiresAt)
		}
	}
	secret, err := NewToken(domain.APITokenPrefix)
	if err != nil {
		writeError(w, r, err)
		return
	}
	t := domain.APIToken{
		ID: newUUID(), Name: strings.TrimSpace(req.Name), DisplayPrefix: displayPrefix(secret), Hash: hashToken(secret), Role: req.Role,
		CreatedBy: p.String(), CreatedAt: now, ExpiresAt: expires, CreatedByUserID: creatorID,
	}
	if err := s.deps.Store.Identity().CreateAPIToken(r.Context(), t); err != nil {
		s.auditRequest(r, "token.create", "api_token", t.ID.String(), map[string]any{"name": t.Name}, err)
		writeError(w, r, err)
		return
	}
	s.auditRequest(r, "token.create", "api_token", t.ID.String(), map[string]any{"name": t.Name, "role": string(t.Role), "prefix": t.DisplayPrefix}, nil)
	t.ExpiresAt = s.effectiveExpiry(t)
	api.WriteJSON(w, http.StatusCreated, dto.CreatedAPIToken{Token: t, Secret: secret})
}

// parentToken loads the API token that authenticated the current request.
func (s *Service) parentToken(ctx context.Context, id string) (*domain.APIToken, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid token principal", domain.ErrUnauthorized)
	}
	toks, err := s.deps.Store.Identity().ListAPITokens(ctx)
	if err != nil {
		return nil, err
	}
	for i := range toks {
		if toks[i].ID == uid {
			return &toks[i], nil
		}
	}
	return nil, fmt.Errorf("%w: token no longer exists", domain.ErrUnauthorized)
}

func utcPtrTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func (s *Service) handleRevokeAPIToken(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, r, fmt.Errorf("%w: invalid token id", domain.ErrInvalidArgument))
		return
	}
	if err := s.deps.Store.Identity().RevokeAPIToken(r.Context(), id, s.now()); err != nil {
		s.auditRequest(r, "token.revoke", "api_token", id.String(), nil, err)
		writeError(w, r, err)
		return
	}
	s.publishInvalidate(r.Context(), "api_token", id.String()) // all replicas, not only this one
	s.auditRequest(r, "token.revoke", "api_token", id.String(), nil, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleListEnrollmentTokens(w http.ResponseWriter, r *http.Request) {
	toks, err := s.deps.Store.Identity().ListEnrollmentTokens(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.NewList(toks))
}

func validateLabels(labels map[string]string) error {
	if issue := domain.CheckLabels(labels); issue != nil {
		return domain.NewValidationError(*issue)
	}
	return nil
}

func (s *Service) handleCreateEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	var req dto.CreateEnrollmentTokenRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := validateName(req.Name); err != nil {
		writeError(w, r, err)
		return
	}
	if err := validateLabels(req.Labels); err != nil {
		writeError(w, r, err)
		return
	}
	now := s.now()
	if err := validateExpiry(req.ExpiresAt, now); err != nil {
		writeError(w, r, err)
		return
	}
	secret, err := NewToken(domain.EnrollmentTokenPrefix)
	if err != nil {
		writeError(w, r, err)
		return
	}
	p := authz.FromContext(r.Context())
	labels := req.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	t := domain.EnrollmentToken{
		ID: newUUID(), Name: strings.TrimSpace(req.Name), DisplayPrefix: displayPrefix(secret), Hash: hashToken(secret), Labels: labels,
		CreatedBy: p.String(), CreatedAt: now, ExpiresAt: utcPtrTime(req.ExpiresAt),
	}
	if err := s.deps.Store.Identity().CreateEnrollmentToken(r.Context(), t); err != nil {
		s.auditRequest(r, "enrollment_token.create", "enrollment_token", t.ID.String(), map[string]any{"name": t.Name}, err)
		writeError(w, r, err)
		return
	}
	s.auditRequest(r, "enrollment_token.create", "enrollment_token", t.ID.String(), map[string]any{"name": t.Name, "labels": labels, "prefix": t.DisplayPrefix}, nil)
	api.WriteJSON(w, http.StatusCreated, dto.CreatedEnrollmentToken{Token: t, Secret: secret})
}

func (s *Service) handleRevokeEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, r, fmt.Errorf("%w: invalid token id", domain.ErrInvalidArgument))
		return
	}
	if err := s.deps.Store.Identity().RevokeEnrollmentToken(r.Context(), id, s.now()); err != nil {
		s.auditRequest(r, "enrollment_token.revoke", "enrollment_token", id.String(), nil, err)
		writeError(w, r, err)
		return
	}
	// every replica drops the token and disconnects the agents that used it
	s.publishEnrollmentRevoked(r.Context(), id, "revoked")
	s.auditRequest(r, "enrollment_token.revoke", "enrollment_token", id.String(), nil, nil)
	w.WriteHeader(http.StatusNoContent)
}

// limitLogin applies the per-IP and per-user-name limits of the dev login and
// returns a 429 error (with Retry-After) when one is exceeded.
func (s *Service) limitLogin(w http.ResponseWriter, r *http.Request, username string) error {
	ip := realip.LimiterKey(s.clientIP(r)) // IPv6 clients share one limit per /64
	for _, k := range []struct {
		l   *keyedLimiter
		key string
		by  string
	}{{s.loginIP, "ip:" + ip, "client address"}, {s.loginUser, "user:" + strings.ToLower(truncate(username, 64)), "user name"}} {
		ok, retry, audit := k.l.allow(k.key)
		if ok {
			continue
		}
		s.metricLogin.WithLabelValues("dev", "rate_limited").Inc()
		err := rateLimitedError(w, retry, "login attempts for this "+k.by)
		if audit {
			s.record(r, s.loginAudit(r, "local:"+truncate(username, 64), truncate(username, 64), "dev", map[string]any{"reason": "rate_limited", "limitedBy": k.by}, err))
		}
		return err
	}
	return nil
}
