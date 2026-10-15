// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package fleetapi

import (
	"errors"
	"fmt"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/authn"
	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/install"
	"github.com/myriarch/myriarch/internal/metrics"
)

// InstallAuthenticator authenticates a request of the installation from the server by the enrollment token
// (authn.Service.AuthenticateInstall): a token is mandatory, and every failure of a token looks the same.
type InstallAuthenticator interface {
	AuthenticateInstall(r *http.Request) (*authn.AgentCredential, error)
}

// maxPlanRequestBytes bounds the body of a plan request: a platform, up to eight attributes of at most 253
// characters, a fleet and a version.
const maxPlanRequestBytes = 16 << 10

func (s *service) installRoutes() []api.Route {
	plan := route(http.MethodPost, "/install/plan", "getInstallPlan", "custom", s.getInstallPlan)
	plan.MaxBodyBytes = maxPlanRequestBytes
	return []api.Route{plan}
}

// installEnabled reports whether the routes of the installation do anything. Otherwise they answer 404, before any
// authentication, so that a server that does not offer the installation reveals nothing about tokens.
func (s *service) installEnabled() bool {
	return s.d.Install != nil && s.d.InstallAuth != nil && s.d.Install.Enabled()
}

func notEnabled(w http.ResponseWriter, r *http.Request) {
	api.WriteError(w, r, fmt.Errorf("%w: the installation of hosts from the server is turned off (install.enabled)", domain.ErrNotFound))
}

// getInstallPlan answers a host that asks for its plan (operation getInstallPlan, api.AuthCustom): the enrollment
// token is checked first, then the request, and the planner decides. The token is read from the header only; it is
// not logged, not counted as a label and not part of any answer. Only its ID is logged.
func (s *service) getInstallPlan(w http.ResponseWriter, r *http.Request) {
	result := "error"
	defer func() { s.installPlans.WithLabelValues(result).Inc() }()

	if !s.installEnabled() {
		result = "disabled"
		notEnabled(w, r)
		return
	}
	cred, err := s.d.InstallAuth.AuthenticateInstall(r)
	if err == nil && cred.EnrollmentTokenID == nil {
		err = domain.ErrUnauthorized
	}
	if err != nil {
		result = s.installAuthFailed(w, r, err)
		return
	}
	var req install.PlanRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		result = "bad_request"
		api.WriteError(w, r, err)
		return
	}
	plan, err := s.d.Install.Plan(r.Context(), install.Credential{TokenID: *cred.EnrollmentTokenID, Labels: cred.Labels}, req)
	if err != nil {
		result = s.installRefused(w, r, err)
		return
	}
	text := ""
	if !wantsJSON(r) {
		if text, err = plan.Text(); err != nil { // the planner checked the plan: this is a bug, not a request
			api.WriteError(w, r, fmt.Errorf("install plan: %w", err))
			return
		}
	}
	s.log.Info("install plan issued", "token_id", cred.EnrollmentTokenID.String(), "fleet", plan.Fleet,
		"platform", install.NormalizePlatform(req.OS, req.Arch).String(), "collector_version", plan.Collector.Version, "version_source", plan.Version.Source,
		"request_id", r.Header.Get(api.RequestIDHeader))
	result = "ok"
	w.Header().Set("Vary", "Accept")
	if wantsJSON(r) {
		api.WriteJSON(w, http.StatusOK, plan)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(text))
}

// installAuthFailed answers a failed authentication and returns the result for the metric. An unauthorized request
// always gets the same answer, so it does not tell whether a token exists, is revoked or has expired.
func (s *service) installAuthFailed(w http.ResponseWriter, r *http.Request, err error) string {
	switch {
	case errors.Is(err, domain.ErrUnauthorized):
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+brand.AuthRealm+`"`)
		api.WriteError(w, r, fmt.Errorf("%w: a valid enrollment token is required", domain.ErrUnauthorized))
		return "unauthorized"
	case errors.Is(err, domain.ErrTooManyRequests):
		api.WriteError(w, r, err)
		return "rate_limited"
	default: // the database: 503 without the cause
		api.WriteError(w, r, err)
		return "unavailable"
	}
}

// installRefused writes the answer to a request that the planner refuses and returns the result for the metric. A
// refusal of the server carries its reason as the code of the first issue of the problem document, so that an
// installer can name it without parsing text.
func (s *service) installRefused(w http.ResponseWriter, r *http.Request, err error) string {
	var ref *install.Refusal
	switch {
	case errors.As(err, &ref):
		status, code := http.StatusServiceUnavailable, api.ErrorCodeUnavailable
		switch {
		case errors.Is(ref.Class, domain.ErrConflict):
			status, code = http.StatusConflict, api.ErrorCodeConflict
		case errors.Is(ref.Class, domain.ErrPrecondition):
			status, code = http.StatusPreconditionFailed, api.ErrorCodePreconditionFailed
		}
		if ref.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(ref.RetryAfter.Seconds()))))
		}
		api.WriteProblem(w, r, api.Problem{
			Status: status, Code: code, Detail: ref.Detail,
			Issues: domain.Issues{{Severity: domain.SeverityError, Code: ref.Code, Message: ref.Detail}},
		})
		return ref.Code
	case errors.Is(err, domain.ErrValidation):
		api.WriteError(w, r, err)
		return "invalid_request"
	default:
		api.WriteError(w, r, err)
		if errors.Is(err, domain.ErrUnavailable) {
			return "unavailable"
		}
		return "error"
	}
}

// wantsJSON reports whether the Accept header prefers application/json to text/plain. Without a header, or with
// */* or text/*, the plan is text/plain, the format that the installers read.
func wantsJSON(r *http.Request) bool {
	for _, h := range r.Header.Values("Accept") {
		for _, part := range strings.Split(h, ",") {
			mt, params, err := mime.ParseMediaType(strings.TrimSpace(part))
			if err != nil {
				continue
			}
			if q, ok := params["q"]; ok {
				if v, err := strconv.ParseFloat(q, 64); err == nil && v <= 0 {
					continue
				}
			}
			switch strings.ToLower(mt) {
			case "application/json":
				return true
			case "text/plain", "text/*", "*/*":
				return false
			}
		}
	}
	return false
}

// newInstallMetrics creates the counter of the plan requests: myriarch_install_plans_total{result}. The result is
// ok, disabled, unauthorized, rate_limited, bad_request, invalid_request, unavailable, error or the reason code of a
// refusal; a token never appears as a label.
func newInstallMetrics(reg prometheus.Registerer) *prometheus.CounterVec {
	return metrics.Register(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "myriarch_install_plans_total", Help: "Requests for an install plan by result (ok, a refusal reason such as fleet_mismatch or preparing, unauthorized, rate_limited, invalid_request).",
	}, []string{"result"}))
}
