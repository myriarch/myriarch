// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package blueprint

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/domain"
)

var (
	objectNameRE   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	fragmentNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
	headerNameRE   = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
)

// reservedHeaderPrefix (lower case) is the header prefix executors may not
// set: the server adds its own delivery, timestamp and signature headers.
var reservedHeaderPrefix = strings.ToLower(brand.WebhookHeaderPrefix)

// validator accumulates issues of ValidateSnapshot.
type validator struct {
	r      *Renderer
	snap   *domain.GitOpsSnapshot
	issues domain.Issues
	seen   map[string]bool
	// syntaxObjects are objects with a static template_syntax finding.
	syntaxObjects map[string]bool
	// bridges caches the synthetic OpAMP Bridge views (bridgeViews).
	bridges []bridgeView
}

func (v *validator) add(sev domain.IssueSeverity, code, msg string, src domain.SourceRef, object, path string) {
	is := domain.Issue{Severity: sev, Code: code, Message: msg, File: src.Path, Object: object, Path: path}
	if code == "template_syntax" {
		v.syntaxObjects[object] = true
	}
	key := string(sev) + "|" + code + "|" + is.File + "|" + object + "|" + path + "|" + msg
	if v.seen[key] {
		return
	}
	v.seen[key] = true
	v.issues = append(v.issues, is)
}

func (v *validator) err(code, msg string, src domain.SourceRef, object, path string) {
	v.add(domain.SeverityError, code, msg, src, object, path)
}

// ValidateSnapshot validates all objects of a snapshot: schema, names,
// references (fleet -> blueprint/policy/executor), extends cycles,
// parameter declarations and values, template syntax, selectors, policies,
// maintenance windows, STATIC executor checks (no environment access), and
// renders every fleet for every matching sample. Environment independent:
// myrctl in CI and myriarchd must produce the same issues.
func (r *Renderer) ValidateSnapshot(snap *domain.GitOpsSnapshot, opts ValidateOptions) domain.Issues {
	v := &validator{r: r, snap: snap, issues: domain.Issues{}, seen: map[string]bool{}, syntaxObjects: map[string]bool{}}
	if snap == nil {
		return v.issues
	}

	v.checkDuplicates()
	for i := range snap.Blueprints {
		v.checkBlueprint(&snap.Blueprints[i])
	}
	for i := range snap.RolloutPolicies {
		v.checkPolicy(&snap.RolloutPolicies[i])
	}
	for i := range snap.Executors {
		v.checkExecutor(&snap.Executors[i])
	}
	for i := range snap.Inventories {
		v.checkInventory(&snap.Inventories[i])
	}
	for i := range snap.Fleets {
		v.checkFleet(&snap.Fleets[i])
	}
	v.renderSamples(opts)

	sortIssues(v.issues)
	return v.issues
}

func objRef(kind domain.ObjectKind, name string) string { return string(kind) + "/" + name }

func (v *validator) checkName(kind domain.ObjectKind, name string, src domain.SourceRef) {
	if !objectNameRE.MatchString(name) {
		v.err("invalid_name", fmt.Sprintf("%s name %q is invalid (must match %s)", kind, name, objectNameRE.String()), src, objRef(kind, name), "metadata.name")
	}
}

func (v *validator) checkDuplicates() {
	type key struct {
		kind domain.ObjectKind
		name string
	}
	first := map[key]string{}
	note := func(kind domain.ObjectKind, name string, src domain.SourceRef) {
		k := key{kind, name}
		if prev, ok := first[k]; ok {
			v.err("duplicate_object", fmt.Sprintf("%s %q is defined more than once (first in %s)", kind, name, prev), src, objRef(kind, name), "metadata.name")
			return
		}
		first[k] = src.Path
	}
	s := v.snap
	for _, o := range s.Blueprints {
		note(domain.KindBlueprint, o.Name, o.Source)
	}
	for _, o := range s.Fleets {
		note(domain.KindFleet, o.Name, o.Source)
	}
	for _, o := range s.RolloutPolicies {
		note(domain.KindRolloutPolicy, o.Name, o.Source)
	}
	for _, o := range s.Executors {
		note(domain.KindExecutor, o.Name, o.Source)
	}
	for _, o := range s.Inventories {
		note(domain.KindInventory, o.Name, o.Source)
	}
}

// -------------------------------------------------------------- blueprint --

