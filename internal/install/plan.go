// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package install

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Masterminds/semver/v3"
	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/artifact"
	"github.com/myriarch/myriarch/internal/assignment"
	"github.com/myriarch/myriarch/internal/config"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

// Codes of the refusals of a plan request. They are the codes of ADR-0046 and travel as the code of the first
// issue of the problem document (the top-level code is the class: conflict, precondition_failed,
// validation_failed or unavailable).
const (
	// 409
	CodeFleetMismatch   = "fleet_mismatch"
	CodeNoFleet         = "no_fleet"
	CodeFleetAmbiguous  = "fleet_ambiguous"
	CodeRolloutBlocking = "rollout_blocking"
	// 412
	CodeNoTargetVersion          = "no_target_version"
	CodeUnsupportedDistribution  = "unsupported_distribution"
	CodeUnknownVersion           = "unknown_version"
	CodeUnknownSupervisorVersion = "unknown_supervisor_version"
	CodeServerSettings           = "server_settings"
	// 422
	CodeInvalidValue         = "invalid_value"
	CodeUnknownAttribute     = "unknown_attribute"
	CodePlatformNotSupported = "platform_not_supported"
	CodeVersionNotAllowed    = "version_not_allowed"
	// 503
	CodePreparing = "preparing"
)

// Retry-After of the answer 503 preparing: while artifacts download, and after a failed attempt (the server
// repeats an attempt after a minute at the earliest).
const (
	retryPreparing = 10 * time.Second
	retryFailed    = 30 * time.Second
)

// Refusal is a plan request that the server refuses on purpose. Class is the sentinel that decides the HTTP
// status: domain.ErrConflict (409), domain.ErrPrecondition (412) or domain.ErrUnavailable (503). A refusal of the
// request itself (422) is a *domain.ValidationError instead.
type Refusal struct {
	Class error
	// Code is one of the Code constants.
	Code string
	// Detail is an English sentence for the person who reads the installer's message. It names no secret.
	Detail string
	// RetryAfter is the time after which the request can be repeated (503 only).
	RetryAfter time.Duration
}

func (e *Refusal) Error() string { return e.Code + ": " + e.Detail }

// Unwrap lets errors.Is find the class.
func (e *Refusal) Unwrap() error { return e.Class }

func refuse(class error, code, format string, args ...any) *Refusal {
	return &Refusal{Class: class, Code: code, Detail: fmt.Sprintf(format, args...)}
}

// AllowedAttributes are the attributes that a host can report in its request: its name and the site contract that
// the installers write into the supervisor configuration (docs/design/opamp-server.md, "Supervisor
// configuration"). os.type and host.arch come from the platform of the request, the labels come from the
// enrollment token.
var AllowedAttributes = []string{
	domain.AttrHostName, domain.AttrSiteType, domain.AttrSiteID, domain.AttrSiteName, domain.AttrSiteCountry,
	domain.AttrSiteRegion, domain.AttrSiteTimezone, domain.AttrEnvironment,
}

// MaxAttributeBytes bounds the value of an attribute of the request (the limit of the label values).
const MaxAttributeBytes = 253

// PlanRequest is the body of the request for a plan. It holds no secret.
type PlanRequest struct {
	// OS and Arch are the platform of the host: linux or windows, amd64 or arm64 (aliases such as x86_64 and
	// aarch64 are accepted).
	OS   string `json:"os"`
	Arch string `json:"arch"`
	// Attributes are the attributes that the host would report (AllowedAttributes). The fleet is chosen from them
	// and from the labels of the enrollment token, as it is for an agent that connects.
	Attributes map[string]string `json:"attributes,omitempty"`
	// Fleet is the fleet that the installing person expects. The server decides; a hint that does not agree is
	// refused.
	Fleet string `json:"fleet,omitempty"`
	// CollectorVersion is a version that the host asks for. It is accepted only inside the frame of the fleet.
	CollectorVersion string `json:"collectorVersion,omitempty"`
}

