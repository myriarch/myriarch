// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package coreapi

import (
	"strings"

	"github.com/Masterminds/semver/v3"

	"github.com/myriarch/myriarch/internal/actorname"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/domain"
)

// IsOutdated reports whether version is a parsable semantic version below
// latest (both without "v"). Unparsable versions are never outdated.
func IsOutdated(version, latest string) bool {
	if version == "" || latest == "" {
		return false
	}
	v, err1 := semver.NewVersion(domain.NormalizeVersion(version))
	l, err2 := semver.NewVersion(domain.NormalizeVersion(latest))
	if err1 != nil || err2 != nil {
		return false
	}
	return v.LessThan(l)
}

// AgentSummaryOf converts an agent to its list representation. latest is the
// newest stable release version ("" if unknown).
func AgentSummaryOf(a *domain.Agent, latest string) dto.AgentSummary {
	s := dto.AgentSummary{
		ID: a.ID, Kind: a.Kind, Hostname: a.Hostname, ServiceName: a.ServiceName, Version: a.Version,
		OSType: a.OSType, Arch: a.Arch, Status: a.Connection.Status, LastSeenAt: a.Connection.LastSeenAt,
		FleetName: a.Assignment.FleetName, ConfigSync: a.ConfigSyncState(), TargetVersion: a.Assignment.TargetVersion,
		HasUpdater: a.Updater != nil,
	}
	if a.Kind == domain.AgentKindCollector {
		s.Outdated = IsOutdated(a.Version, latest)
	}
	if a.Health != nil {
		h := a.Health.Healthy
		s.Healthy = &h
		s.HealthStatus = a.Health.Status
	}
	for k, v := range a.NonIdentifyingAttributes {
		if strings.HasPrefix(k, "site.") {
			if s.Site == nil {
				s.Site = map[string]string{}
			}
			s.Site[k] = v
		}
	}
	if len(a.Labels) > 0 {
		s.Labels = make(map[string]string, len(a.Labels))
		for k, v := range a.Labels {
			s.Labels[k] = v
		}
	}
	return s
}

// RolloutSummaryOf converts a rollout (with counts) to its list
// representation; createdBy keeps the stored actor reference.
func RolloutSummaryOf(r *domain.Rollout) dto.RolloutSummary { return dto.NewRolloutSummary(r) }

// RolloutSummaryNamed is RolloutSummaryOf with the creator presented by its
// display name (names may be nil).
func RolloutSummaryNamed(r *domain.Rollout, names *actorname.Resolver) dto.RolloutSummary {
	s := dto.NewRolloutSummary(r)
	if names != nil {
		s.CreatedBy = names.Name(s.CreatedBy)
	}
	return s
}
