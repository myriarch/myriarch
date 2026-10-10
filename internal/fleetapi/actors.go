// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package fleetapi

import (
	"context"

	"github.com/myriarch/myriarch/internal/actorname"
	"github.com/myriarch/myriarch/internal/domain"
)

// actorNames resolves the stored actor references ("user:<uuid>",
// "token:<uuid>", "system") of one response into display names. Stored
// references stay stable identifiers (audit, authorization); the API shows who
// acted. Unknown references are returned as they are.
type actorNames = actorname.Resolver

func (s *service) actors(ctx context.Context) *actorNames {
	return actorname.New(ctx, s.d.Store.Identity())
}

// presentRollout replaces the stored creator and approver references by
// display names and fills the origin.
func (s *service) presentRollout(ctx context.Context, ro *domain.Rollout) *domain.Rollout {
	cp := *ro // the store may hand out shared state: never rewrite it in place
	cp.Waves = append([]domain.RolloutWave(nil), ro.Waves...)
	out := normalizeRollout(&cp)
	names := s.actors(ctx)
	if out.Origin == "" {
		out.Origin = domain.OriginOf(out.CreatedBy)
	}
	out.CreatedBy = names.Name(out.CreatedBy)
	for i := range out.Waves {
		out.Waves[i].ApprovedBy = names.Name(out.Waves[i].ApprovedBy)
	}
	// the selection annotation stores the stable reference of the operator
	if sel, ok := out.Annotations[domain.SelectionAnnotation].(map[string]any); ok {
		if by, _ := sel["selectedBy"].(string); by != "" {
			selCopy := make(map[string]any, len(sel))
			for k, v := range sel {
				selCopy[k] = v
			}
			selCopy["selectedBy"] = names.Name(by)
			annotations := make(map[string]any, len(out.Annotations))
			for k, v := range out.Annotations {
				annotations[k] = v
			}
			annotations[domain.SelectionAnnotation] = selCopy
			out.Annotations = annotations
		}
	}
	return out
}

// presentRelease replaces the stored acknowledging reference by a display name.
func (s *service) presentRelease(ctx context.Context, rel domain.Release) domain.Release {
	rel = normalizeRelease(rel)
	rel.AcknowledgedBy = s.actors(ctx).Name(rel.AcknowledgedBy)
	return rel
}