// Credential is what the server knows about the enrollment token of the request.
type Credential struct {
	// TokenID is the ID of the token (never the secret); the install grants are bound to it.
	TokenID uuid.UUID
	// Labels are the labels of the token.
	Labels map[string]string
}

// Assigner is the part of the assignment service that the planner uses: the fleet is decided exactly as it is for
// an agent that connects.
type Assigner interface {
	Match(ctx context.Context, view domain.AttributeView) (assignment.MatchResult, error)
	Snapshot(ctx context.Context) (*domain.GitOpsSnapshot, error)
	VersionSelections(ctx context.Context) (map[string]*domain.FleetVersionSelection, error)
}

// Artifacts is the part of the artifact cache that the planner uses.
type Artifacts interface {
	Prepare(ctx context.Context, distribution, version, os, arch string) (*domain.Artifact, artifact.Readiness, error)
	InstallGrant(a *domain.Artifact, enrollmentToken uuid.UUID) (string, error)
	InstallURL(a *domain.Artifact) (string, error)
}

// Deps are the dependencies of the planner.
type Deps struct {
	Store     store.Store
	Assigner  Assigner
	Artifacts Artifacts
	Config    config.InstallConfig
	// PublicURL and OpAMPPath (server.publicUrl, opamp.path) give the OpAMP endpoint.
	PublicURL string
	OpAMPPath string
	Logger    *slog.Logger
}

// Planner answers a host's request for its plan.
type Planner struct {
	d         Deps
	log       *slog.Logger
	platforms []Platform
}

// NewPlanner checks the settings and returns the planner.
func NewPlanner(d Deps) (*Planner, error) {
	if d.Store == nil || d.Assigner == nil || d.Artifacts == nil {
		return nil, errors.New("install: store, assigner and artifacts are required")
	}
	platforms, err := ParsePlatforms(d.Config.Platforms)
	if err != nil {
		return nil, fmt.Errorf("install.platforms: %w", err)
	}
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Planner{d: d, log: log.With("component", "install"), platforms: platforms}, nil
}

// Enabled reports install.enabled.
func (p *Planner) Enabled() bool { return p.d.Config.Enabled }

