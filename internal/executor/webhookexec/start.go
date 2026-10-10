// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package webhookexec

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/executor"
)

// Start triggers updates (docs/concepts/executors.md): batches of batchSize
// targets per call. 2xx = accepted; everything else fails all targets of the
// batch.
func (e *exec) Start(ctx context.Context, req executor.UpdateRequest) ([]executor.TargetResult, error) {
	results := newResults(req.Targets)
	if len(req.Targets) == 0 {
		return results, nil
	}
	if issues := e.Validate(req.Spec, req.Definition); issues.HasErrors() {
		failAll(results, issues[0].Message)
		return results, nil
	}
	switch e.typ {
	case domain.ExecutorWebhook:
		e.startWebhook(ctx, req, results)
	case domain.ExecutorAWX:
		e.startAWX(ctx, req, results)
	}
	return results, ctx.Err()
}

// ---------------------------------------------------------------- webhook ----

func (e *exec) startWebhook(ctx context.Context, req executor.UpdateRequest, results []executor.TargetResult) {
	w := req.Definition.Webhook
	secret, err := e.secret(w.SecretEnv, w.SecretFile, "webhook secret", parseURL(w.URL))
	if err != nil {
		failAll(results, err.Error())
		return
	}
	method := strings.ToUpper(w.Method)
	if method == "" {
		method = http.MethodPost
	}
	timeout := e.timeout(w.Timeout)
	offset := 0
	for _, batch := range batches(req.Targets, w.BatchSize) {
		res := results[offset : offset+len(batch)]
		offset += len(batch)

		did := deliveryID("update", req.RolloutID, req.Wave, req.ToVersion, attempts(batch), targetIDs(batch))
		payload := payloadFor("collector.update", req, batch, did)
		code, body, err := e.post(ctx, method, w.URL, payload, secret, w.Headers, timeout, did)
		switch {
		case err != nil:
			failAll(res, "webhook call failed: "+err.Error())
			e.log.Warn("webhook call failed", "rollout_id", req.RolloutID, "targets", len(batch), "error", err)
		case code < 200 || code > 299:
			msg := fmt.Sprintf("webhook returned HTTP %d", code)
			if x := excerpt(body); x != "" {
				msg += ": " + x
			}
			failAll(res, msg)
			e.log.Warn("webhook rejected the update", "rollout_id", req.RolloutID, "status", code, "targets", len(batch))
		default:
			for i := range res {
				res[i].Started = true
				res[i].ExternalRef = did.String()
			}
		}
	}
}

// post sends one signed payload.
func (e *exec) post(ctx context.Context, method, rawURL string, payload executor.WebhookPayload, secret []byte, custom map[string]string, timeout time.Duration, did uuid.UUID) (int, []byte, error) {
	body, err := marshal(payload)
	if err != nil {
		return 0, nil, fmt.Errorf("encode payload: %w", err)
	}
	ts := e.now().Unix()
	hdr := map[string]string{}
	for k, v := range custom {
		hdr[k] = v
	}
	hdr[brand.DeliveryHeader] = did.String()
	hdr[brand.TimestampHeader] = fmt.Sprint(ts)
	hdr[brand.SignatureHeader] = Signature(secret, ts, body)
	return e.doJSON(ctx, e.client, method, rawURL, body, hdr, timeout)
}

// ---------------------------------------------------------------------- awx ----

type awxLaunch struct {
	Limit     string         `json:"limit"`
	ExtraVars map[string]any `json:"extra_vars"`
}

