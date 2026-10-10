// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package channels

import (
	"fmt"
	"strings"

	"github.com/myriarch/myriarch/internal/notify"
)

// render builds the request body for a channel.
func render(c *channel, n notify.Notification) ([]byte, error) {
	switch c.typ {
	case "teams":
		return marshal(teamsCard(n))
	case "slack":
		return marshal(slackMessage(n))
	default:
		return marshal(n)
	}
}

// ------------------------------------------------------------------- Teams ----

// teamsCard is a Teams Workflows / incoming webhook message with an Adaptive
// Card (https://adaptivecards.io, schema version 1.4).
func teamsCard(n notify.Notification) map[string]any {
	color := "Accent"
	switch n.Severity {
	case notify.SeverityCritical:
		color = "Attention"
	case notify.SeverityWarning:
		color = "Warning"
	}
	facts := []map[string]string{
		{"title": "Severity", "value": string(n.Severity)},
		{"title": "Event", "value": string(n.Event)},
	}
	for _, f := range n.Fields {
		facts = append(facts, map[string]string{"title": f.Name, "value": f.Value})
	}
	if !n.Time.IsZero() {
		facts = append(facts, map[string]string{"title": "Time", "value": n.Time.UTC().Format("2006-01-02 15:04:05 UTC")})
	}
	body := []map[string]any{
		{"type": "TextBlock", "text": n.Title, "weight": "Bolder", "size": "Medium", "wrap": true, "color": color},
		{"type": "TextBlock", "text": n.Summary, "wrap": true},
		{"type": "FactSet", "facts": facts},
	}
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"body":    body,
	}
	if n.Link != "" {
		card["actions"] = []map[string]any{{"type": "Action.OpenUrl", "title": "Open in myriarch", "url": n.Link}}
	}
	return map[string]any{
		"type": "message",
		"attachments": []map[string]any{{
			"contentType": "application/vnd.microsoft.card.adaptive",
			"content":     card,
		}},
	}
}

// ------------------------------------------------------------------- Slack ----

// slackMessage is a Block Kit message: header, summary, fields, button.
func slackMessage(n notify.Notification) map[string]any {
	prefix := ""
	switch n.Severity {
	case notify.SeverityCritical:
		prefix = "[CRITICAL] "
	case notify.SeverityWarning:
		prefix = "[WARNING] "
	}
	blocks := []map[string]any{
		{"type": "header", "text": map[string]any{"type": "plain_text", "text": truncate(prefix+n.Title, 150)}},
	}
	if n.Summary != "" {
		blocks = append(blocks, map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": truncate(slackEscape(n.Summary), 3000)}})
	}
	if len(n.Fields) > 0 {
		fields := make([]map[string]any, 0, len(n.Fields))
		for _, f := range n.Fields {
			if len(fields) == 10 { // Slack allows at most 10 fields per section
				break
			}
			fields = append(fields, map[string]any{"type": "mrkdwn", "text": truncate("*"+slackEscape(f.Name)+"*\n"+slackEscape(f.Value), 2000)})
		}
		blocks = append(blocks, map[string]any{"type": "section", "fields": fields})
	}
	if n.Link != "" {
		blocks = append(blocks, map[string]any{"type": "actions", "elements": []map[string]any{{
			"type": "button", "text": map[string]any{"type": "plain_text", "text": "Open in myriarch"}, "url": n.Link,
		}}})
	}
	return map[string]any{"text": prefix + n.Title, "blocks": blocks}
}

// slackEscape escapes the three characters Slack treats specially.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

var _ = fmt.Sprint