// Plan decides what a host installs, following ADR-0046 and docs/design/opamp-server.md ("Installation from the
// server"):
//
//  1. The request is checked against allowlists (422).
//  2. The fleet is the one that the assignment service gives the host from the reported attributes and the labels
//     of the enrollment token; none, several of the same priority, or another than the hint is 409.
//  3. The version is the effective target of the fleet. While a version rollout of the fleet is pending, running
//     or waiting for approval, it is the version the fleet ran before (the fromRef of the rollout), so that the
//     new host becomes a straggler of its last wave; a paused, failed or rolling back rollout, or one without a
//     single starting version, is 409; a fleet on hold or without a target is 412. A version that the host asks
//     for is accepted inside the frame of the fleet only (422).
//  4. The release tracker must know the version for collector and supervisor (412).
//  5. The artifacts for the platform are prepared in the background; until both are ready the answer is 503 with
//     Retry-After.
//
// The reads are ordered so that a concurrent change cannot produce a version that never was one: the effective
// target is read from the memory of the assignment service, which follows the database and never runs ahead of
// it, and the rollouts are read after it. A rollout that the leader creates for a new target after the target was
// read is then seen with the fromRef that belongs to the previous target; the combination "new target, no
// rollout yet" cannot arise, which would send the host past the waves.
func (p *Planner) Plan(ctx context.Context, cred Credential, req PlanRequest) (*Plan, error) {
	if cred.TokenID == uuid.Nil {
		return nil, errors.New("install: a plan is only given to the holder of an enrollment token")
	}
	plat, err := p.checkRequest(&req)
	if err != nil {
		return nil, err
	}

	// 1. the fleet
	view := domain.AttributeView{}
	for k, v := range req.Attributes {
		view[k] = v
	}
	view[domain.AttrOSType], view[domain.AttrHostArch] = plat.OS, plat.Arch
	for k, v := range cred.Labels {
		view[domain.LabelPrefix+k] = v
	}
	match, err := p.d.Assigner.Match(ctx, view)
	if err != nil {
		return nil, fmt.Errorf("%w: match the fleet: %v", domain.ErrUnavailable, err)
	}
	switch {
	case match.Fleet == "" && len(match.Candidates) > 1:
		return nil, refuse(domain.ErrConflict, CodeFleetAmbiguous,
			"more than one fleet of the same highest priority selects this host (the fleets that match: %s): give the fleets different priorities in Git or narrow their selectors",
			strings.Join(match.Candidates, ", "))
	case match.Fleet == "":
		return nil, refuse(domain.ErrConflict, CodeNoFleet,
			"no fleet selects this host: check its site attributes and the labels of the enrollment token against the selectors of the fleets")
	case req.Fleet != "" && req.Fleet != match.Fleet:
		return nil, refuse(domain.ErrConflict, CodeFleetMismatch,
			"the server assigns this host to the fleet %s, not to %s: leave the fleet out of the request, or correct the site attributes and the enrollment token",
			match.Fleet, req.Fleet)
	}
	snap, err := p.d.Assigner.Snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: load the GitOps state: %v", domain.ErrUnavailable, err)
	}
	fleet := findFleet(snap, match.Fleet)
	if fleet == nil { // the GitOps state changed between the two reads
		return nil, refuse(domain.ErrConflict, CodeNoFleet, "the fleet %s does not exist any more: ask again", match.Fleet)
	}
	dist := fleet.Collector.Distribution
	if dist == "" {
		dist = domain.DistributionContrib
	}
	if dist != domain.DistributionContrib {
		return nil, refuse(domain.ErrPrecondition, CodeUnsupportedDistribution,
			"the fleet %s uses the distribution %s: only %s can be installed from the server", fleet.Name, dist, domain.DistributionContrib)
	}

	// 2. the version: the target first, the rollouts after it (see the comment on Plan)
	selections, err := p.d.Assigner.VersionSelections(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: load the version selections: %v", domain.ErrUnavailable, err)
	}
	target := domain.ResolveVersionTarget(fleet, selections[fleet.Name])
	if target.Version == "" {
		return nil, noTarget(fleet.Name, target.Source)
	}
	version, source := target.Version, string(target.Source)
	from, err := p.rolloutBase(ctx, fleet.Name)
	if err != nil {
		return nil, err
	}
	if from != "" {
		version, source = from, SourceRollout
	}
	if want := req.CollectorVersion; want != "" && want != version {
		if err := allowedVersion(fleet, want, version, target.Version); err != nil {
			return nil, err
		}
		version, source = want, SourceRequest
	}

	// 3. the releases
	if err := p.knownRelease(ctx, domain.DistributionContrib, version, source == SourceRequest); err != nil {
		return nil, err
	}
	if err := p.knownRelease(ctx, domain.DistributionSupervisor, version, source == SourceRequest); err != nil {
		return nil, err
	}

	// 4. the artifacts: both are started before the answer, so that they download side by side
	collector, cState, err := p.d.Artifacts.Prepare(ctx, domain.DistributionContrib, version, plat.OS, plat.Arch)
	if err != nil {
		return nil, p.prepareError(domain.DistributionContrib, version, err)
	}
	supervisor, sState, err := p.d.Artifacts.Prepare(ctx, domain.DistributionSupervisor, version, plat.OS, plat.Arch)
	if err != nil {
		return nil, p.prepareError(domain.DistributionSupervisor, version, err)
	}
	if cState != artifact.PrepareReady || sState != artifact.PrepareReady {
		return nil, notReady(plat, cState, sState)
	}

	// 5. the plan
	endpoint, err := opampEndpoint(p.d.PublicURL, p.d.OpAMPPath)
	if err != nil {
		return nil, refuse(domain.ErrPrecondition, CodeServerSettings, "%v", err)
	}
	plan := &Plan{
		Format:  PlanFormat,
		Fleet:   fleet.Name,
		Version: PlanVersion{Source: source},
		OpAMP:   PlanOpAMP{Endpoint: endpoint},
		Updater: PlanUpdater{Install: fleet.Update.Executor == domain.ExecutorUpdater},
	}
	if plan.Collector, err = p.artifactRef(collector, version, cred.TokenID); err != nil {
		return nil, err
	}
	if plan.Supervisor, err = p.artifactRef(supervisor, version, cred.TokenID); err != nil {
		return nil, err
	}
	if err := plan.Check(); err != nil {
		// a value that the grammar refuses can only come from a setting (server.publicUrl, opamp.path)
		return nil, refuse(domain.ErrPrecondition, CodeServerSettings, "the plan cannot be written in its line format (check server.publicUrl and opamp.path): %v", err)
	}
	return plan, nil
}