func (e *exec) startAWX(ctx context.Context, req executor.UpdateRequest, results []executor.TargetResult) {
	a := req.Definition.AWX
	token, err := e.secret(a.TokenEnv, a.TokenFile, "AWX token", parseURL(a.URL))
	if err != nil {
		failAll(results, err.Error())
		return
	}
	client, err := e.clientFor(a)
	if err != nil {
		failAll(results, err.Error())
		return
	}
	limitAttr := a.LimitAttribute
	if limitAttr == "" {
		limitAttr = domain.AttrHostName
	}
	launchURL := strings.TrimRight(a.URL, "/") + a.EffectiveAPIPrefix() + fmt.Sprintf("job_templates/%d/launch/", a.JobTemplateID)
	timeout := e.timeout(a.Timeout)
	header := map[string]string{"Authorization": "Bearer " + string(token)}

	// Targets without a limit value cannot be addressed. The value comes from
	// the agent: it must be a plain host name, never a host pattern.
	invalid := func(i int, err error) {
		results[i].Error = fmt.Sprintf("agent attribute %q is not usable for the AWX limit: %v", limitAttr, err)
	}
	type item struct {
		idx  int
		host string
	}
	var items []item
	for i, t := range req.Targets {
		host := t.Attributes[limitAttr]
		if host == "" && limitAttr == domain.AttrHostName {
			host = t.Hostname
		}
		if host == "" {
			results[i].Error = fmt.Sprintf("agent has no attribute %q for the AWX limit", limitAttr)
			continue
		}
		if err := limitHost(host); err != nil {
			invalid(i, err)
			continue
		}
		items = append(items, item{i, host})
	}
	for _, batch := range batches(items, a.BatchSize) {
		hosts := make([]string, len(batch))
		targets := make([]executor.Target, len(batch))
		for k, it := range batch {
			hosts[k] = it.host
			targets[k] = req.Targets[it.idx]
		}
		sort.Strings(hosts)
		did := deliveryID("awx", req.RolloutID, req.Wave, req.ToVersion, attempts(targets), targetIDs(targets))
		payload := payloadFor("collector.update", req, targets, did)

		extra, err := e.extraVars(a, req, hosts)
		if err != nil {
			for _, it := range batch {
				results[it.idx].Error = "extraVars: " + err.Error()
			}
			continue
		}
		extra[brand.AWXExtraVarsKey] = payload
		body, err := marshal(awxLaunch{Limit: strings.Join(hosts, ","), ExtraVars: extra})
		if err != nil {
			for _, it := range batch {
				results[it.idx].Error = "encode AWX request: " + err.Error()
			}
			continue
		}
		code, resp, err := e.doJSON(ctx, client, http.MethodPost, launchURL, body, header, timeout)
		fail := func(msg string) {
			for _, it := range batch {
				results[it.idx].Error = msg
			}
		}
		switch {
		case err != nil:
			fail("AWX launch failed: " + err.Error())
			e.log.Warn("AWX launch failed", "rollout_id", req.RolloutID, "hosts", len(hosts), "error", err)
			continue
		case code < 200 || code > 299:
			msg := fmt.Sprintf("AWX returned HTTP %d", code)
			if x := excerpt(resp); x != "" {
				msg += ": " + x
			}
			fail(msg)
			continue
		}
		var out struct {
			Job           json.Number    `json:"job"`
			IgnoredFields map[string]any `json:"ignored_fields"`
		}
		dec := json.NewDecoder(strings.NewReader(string(resp)))
		dec.UseNumber()
		if err := dec.Decode(&out); err != nil || out.Job.String() == "" || out.Job.String() == "0" {
			fail("AWX response contains no job id: " + excerpt(resp))
			continue
		}
		if _, ignored := out.IgnoredFields["limit"]; ignored {
			fail(fmt.Sprintf("AWX ignored the limit: enable \"prompt on launch\" for limit in job template %d (the job would run on ALL hosts)", a.JobTemplateID))
			e.cancelJob(ctx, client, a, header, timeout, out.Job.String())
			continue
		}
		if _, ignored := out.IgnoredFields["extra_vars"]; ignored {
			fail(fmt.Sprintf("AWX ignored extra_vars: enable \"prompt on launch\" for variables in job template %d", a.JobTemplateID))
			e.cancelJob(ctx, client, a, header, timeout, out.Job.String())
			continue
		}
		for _, it := range batch {
			results[it.idx].Started = true
			results[it.idx].ExternalRef = "awx-job:" + out.Job.String()
		}
	}
}