func (v *validator) checkBlueprint(bp *domain.Blueprint) {
	obj := objRef(domain.KindBlueprint, bp.Name)
	v.checkName(domain.KindBlueprint, bp.Name, bp.Source)

	// extends: missing parent / cycle.
	if bp.Extends != "" {
		chain, is, cycle := walkChain(v.snap, bp.Name, "")
		for _, i := range is {
			switch i.Code {
			case "extends_cycle":
				if len(cycle) > 0 && cycle[0] == bp.Name && bp.Name == minString(cycle) {
					v.err(i.Code, i.Message, bp.Source, obj, "spec.extends")
				}
			default:
				if i.Object == obj {
					v.err(i.Code, i.Message, bp.Source, obj, i.Path)
				}
			}
		}
		if len(is) == 0 {
			_, cissues := declareParams(chain)
			for _, i := range cissues {
				if i.Object == obj {
					v.err(i.Code, i.Message, bp.Source, obj, i.Path)
				}
			}
		}
	}

	// parameters.
	seen := map[string]bool{}
	for i, p := range bp.Parameters {
		path := fmt.Sprintf("spec.parameters[%d]", i)
		if !paramNameRE.MatchString(p.Name) {
			v.err("invalid_parameter", fmt.Sprintf("parameter name %q is invalid (must match %s)", p.Name, paramNameRE.String()), bp.Source, obj, path+".name")
			continue
		}
		if seen[p.Name] {
			v.err("invalid_parameter", fmt.Sprintf("parameter %q is declared more than once", p.Name), bp.Source, obj, path)
			continue
		}
		seen[p.Name] = true
		if !validParamType(p.Type) {
			v.err("invalid_parameter", fmt.Sprintf("parameter %q has unknown type %q (string, int, number, bool, duration, list, map)", p.Name, p.Type), bp.Source, obj, path+".type")
			continue
		}
		if p.Default != nil {
			if _, err := coerceValue(p.Type, p.Default); err != nil {
				v.err("invalid_parameter", fmt.Sprintf("default of parameter %q does not match type %s: %v", p.Name, p.Type, err), bp.Source, obj, path+".default")
			}
		}
		if p.Pattern != "" {
			if p.Type != domain.ParamString {
				v.err("invalid_parameter", fmt.Sprintf("parameter %q: pattern is only allowed for type string", p.Name), bp.Source, obj, path+".pattern")
			} else if _, err := v.r.fullMatchRegexp(p.Pattern); err != nil {
				v.err("invalid_parameter", fmt.Sprintf("parameter %q: invalid pattern: %v", p.Name, err), bp.Source, obj, path+".pattern")
			}
		}
		if (p.Min != nil || p.Max != nil) && p.Type != domain.ParamInt && p.Type != domain.ParamNumber {
			v.err("invalid_parameter", fmt.Sprintf("parameter %q: min/max are only allowed for int and number", p.Name), bp.Source, obj, path)
		}
		if p.Min != nil && p.Max != nil && *p.Min > *p.Max {
			v.err("invalid_parameter", fmt.Sprintf("parameter %q: min is greater than max", p.Name), bp.Source, obj, path)
		}
		for j, e := range p.Enum {
			if _, err := coerceValue(p.Type, e); err != nil {
				v.err("invalid_parameter", fmt.Sprintf("parameter %q: enum[%d]: %v", p.Name, j, err), bp.Source, obj, fmt.Sprintf("%s.enum[%d]", path, j))
			}
		}
	}

	// config / fragments / remove.
	v.checkConfigTree(bp, obj, bp.Config, "spec.config")
	names := map[string]bool{}
	for i, fr := range bp.Fragments {
		path := fmt.Sprintf("spec.fragments[%d]", i)
		if !fragmentNameRE.MatchString(fr.Name) {
			v.err("invalid_name", fmt.Sprintf("fragment name %q is invalid (must match %s)", fr.Name, fragmentNameRE.String()), bp.Source, obj, path+".name")
		} else if names[fr.Name] {
			v.err("duplicate_object", fmt.Sprintf("fragment %q is defined more than once in blueprint %q", fr.Name, bp.Name), bp.Source, obj, path+".name")
		}
		names[fr.Name] = true
		if w := strings.TrimSpace(fr.When); w != "" {
			e := v.r.parseEntry(fr.When)
			switch {
			case e.err != nil:
				v.err("template_syntax", fmt.Sprintf("fragment %q: when: %v", fr.Name, e.err), bp.Source, obj, path+".when")
			case !e.whole:
				v.err("template_syntax", fmt.Sprintf("fragment %q: when must be exactly one \"[[ expression ]]\" evaluating to a bool", fr.Name), bp.Source, obj, path+".when")
			}
		}
		v.checkConfigTree(bp, obj, fr.Config, path+".config")
		v.checkRemovePaths(bp, obj, fr.Remove, path+".remove")
	}
	v.checkRemovePaths(bp, obj, bp.Remove, "spec.remove")
}

func minString(ss []string) string {
	m := ss[0]
	for _, s := range ss[1:] {
		if s < m {
			m = s
		}
	}
	return m
}

func (v *validator) checkRemovePaths(bp *domain.Blueprint, obj string, paths []string, base string) {
	for i, p := range paths {
		path := fmt.Sprintf("%s[%d]", base, i)
		if strings.TrimSpace(p) == "" || strings.Contains(p, leftDelim) {
			v.err("invalid_field", fmt.Sprintf("remove path %q is invalid (confmap path like receivers::host_metrics, no templates)", p), bp.Source, obj, path)
			continue
		}
		for _, part := range strings.Split(p, pathSeparator) {
			if part == "" {
				v.err("invalid_field", fmt.Sprintf("remove path %q contains an empty segment", p), bp.Source, obj, path)
				break
			}
		}
	}
}