func (p *Planner) artifactRef(a *domain.Artifact, version string, token uuid.UUID) (PlanArtifact, error) {
	u, err := p.d.Artifacts.InstallURL(a)
	if err != nil {
		return PlanArtifact{}, refuse(domain.ErrPrecondition, CodeServerSettings, "%v", err)
	}
	g, err := p.d.Artifacts.InstallGrant(a, token)
	if err != nil {
		return PlanArtifact{}, fmt.Errorf("install grant for %s %s: %w", a.Distribution, version, err)
	}
	return PlanArtifact{Version: version, URL: u, Grant: g}, nil
}

// ---------------------------------------------------------------- the request ----

// checkRequest validates and normalizes the request in place and returns the platform. The refusal is a
// *domain.ValidationError with one issue per problem.
func (p *Planner) checkRequest(req *PlanRequest) (Platform, error) {
	var issues domain.Issues
	add := func(code, path, format string, args ...any) {
		issues = append(issues, domain.Issue{Severity: domain.SeverityError, Code: code, Path: path, Message: fmt.Sprintf(format, args...)})
	}

	plat := NormalizePlatform(req.OS, req.Arch)
	platOK := true
	if plat.OS != "linux" && plat.OS != "windows" {
		add(CodeInvalidValue, "os", "os must be linux or windows")
		platOK = false
	}
	if plat.Arch != "amd64" && plat.Arch != "arm64" {
		add(CodeInvalidValue, "arch", "arch must be amd64 or arm64")
		platOK = false
	}
	if platOK {
		found := false
		for _, s := range p.platforms {
			found = found || s == plat
		}
		if !found {
			var list []string
			for _, s := range p.platforms {
				list = append(list, s.String())
			}
			add(CodePlatformNotSupported, "os", "hosts on %s cannot be installed from this server (install.platforms: %s)", plat, strings.Join(list, ", "))
		}
	}

	allowed := map[string]bool{}
	for _, k := range AllowedAttributes {
		allowed[k] = true
	}
	keys := make([]string, 0, len(req.Attributes))
	for k := range req.Attributes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 2*len(AllowedAttributes) { // not a request of an installer: one message instead of one per key
		add(CodeUnknownAttribute, "attributes", "at most %d attributes are accepted (%s)", 2*len(AllowedAttributes), strings.Join(AllowedAttributes, ", "))
		keys = nil
	}
	for _, k := range keys {
		v := req.Attributes[k]
		path := "attributes." + safeKey(k)
		switch {
		case !allowed[k]:
			add(CodeUnknownAttribute, path, "the attribute %q is not accepted (accepted: %s; os.type and host.arch come from os and arch, labels from the enrollment token)",
				safeKey(k), strings.Join(AllowedAttributes, ", "))
		case !validAttributeValue(v):
			add(CodeInvalidValue, path, "the value of %s must be 1 to %d characters without control characters or surrounding spaces", k, MaxAttributeBytes)
		}
	}
	if req.Fleet != "" && !domain.ValidDNSLabel(req.Fleet) {
		add(CodeInvalidValue, "fleet", "fleet must be the name of a fleet (lower case letters, digits and \"-\", at most 63 characters)")
	}
	if req.CollectorVersion != "" {
		v := domain.NormalizeVersion(req.CollectorVersion)
		if _, err := semver.StrictNewVersion(v); err != nil || len(v) > 64 || !versionValue.MatchString(v) {
			add(CodeInvalidValue, "collectorVersion", "collectorVersion must be a version such as 0.162.0")
		} else {
			req.CollectorVersion = v
		}
	}
	if len(issues) > 0 {
		return Platform{}, domain.NewValidationError(issues...)
	}
	return plat, nil
}