// extraVars evaluates the executor template context in the configured vars.
func (e *exec) extraVars(a *domain.AWXExecutorSpec, req executor.UpdateRequest, hosts []string) (map[string]any, error) {
	out := map[string]any{}
	if len(a.ExtraVars) == 0 {
		return out, nil
	}
	hostList := make([]any, len(hosts))
	for i, h := range hosts {
		hostList[i] = h
	}
	params := req.Spec.Params
	if params == nil {
		params = map[string]any{}
	}
	data := map[string]any{
		"toVersion": req.ToVersion, "distribution": req.Distribution, "fleet": req.Fleet,
		"rolloutId": req.RolloutID.String(), "wave": req.Wave, "hostnames": hostList, "params": params,
	}
	v, err := e.renderer.EvalValue(a.ExtraVars, data)
	if err != nil {
		return nil, err
	}
	m, _ := v.(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func (e *exec) cancelJob(ctx context.Context, client *http.Client, a *domain.AWXExecutorSpec, header map[string]string, timeout time.Duration, job string) {
	u := strings.TrimRight(a.URL, "/") + a.EffectiveAPIPrefix() + "jobs/" + url.PathEscape(job) + "/cancel/"
	if code, _, err := e.doJSON(ctx, client, http.MethodPost, u, nil, header, timeout); err != nil || code > 299 {
		e.log.Warn("AWX job cancel failed", "job", job, "status", code, "error", err)
	}
}

// ------------------------------------------------------------------- cancel ----

// Cancel withdraws updates (best effort): the webhook receives the same
// payload with event "collector.update.cancel"; AWX jobs are cancelled by id.
func (e *exec) Cancel(ctx context.Context, req executor.CancelRequest) error {
	if len(req.AgentIDs) == 0 || req.Definition == nil {
		return nil
	}
	switch e.typ {
	case domain.ExecutorWebhook:
		return e.cancelWebhook(ctx, req)
	case domain.ExecutorAWX:
		return e.cancelAWX(ctx, req)
	}
	return nil
}

func (e *exec) cancelWebhook(ctx context.Context, req executor.CancelRequest) error {
	w := req.Definition.Webhook
	if w == nil {
		return nil
	}
	if err := e.hostAllowedURL(w.URL); err != nil {
		return err
	}
	secret, err := e.secret(w.SecretEnv, w.SecretFile, "webhook secret", parseURL(w.URL))
	if err != nil {
		return err
	}
	method := strings.ToUpper(w.Method)
	if method == "" {
		method = http.MethodPost
	}
	timeout := e.timeout(w.Timeout)
	var errs []string
	for _, ids := range batches(req.AgentIDs, w.BatchSize) {
		targets := make([]executor.Target, len(ids))
		for i, id := range ids {
			targets[i] = executor.Target{AgentID: id}
		}
		ur := executor.UpdateRequest{RolloutID: req.RolloutID, Spec: req.Spec}
		did := deliveryID("cancel", req.RolloutID, 0, "", nil, ids)
		payload := payloadFor("collector.update.cancel", ur, targets, did)
		code, body, err := e.post(ctx, method, w.URL, payload, secret, w.Headers, timeout, did)
		switch {
		case err != nil:
			errs = append(errs, "cancel webhook call failed: "+err.Error())
		case code < 200 || code > 299:
			errs = append(errs, fmt.Sprintf("cancel webhook returned HTTP %d: %s", code, excerpt(body)))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func parseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	return u
}

func (e *exec) hostAllowedURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid executor URL")
	}
	return e.hostAllowed(u)
}

func (e *exec) cancelAWX(ctx context.Context, req executor.CancelRequest) error {
	a := req.Definition.AWX
	if a == nil {
		return nil
	}
	if err := e.hostAllowedURL(a.URL); err != nil {
		return err
	}
	jobs := map[string]bool{}
	for _, id := range req.AgentIDs {
		if ref, ok := req.Refs[id]; ok && strings.HasPrefix(ref, "awx-job:") {
			jobs[strings.TrimPrefix(ref, "awx-job:")] = true
		}
	}
	if len(jobs) == 0 {
		return nil
	}
	token, err := e.secret(a.TokenEnv, a.TokenFile, "AWX token", parseURL(a.URL))
	if err != nil {
		return err
	}
	client, err := e.clientFor(a)
	if err != nil {
		return err
	}
	header := map[string]string{"Authorization": "Bearer " + string(token)}
	timeout := e.timeout(a.Timeout)
	var errs []string
	list := make([]string, 0, len(jobs))
	for j := range jobs {
		list = append(list, j)
	}
	sort.Strings(list)
	for _, job := range list {
		u := strings.TrimRight(a.URL, "/") + a.EffectiveAPIPrefix() + "jobs/" + url.PathEscape(job) + "/cancel/"
		code, body, err := e.doJSON(ctx, client, http.MethodPost, u, nil, header, timeout)
		switch {
		case err != nil:
			errs = append(errs, fmt.Sprintf("cancel AWX job %s: %v", job, err))
		case code == http.StatusConflict || code == http.StatusNotFound:
			// already finished / gone: nothing to cancel
		case code < 200 || code > 299:
			errs = append(errs, fmt.Sprintf("cancel AWX job %s: HTTP %d: %s", job, code, excerpt(body)))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}