// checkConfigTree checks template syntax and forbidden keys in a config tree.
func (v *validator) checkConfigTree(bp *domain.Blueprint, obj string, tree map[string]any, base string) {
	if len(tree) == 0 {
		return
	}
	var walk func(x any, path string)
	walk = func(x any, path string) {
		switch t := x.(type) {
		case map[string]any:
			for _, k := range sortedKeys(t) {
				kp := path + pathSeparator + k
				if containsPathSeparator(k) {
					v.err("invalid_config_key", fmt.Sprintf("config key %q contains \"::\", which confmap interprets as path separator", k), bp.Source, obj, kp)
				}
				if strings.Contains(k, leftDelim) {
					if e := v.r.parseEntry(k); e.err != nil {
						v.err("template_syntax", fmt.Sprintf("%s: %v", kp, e.err), bp.Source, obj, kp)
					}
				}
				walk(t[k], kp)
			}
		case map[any]any:
			walk(normalizeTree(t), path)
		case []any:
			for i, e := range t {
				walk(e, fmt.Sprintf("%s[%d]", path, i))
			}
		case string:
			if strings.Contains(t, leftDelim) {
				if e := v.r.parseEntry(t); e.err != nil {
					v.err("template_syntax", fmt.Sprintf("%s: %v", path, e.err), bp.Source, obj, path)
				}
			}
		}
	}
	walk(tree, base)
}

// ----------------------------------------------------------------- policy --

func (v *validator) checkPolicy(p *domain.RolloutPolicy) {
	obj := objRef(domain.KindRolloutPolicy, p.Name)
	v.checkName(domain.KindRolloutPolicy, p.Name, p.Source)
	src := p.Source

	names := map[string]bool{}
	lastPercent := 0
	for i, w := range p.Waves {
		path := fmt.Sprintf("spec.waves[%d]", i)
		if strings.TrimSpace(w.Name) == "" {
			v.err("wave_invalid", "wave has no name", src, obj, path+".name")
		} else if names[w.Name] {
			v.err("wave_invalid", fmt.Sprintf("wave name %q is used more than once", w.Name), src, obj, path+".name")
		}
		names[w.Name] = true
		switch {
		case w.Selector != nil && w.Percent != 0:
			v.err("wave_invalid", fmt.Sprintf("wave %q must have either selector or percent, not both", w.Name), src, obj, path)
		case w.Selector == nil && w.Percent == 0:
			v.err("wave_invalid", fmt.Sprintf("wave %q needs a selector or a percent (1-100)", w.Name), src, obj, path)
		case w.Selector != nil:
			if err := w.Selector.Validate(); err != nil {
				v.err("selector_invalid", fmt.Sprintf("wave %q: %v", w.Name, trimValidation(err)), src, obj, path+".selector")
			}
		default:
			if w.Percent < 1 || w.Percent > 100 {
				v.err("wave_invalid", fmt.Sprintf("wave %q: percent %d is out of range 1-100", w.Name, w.Percent), src, obj, path+".percent")
			} else if w.Percent <= lastPercent {
				v.err("wave_invalid", fmt.Sprintf("wave %q: percent %d must be greater than the previous percent wave (%d)", w.Name, w.Percent, lastPercent), src, obj, path+".percent")
			} else {
				lastPercent = w.Percent
			}
		}
	}
	if p.MaxConcurrent < 0 {
		v.err("policy_invalid", "maxConcurrent must not be negative", src, obj, "spec.maxConcurrent")
	}
	for _, d := range []struct {
		name string
		val  domain.Duration
	}{{"healthWait", p.HealthWait}, {"applyTimeout", p.ApplyTimeout}, {"offlineGracePeriod", p.OfflineGracePeriod}} {
		if d.val < 0 {
			v.err("policy_invalid", d.name+" must not be negative", src, obj, "spec."+d.name)
		}
	}
	ft := p.FailureThreshold
	if ft.MaxFailed < 0 || ft.MaxFailedPercent < 0 || ft.MaxFailedPercent > 100 {
		v.err("policy_invalid", "failureThreshold: maxFailed must be >= 0 and maxFailedPercent 0-100", src, obj, "spec.failureThreshold")
	}
	switch p.OnFailure {
	case "", domain.FailurePause, domain.FailureRollback:
	default:
		v.err("policy_invalid", fmt.Sprintf("onFailure %q is invalid (pause or rollback)", p.OnFailure), src, obj, "spec.onFailure")
	}
	if p.DefaultTimezone != "" {
		if _, err := time.LoadLocation(p.DefaultTimezone); err != nil {
			v.err("policy_invalid", fmt.Sprintf("defaultTimezone %q is not a valid IANA time zone", p.DefaultTimezone), src, obj, "spec.defaultTimezone")
		}
	}
	for i, w := range p.MaintenanceWindows {
		path := fmt.Sprintf("spec.maintenanceWindows[%d]", i)
		if _, ok := ParseClock(w.Start); !ok {
			v.err("window_invalid", fmt.Sprintf("start %q is not a valid HH:MM time (quote the value)", w.Start), src, obj, path+".start")
		}
		if _, ok := ParseClock(w.End); !ok {
			v.err("window_invalid", fmt.Sprintf("end %q is not a valid HH:MM time (quote the value)", w.End), src, obj, path+".end")
		}
		for j, d := range w.Days {
			if _, ok := ParseWeekday(d); !ok {
				v.err("window_invalid", fmt.Sprintf("day %q is invalid (Mon, Tue, Wed, Thu, Fri, Sat, Sun)", d), src, obj, fmt.Sprintf("%s.days[%d]", path, j))
			}
		}
		if w.Timezone != "" && w.Timezone != "agent" {
			if _, err := time.LoadLocation(w.Timezone); err != nil {
				v.err("window_invalid", fmt.Sprintf("timezone %q is not a valid IANA time zone (or \"agent\")", w.Timezone), src, obj, path+".timezone")
			}
		}
	}
}