// safeKey shortens an attribute key from the request for a message and drops what is not printable.
func safeKey(k string) string {
	var b strings.Builder
	for _, r := range k {
		if b.Len() >= 40 {
			b.WriteString("...")
			break
		}
		if r > unicode.MaxASCII || !unicode.IsPrint(r) {
			r = '?'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func validAttributeValue(v string) bool {
	if v == "" || len(v) > MaxAttributeBytes || !utf8.ValidString(v) || strings.TrimSpace(v) != v {
		return false
	}
	return !strings.ContainsFunc(v, func(r rune) bool { return unicode.IsControl(r) || r == utf8.RuneError })
}

// ----------------------------------------------------------------- the version ----

func noTarget(fleet string, source domain.VersionTargetSource) *Refusal {
	if source == domain.TargetSourceHold {
		return refuse(domain.ErrPrecondition, CodeNoTargetVersion,
			"the fleet %s is on hold: the version chosen in the UI is outside the frame in Git, so the fleet has no target version until an operator chooses one or Git sets collector.version; install with an explicit version on the manual path",
			fleet)
	}
	return refuse(domain.ErrPrecondition, CodeNoTargetVersion,
		"the fleet %s has no target version (spec.collector.version is empty and no version was chosen in the UI); install with an explicit version on the manual path",
		fleet)
}

// rolloutBase returns the version a new host takes while a version rollout of the fleet is under way: the fromRef
// of that rollout, or "" when none is. A rollout that is paused, failed or rolling back, or whose hosts start from
// different versions, is a refusal.
func (p *Planner) rolloutBase(ctx context.Context, fleet string) (string, error) {
	list, _, err := p.d.Store.Rollouts().List(ctx, store.RolloutFilter{
		Fleets: []string{fleet}, Kinds: []domain.RolloutKind{domain.RolloutKindVersion}, ActiveOnly: true, Limit: 20,
	})
	if err != nil {
		return "", fmt.Errorf("%w: list the version rollouts: %v", domain.ErrUnavailable, err)
	}
	var running []domain.Rollout
	for _, r := range list {
		switch r.Status {
		case domain.RolloutPaused, domain.RolloutFailed, domain.RolloutRollingBack:
			return "", refuse(domain.ErrConflict, CodeRolloutBlocking,
				"the version rollout %s of the fleet %s is %s: a host cannot be installed until an operator has resolved it", r.ID, fleet, strings.ReplaceAll(string(r.Status), "_", " "))
		case domain.RolloutPending, domain.RolloutRunning, domain.RolloutWaitingApproval:
			running = append(running, r)
		}
	}
	base := ""
	for _, r := range running {
		from := domain.NormalizeVersion(r.FromRef)
		if from == "" || (base != "" && from != base) {
			return "", refuse(domain.ErrConflict, CodeRolloutBlocking,
				"the version rollout %s of the fleet %s starts from hosts with different versions, so there is no single version for a new host while it runs", r.ID, fleet)
		}
		base = from
	}
	return base, nil
}

// allowedVersion decides whether the host may ask for version want instead of base: the target version of the
// fleet and the version it would get anyway are fine, and so is every version inside the frame in Git
// (spec.update.selection). Without a frame no other version can be installed.
func allowedVersion(f *domain.Fleet, want, base, target string) error {
	if want == base || want == target || f.Update.Selection.Active() && f.Update.Selection.Allows(want) {
		return nil
	}
	msg := fmt.Sprintf("the fleet %s has no version frame (update.selection is not enabled): only the version %s can be installed", f.Name, base)
	if f.Update.Selection.Active() {
		msg = fmt.Sprintf("version %s is outside the frame of the fleet %s (%s)", want, f.Name, f.Update.Selection.FrameText())
	}
	return domain.NewValidationError(domain.Issue{Severity: domain.SeverityError, Code: CodeVersionNotAllowed, Path: "collectorVersion", Message: msg})
}

// knownRelease requires that the release tracker knows the release. A version that the host asked for is a
// problem of the request (422); a version that the fleet itself names is a state of the server (412).
func (p *Planner) knownRelease(ctx context.Context, dist, version string, requested bool) error {
	_, err := p.d.Store.Releases().Get(ctx, dist, version)
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, domain.ErrNotFound):
		return fmt.Errorf("%w: read the release: %v", domain.ErrUnavailable, err)
	}
	what, code := dist, CodeUnknownVersion
	if dist == domain.DistributionSupervisor {
		code = CodeUnknownSupervisorVersion
	}
	msg := fmt.Sprintf("the release tracker does not know %s %s, so the server does not offer it (check releases.enabled and releases.minVersion, or wait for the next poll)", what, version)
	if requested {
		return domain.NewValidationError(domain.Issue{Severity: domain.SeverityError, Code: code, Path: "collectorVersion", Message: msg})
	}
	return refuse(domain.ErrPrecondition, code, "%s", msg)
}

// ----------------------------------------------------------------- the artifacts ----

func (p *Planner) prepareError(dist, version string, err error) error {
	if errors.Is(err, domain.ErrInvalidArgument) {
		return refuse(domain.ErrPrecondition, CodeUnknownVersion, "%s %s cannot be installed: %v", dist, version, err)
	}
	return fmt.Errorf("%w: prepare %s %s: %v", domain.ErrUnavailable, dist, version, err)
}

func notReady(plat Platform, states ...artifact.Readiness) *Refusal {
	failed := false
	for _, s := range states {
		failed = failed || s == artifact.PrepareFailed
	}
	if failed {
		r := refuse(domain.ErrUnavailable, CodePreparing,
			"the last attempt to prepare the collector and the supervisor for %s failed; the server tries again, see the artifact list of the server for the reason", plat)
		r.RetryAfter = retryFailed
		return r
	}
	r := refuse(domain.ErrUnavailable, CodePreparing,
		"the collector and the supervisor for %s are being prepared (the first request for a platform downloads about 112 MB): ask again after the time in Retry-After", plat)
	r.RetryAfter = retryPreparing
	return r
}

// opampEndpoint is the OpAMP endpoint for the public URL: wss:// for https, ws:// for http (which production
// refuses), plus the path of the OpAMP endpoint.
func opampEndpoint(publicURL, path string) (string, error) {
	u, err := url.Parse(publicURL)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("server.publicUrl is not set to an absolute http(s) URL: the plan cannot name the OpAMP endpoint")
	}
	scheme := "wss"
	if u.Scheme == "http" {
		scheme = "ws"
	}
	return scheme + "://" + u.Host + strings.TrimRight(u.Path, "/") + path, nil
}

func findFleet(snap *domain.GitOpsSnapshot, name string) *domain.Fleet {
	if snap == nil {
		return nil
	}
	for i := range snap.Fleets {
		if snap.Fleets[i].Name == name {
			return &snap.Fleets[i]
		}
	}
	return nil
}