func trimValidation(err error) string {
	return strings.TrimPrefix(err.Error(), domain.ErrValidation.Error()+": ")
}

// --------------------------------------------------------------- executor --

func (v *validator) checkExecutor(e *domain.Executor) {
	obj := objRef(domain.KindExecutor, e.Name)
	v.checkName(domain.KindExecutor, e.Name, e.Source)
	src := e.Source
	checkURL := func(raw, field string) {
		u, err := url.Parse(raw)
		switch {
		case raw == "":
			v.err("missing_field", "url is required", src, obj, field)
		case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
			v.err("executor_invalid", fmt.Sprintf("url %q must be an absolute http(s) URL", redactURL(raw)), src, obj, field)
		case u.User != nil:
			v.err("executor_invalid", "url must not contain credentials (use secretEnv/secretFile or tokenEnv/tokenFile)", src, obj, field)
		}
	}
	checkDur := func(d domain.Duration, field string) {
		if d < 0 {
			v.err("executor_invalid", field+" must not be negative", src, obj, field)
		}
		if d.D() > MaxExecutorTimeout {
			v.err("executor_invalid", fmt.Sprintf("%s must not exceed %s (myriarchd caps executor calls; rollouts.executorTimeout and executors.maxTimeout may be lower)", field, MaxExecutorTimeout), src, obj, field)
		}
	}
	switch e.Type {
	case domain.ExecutorWebhook:
		w := e.Webhook
		if w == nil {
			v.err("missing_field", "spec.webhook is required for type webhook", src, obj, "spec.webhook")
			return
		}
		if e.AWX != nil {
			v.err("executor_invalid", "spec.awx must not be set for type webhook", src, obj, "spec.awx")
		}
		checkURL(w.URL, "spec.webhook.url")
		switch strings.ToUpper(w.Method) {
		case "", "POST", "PUT", "PATCH":
		default:
			v.err("executor_invalid", fmt.Sprintf("method %q is not supported (POST, PUT, PATCH)", w.Method), src, obj, "spec.webhook.method")
		}
		if w.SecretEnv == "" && w.SecretFile == "" {
			v.err("executor_invalid", "a secret reference is required: set secretEnv or secretFile", src, obj, "spec.webhook")
		}
		if w.SecretEnv != "" && w.SecretFile != "" {
			v.err("executor_invalid", "set only one of secretEnv and secretFile", src, obj, "spec.webhook")
		}
		for _, k := range sortedKeys(w.Headers) {
			lk := strings.ToLower(k)
			switch {
			case !headerNameRE.MatchString(k):
				v.err("executor_invalid", fmt.Sprintf("header name %q is invalid", k), src, obj, "spec.webhook.headers::"+k)
			case strings.HasPrefix(lk, reservedHeaderPrefix):
				v.err("executor_invalid", fmt.Sprintf("header %q is reserved", k), src, obj, "spec.webhook.headers::"+k)
			case lk == "authorization" || lk == "cookie" || lk == "proxy-authorization" || lk == "host" || lk == "content-length":
				v.err("executor_invalid", fmt.Sprintf("header %q must not be set here (credentials belong in secretEnv/secretFile)", k), src, obj, "spec.webhook.headers::"+k)
			}
		}
		checkDur(w.Timeout, "spec.webhook.timeout")
		if w.BatchSize < 0 {
			v.err("executor_invalid", "batchSize must not be negative", src, obj, "spec.webhook.batchSize")
		}
	case domain.ExecutorAWX:
		a := e.AWX
		if a == nil {
			v.err("missing_field", "spec.awx is required for type awx", src, obj, "spec.awx")
			return
		}
		if e.Webhook != nil {
			v.err("executor_invalid", "spec.webhook must not be set for type awx", src, obj, "spec.webhook")
		}
		checkURL(a.URL, "spec.awx.url")
		if a.APIPrefix != "" && !validAPIPrefix(a.APIPrefix) {
			v.err("executor_invalid", "apiPrefix must be an absolute path such as /api/v2/ or /api/controller/v2/ (segments of letters, digits, \".\", \"_\", \"~\" or \"-\"; no \".\" or \"..\" segments)", src, obj, "spec.awx.apiPrefix")
		}
		if a.JobTemplateID <= 0 {
			v.err("missing_field", "jobTemplateId is required (positive integer)", src, obj, "spec.awx.jobTemplateId")
		}
		if a.TokenEnv == "" && a.TokenFile == "" {
			v.err("executor_invalid", "a token reference is required: set tokenEnv or tokenFile", src, obj, "spec.awx")
		}
		if a.TokenEnv != "" && a.TokenFile != "" {
			v.err("executor_invalid", "set only one of tokenEnv and tokenFile", src, obj, "spec.awx")
		}
		checkDur(a.Timeout, "spec.awx.timeout")
		if a.BatchSize < 0 {
			v.err("executor_invalid", "batchSize must not be negative", src, obj, "spec.awx.batchSize")
		}
		for p, err := range v.r.CheckTemplates(a.ExtraVars) {
			v.err("template_syntax", fmt.Sprintf("extraVars: %s: %v", p, err), src, obj, "spec.awx.extraVars::"+p)
		}
	case "":
		v.err("missing_field", "type is required (webhook or awx)", src, obj, "spec.type")
	default:
		v.err("executor_invalid", fmt.Sprintf("type %q is not supported (webhook or awx)", e.Type), src, obj, "spec.type")
	}
}

// apiPrefixRE is the grammar of spec.awx.apiPrefix: an absolute path of plain
// segments with an optional trailing slash.
var apiPrefixRE = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)+/?$`)

// validAPIPrefix reports whether p is an acceptable AWX API prefix: it matches
// apiPrefixRE and has no "." or ".." segment.
func validAPIPrefix(p string) bool {
	if !apiPrefixRE.MatchString(p) {
		return false
	}
	for _, seg := range strings.Split(strings.Trim(p, "/"), "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// MaxExecutorTimeout is the longest timeout an Executor object may request.
const MaxExecutorTimeout = 5 * time.Minute

// redactURL strips credentials and query from a URL for messages.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<invalid>"
	}
	return u.Scheme + "://" + u.Host + u.Path
}

// -------------------------------------------------------------- inventory --

func (v *validator) checkInventory(inv *domain.Inventory) {
	obj := objRef(domain.KindInventory, inv.Name)
	v.checkName(domain.KindInventory, inv.Name, inv.Source)
	for i, e := range inv.Entries {
		path := fmt.Sprintf("spec.entries[%d]", i)
		if len(e.Match) == 0 {
			v.err("missing_field", "entry needs a non-empty match (it would apply to every agent)", inv.Source, obj, path+".match")
		}
		for _, k := range sortedKeys(e.Match) {
			if strings.HasPrefix(k, domain.LabelPrefix) {
				v.err("invalid_field", fmt.Sprintf("match key %q: match only sees agent attributes, not labels", k), inv.Source, obj, path+".match::"+k)
			}
		}
		for _, k := range sortedKeys(e.Labels) {
			if k == "" || strings.Contains(k, " ") {
				v.err("invalid_field", fmt.Sprintf("label key %q is invalid", k), inv.Source, obj, path+".labels::"+k)
			}
		}
	}
}

// ------------------------------------------------------------------ fleet --

func (v *validator) checkFleet(f *domain.Fleet) {
	obj := objRef(domain.KindFleet, f.Name)
	v.checkName(domain.KindFleet, f.Name, f.Source)
	src := f.Source

	if err := f.Selector.Validate(); err != nil {
		v.err("selector_invalid", trimValidation(err), src, obj, "spec.selector")
	}
	for k := range f.Selector.MatchAttributes {
		if strings.TrimSpace(k) == "" {
			v.err("selector_invalid", "matchAttributes contains an empty key", src, obj, "spec.selector.matchAttributes")
		}
	}

	if f.Blueprint == "" {
		v.err("missing_field", "blueprint is required", src, obj, "spec.blueprint")
	} else if _, is := resolveChain(v.snap, f.Blueprint, f.Name); len(is) > 0 {
		for _, i := range is {
			// extends problems are reported by the blueprint itself.
			if i.Object == obj {
				v.err(i.Code, i.Message, src, obj, i.Path)
			}
		}
	}

	if d := f.Collector.Distribution; d != "" && d != domain.DistributionContrib {
		v.err("invalid_field", fmt.Sprintf("distribution %q is not supported (otelcol-contrib)", d), src, obj, "spec.collector.distribution")
	}
	if ver := f.Collector.Version; ver != "" {
		if strings.HasPrefix(ver, "v") {
			v.err("invalid_field", fmt.Sprintf("collector version %q must be given without a leading \"v\"", ver), src, obj, "spec.collector.version")
		} else if _, err := semver.StrictNewVersion(ver); err != nil {
			v.err("invalid_field", fmt.Sprintf("collector version %q is not a valid semantic version (e.g. 0.162.0)", ver), src, obj, "spec.collector.version")
		}
	}

	// update executor.
	selectionOn := f.Update.Selection.Active()
	noVersionHint := func(exec domain.ExecutorType) {
		if f.Collector.Version == "" && !selectionOn {
			v.add(domain.SeverityWarning, "missing_field", fmt.Sprintf("update.executor is %s but collector.version is empty: nothing will be rolled out", exec), src, obj, "spec.collector.version")
		}
	}
	switch f.Update.Executor {
	case "", domain.ExecutorNone, domain.ExecutorUpdater, domain.ExecutorManual:
		if f.Update.ExecutorRef != "" {
			v.add(domain.SeverityWarning, "executor_invalid", fmt.Sprintf("executorRef %q is ignored for executor %q", f.Update.ExecutorRef, orNone(f.Update.Executor)), src, obj, "spec.update.executorRef")
		}
		if f.Update.Executor == domain.ExecutorUpdater || f.Update.Executor == domain.ExecutorManual {
			noVersionHint(f.Update.Executor)
		}
	case domain.ExecutorWebhook, domain.ExecutorAWX:
		if f.Update.ExecutorRef == "" {
			v.err("missing_field", fmt.Sprintf("executorRef is required for executor %q", f.Update.Executor), src, obj, "spec.update.executorRef")
		} else if ex := v.findExecutor(f.Update.ExecutorRef); ex == nil {
			v.err("executor_not_found", fmt.Sprintf("executor %q does not exist", f.Update.ExecutorRef), src, obj, "spec.update.executorRef")
		} else if ex.Type != f.Update.Executor {
			v.err("executor_invalid", fmt.Sprintf("executor %q has type %q but the fleet uses %q", ex.Name, ex.Type, f.Update.Executor), src, obj, "spec.update.executorRef")
		}
		noVersionHint(f.Update.Executor)
	default:
		v.err("executor_invalid", fmt.Sprintf("update.executor %q is invalid (none, updater, webhook, awx, manual)", f.Update.Executor), src, obj, "spec.update.executor")
	}
	v.checkSelection(f)
	for p, err := range v.r.CheckTemplates(f.Update.Params) {
		v.err("template_syntax", fmt.Sprintf("update.params: %s: %v", p, err), src, obj, "spec.update.params::"+p)
	}

	// policies.
	for _, ref := range []struct{ name, field string }{{f.Rollout.ConfigPolicy, "spec.rollout.configPolicy"}, {f.Rollout.VersionPolicy, "spec.rollout.versionPolicy"}} {
		if ref.name == "" || ref.name == domain.DefaultRolloutPolicyName {
			continue
		}
		if v.findPolicy(ref.name) == nil {
			v.err("policy_not_found", fmt.Sprintf("rollout policy %q does not exist", ref.name), src, obj, ref.field)
		}
	}
	v.checkBridgeSelection(f)
}

// checkSelection validates spec.update.selection, the frame for UI-selected
// target versions (docs/concepts/fleets.md, "The frame").
func (v *validator) checkSelection(f *domain.Fleet) {
	sel := f.Update.Selection
	if sel == nil {
		return
	}
	obj := objRef(domain.KindFleet, f.Name)
	src := f.Source
	const base = "spec.update.selection"
	bad := func(msg, field string) { v.err("selection_invalid", msg, src, obj, base+field) }

	if !sel.Enabled {
		if sel.VersionRange != "" || len(sel.Versions) > 0 {
			v.add(domain.SeverityWarning, "selection_invalid", "update.selection is disabled: versionRange/versions are ignored", src, obj, base)
		}
		return
	}

	hasRange, hasList := sel.VersionRange != "", len(sel.Versions) > 0
	frameOK := hasRange != hasList
	if !frameOK {
		bad("exactly one of update.selection.versionRange or update.selection.versions is required", "")
	}
	if hasRange {
		if _, err := semver.NewConstraint(sel.VersionRange); err != nil {
			bad(fmt.Sprintf("versionRange %q is not a valid semantic version constraint", sel.VersionRange), ".versionRange")
			frameOK = false
		}
	}
	if hasList {
		seen := map[string]bool{}
		for i, ver := range sel.Versions {
			field := fmt.Sprintf(".versions[%d]", i)
			if _, err := semver.StrictNewVersion(ver); err != nil || strings.HasPrefix(ver, "v") {
				bad(fmt.Sprintf("versions[%d] %q must be a semantic version without \"v\"", i, ver), field)
				frameOK = false
				continue
			}
			if seen[ver] {
				bad(fmt.Sprintf("versions[%d] %q is listed twice", i, ver), field)
				frameOK = false
			}
			seen[ver] = true
		}
	}
	if !domain.VersionRolloutsEnabled(f) {
		bad("UI-triggered updates need an update executor (update.executor is none)", "")
	}
	if cv := f.Collector.Version; frameOK && cv != "" {
		if _, err := semver.StrictNewVersion(domain.NormalizeVersion(cv)); err == nil && !sel.Allows(cv) {
			bad(fmt.Sprintf("collector.version %s is outside update.selection (%s)", domain.NormalizeVersion(cv), sel.FrameText()), "")
		}
	}
	if name := sel.RolloutPolicy; name != "" && name != domain.DefaultRolloutPolicyName && v.findPolicy(name) == nil {
		v.err("policy_not_found", fmt.Sprintf("rollout policy %q does not exist", name), src, obj, base+".rolloutPolicy")
	}
}

func orNone(t domain.ExecutorType) domain.ExecutorType {
	if t == "" {
		return domain.ExecutorNone
	}
	return t
}

func (v *validator) findExecutor(name string) *domain.Executor {
	for i := range v.snap.Executors {
		if v.snap.Executors[i].Name == name {
			return &v.snap.Executors[i]
		}
	}
	return nil
}

func (v *validator) findPolicy(name string) *domain.RolloutPolicy {
	for i := range v.snap.RolloutPolicies {
		if v.snap.RolloutPolicies[i].Name == name {
			return &v.snap.RolloutPolicies[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------- bridges --

// bridgeValue stands for every attribute value that a Bridge chooses itself
// (names, versions, identifiers): a selector that pins such an attribute to a
// value does not match it, a selector that only needs the attribute to exist
// does.
const bridgeValue = "bridge-reported"

// bridgeView is the attribute view of a synthetic OpAMP Bridge agent.
type bridgeView struct {
	mode domain.BridgeMode
	view domain.AttributeView
}

// bridgeViews returns the views of one Bridge in operator mode and one in
// standalone mode, as the Bridge reports itself
// (docs/design/upstream-notes.md, "OpAMP Bridge: identity, modes and remote
// config"), with the labels of the inventory applied. A Bridge has no os.type,
// no site.* attributes and no server labels of an enrollment token: selectors
// on those never match, which is what the warning bridge_selected relies on.
// Selectors on labels.* from a token are decided at run time by the assignment.
func (v *validator) bridgeViews() []bridgeView {
	if v.bridges != nil {
		return v.bridges
	}
	operator := map[string]string{
		domain.AttrServiceName: domain.BridgeServiceName, domain.AttrServiceInstanceID: bridgeValue, domain.AttrServiceVersion: bridgeValue,
		"os.family": "linux", domain.AttrHostName: bridgeValue, domain.AttrBridgeMode: string(domain.BridgeModeOperator),
	}
	standalone := map[string]string{
		domain.AttrServiceName: bridgeValue, domain.AttrServiceInstanceID: bridgeValue, domain.AttrServiceVersion: bridgeValue,
		"os.family": "linux", domain.AttrHostName: bridgeValue, domain.AttrBridgeMode: string(domain.BridgeModeStandalone),
		"k8s.cluster.uid": bridgeValue, "k8s.namespace.name": bridgeValue, "k8s.workload.name": bridgeValue, "k8s.workload.type": bridgeValue,
		"opentelemetry.io/agent.type": bridgeValue,
	}
	for _, b := range []struct {
		mode  domain.BridgeMode
		attrs map[string]string
	}{{domain.BridgeModeOperator, operator}, {domain.BridgeModeStandalone, standalone}} {
		v.bridges = append(v.bridges, bridgeView{mode: b.mode, view: ViewFrom(b.attrs, ApplyInventory(v.snap, b.attrs).Labels)})
	}
	return v.bridges
}

// checkBridgeSelection warns when the selector of a fleet without
// spec.bridge.manage also matches a Bridge (docs/reference/gitops-schema.md,
// issue code bridge_selected): the fleet would never configure it. Priorities
// are not considered.
func (v *validator) checkBridgeSelection(f *domain.Fleet) {
	if f.Bridge.Manage || f.Selector.Validate() != nil {
		return
	}
	var modes []domain.BridgeMode
	for _, b := range v.bridgeViews() {
		if f.Selector.Matches(b.view) {
			modes = append(modes, b.mode)
		}
	}
	if len(modes) == 0 {
		return
	}
	names := make([]string, len(modes))
	for i, m := range modes {
		names[i] = string(m)
	}
	list := strings.Join(names, " and ")
	msg := fmt.Sprintf("the selector also matches OpAMP Bridge agents (%s mode): without spec.bridge.manage the fleet configures none of them, and with it only Bridges in standalone mode; narrow the selector or set spec.bridge.manage", list)
	if len(modes) == 1 && modes[0] == domain.BridgeModeOperator {
		msg = "the selector also matches OpAMP Bridge agents in operator mode, which no fleet configures: narrow the selector"
	}
	v.add(domain.SeverityWarning, "bridge_selected", msg, f.Source, objRef(domain.KindFleet, f.Name), "spec.selector")
}

// checkBridgeSample warns for every fleet without spec.bridge.manage whose
// selector matches the sample agent that is an OpAMP Bridge. The inventory is
// applied like for every sample.
func (v *validator) checkBridgeSample(sm Sample) {
	inv := ApplyInventory(v.snap, sm.Attributes)
	labels := map[string]string{}
	for k, val := range sm.Labels {
		labels[k] = val
	}
	for k, val := range inv.Labels {
		labels[k] = val
	}
	view := ViewFrom(sm.Attributes, labels)
	mode := "mode not reported"
	if m := domain.BridgeModeOf(sm.Attributes); m != domain.BridgeModeUnknown {
		mode = string(m) + " mode"
	}
	fleets := make([]*domain.Fleet, 0, len(v.snap.Fleets))
	for i := range v.snap.Fleets {
		fleets = append(fleets, &v.snap.Fleets[i])
	}
	sort.SliceStable(fleets, func(i, j int) bool { return fleets[i].Name < fleets[j].Name })
	for _, f := range fleets {
		if f.Bridge.Manage || !f.Selector.Matches(view) {
			continue
		}
		v.addAt(domain.SeverityWarning, "bridge_selected",
			fmt.Sprintf("sample agent %q is an OpAMP Bridge (%s) and matches fleet %q, which does not set spec.bridge.manage: a Bridge like it gets no remote configuration from the fleet, and the sample is not rendered", sm.Name, mode, f.Name),
			domain.SourceRef{Path: sm.File}, sm.Line, "Sample/"+sm.Name)
	}
}

// ---------------------------------------------------------------- samples --

func (v *validator) renderSamples(opts ValidateOptions) {
	if len(opts.Samples) == 0 {
		return
	}
	samples := append([]Sample(nil), opts.Samples...)
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].Name < samples[j].Name })
	for _, sm := range samples {
		if !opts.Deadline.IsZero() && time.Now().After(opts.Deadline) {
			v.err("time_budget_exceeded", "validation took longer than the time budget; the remaining samples were not rendered", domain.SourceRef{Path: sm.File}, "Sample/"+sm.Name, "")
			return
		}
		if domain.IsOpAMPBridge(sm.Attributes) {
			// a Bridge is configured by no fleet without the opt-in and by no fleet
			// in operator mode: the sample is checked, never rendered, and does not
			// have to match a fleet
			v.checkBridgeSample(sm)
			continue
		}
		inv := ApplyInventory(v.snap, sm.Attributes)
		labels := map[string]string{}
		for k, val := range sm.Labels {
			labels[k] = val
		}
		for k, val := range inv.Labels {
			labels[k] = val
		}
		view := ViewFrom(sm.Attributes, labels)
		match := MatchFleet(v.snap, view)
		sampleSrc := domain.SourceRef{Path: sm.File}
		switch {
		case len(match.Candidates) == 0:
			v.addAt(domain.SeverityWarning, "sample_unassigned", fmt.Sprintf("sample agent %q matches no fleet", sm.Name), sampleSrc, sm.Line, "Sample/"+sm.Name)
			continue
		case match.Error != "":
			v.addAt(domain.SeverityWarning, "fleet_ambiguous", fmt.Sprintf("sample agent %q: %s", sm.Name, match.Error), sampleSrc, sm.Line, "Sample/"+sm.Name)
		}
		for _, name := range match.Candidates {
			var fleet *domain.Fleet
			for i := range v.snap.Fleets {
				if v.snap.Fleets[i].Name == name {
					fleet = &v.snap.Fleets[i]
				}
			}
			if fleet == nil || fleet.Blueprint == "" {
				continue // reported by the static fleet check
			}
			res, err := v.r.Render(RenderInput{
				Snapshot: v.snap, Fleet: fleet, Agent: AgentInput{Attributes: view},
				InventoryParameters: inv.Parameters, Components: opts.Components,
			})
			if err != nil {
				v.err("template_error", fmt.Sprintf("render for sample %q failed: %v", sm.Name, err), fleet.Source, objRef(domain.KindFleet, fleet.Name), "")
				continue
			}
			for _, is := range res.Issues {
				if is.Code == "template_syntax" && v.syntaxObjects[is.Object] {
					continue // the static blueprint check reports it already
				}
				if is.Object == "" {
					is.Object = objRef(domain.KindFleet, fleet.Name)
				}
				if is.File == "" {
					is.File = fleet.Source.Path
				}
				// Identical findings of different samples are reported once;
				// the sample is named in the message of the first.
				key := string(is.Severity) + "|" + is.Code + "|" + is.File + "|" + is.Object + "|" + is.Path + "|" + is.Message
				if v.seen[key] {
					continue
				}
				v.seen[key] = true
				is.Message = fmt.Sprintf("%s (rendering fleet %q for sample %q)", is.Message, fleet.Name, sm.Name)
				v.issues = append(v.issues, is)
			}
		}
	}
}

func (v *validator) addAt(sev domain.IssueSeverity, code, msg string, src domain.SourceRef, line int, object string) {
	v.issues = append(v.issues, domain.Issue{Severity: sev, Code: code, Message: msg, File: src.Path, Line: line, Object: object})
}
