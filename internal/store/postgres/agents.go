// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/store"
)

type agentStore struct{ db *DB }

var _ store.AgentStore = (*agentStore)(nil)

// agentSelect hydrates complete agents including the updater link in both
// directions: an updater row carries its own link (linked_agent_id); for a
// collector the updater row pointing at it is looked up (most recently seen
// wins, index agents_linked_idx).
const agentSelect = `
SELECT a.id, a.kind, a.service_name, a.service_version, a.hostname, a.os_type, a.os_description, a.arch,
  a.identifying_attributes, a.non_identifying_attributes, a.labels, a.capabilities,
  a.status, a.connected_replica, a.transport, a.remote_addr, a.connected_at, a.disconnected_at, a.last_seen_at,
  a.healthy, a.health_status, a.health_last_error, a.health_start_time, a.component_health, a.health_updated_at,
  a.remote_config_status, a.remote_config_hash, a.remote_config_error, a.remote_config_updated_at,
  a.effective_config_hash, a.effective_config_updated_at, a.available_components_hash, a.package_statuses,
  a.fleet_name, a.assigned_config_hash, a.desired_config_hash, a.failed_config_hash,
  a.target_version, a.assignment_error, a.assigned_at,
  a.enrollment_token_id, a.first_seen_at, a.updated_at,
  COALESCE(a.linked_agent_id, ul.id) AS link_id,
  COALESCE(CASE WHEN a.linked_agent_id IS NOT NULL THEN lc.status = 'online' ELSE ul.status = 'online' END, false) AS link_online,
  COALESCE(ul.online_count > 1, false) AS link_ambiguous,
  COALESCE(a.client_cert_subject, '') AS client_cert_subject
FROM agents a
LEFT JOIN agents lc ON lc.id = a.linked_agent_id
LEFT JOIN LATERAL (
  -- The updater of a collector is an updater that claims it AND holds the same
  -- credential (enrollment token) and, if both known, the same host name: an
  -- agent cannot attach itself to a foreign collector. Online updaters win;
  -- several online ones are reported as ambiguous.
  SELECT (array_agg(u.id ORDER BY (u.status = 'online') DESC, u.last_seen_at DESC NULLS LAST, u.id))[1] AS id,
         (array_agg(u.status ORDER BY (u.status = 'online') DESC, u.last_seen_at DESC NULLS LAST, u.id))[1] AS status,
         count(*) FILTER (WHERE u.status = 'online') AS online_count
  FROM agents u
  WHERE u.linked_agent_id = a.id
    AND u.enrollment_token_id IS NOT DISTINCT FROM a.enrollment_token_id
    AND (u.enrollment_token_id IS NOT NULL OR u.client_cert_subject IS NOT DISTINCT FROM a.client_cert_subject)
    AND (u.hostname = '' OR a.hostname = '' OR lower(u.hostname) = lower(a.hostname))
  HAVING count(*) > 0
) ul ON a.linked_agent_id IS NULL
`

type rowScanner interface{ Scan(dest ...any) error }

func scanAgent(r rowScanner) (*domain.Agent, error) {
	var (
		a                           domain.Agent
		kind, status                string
		idAttrs, nonIDAttrs, labels []byte
		caps                        int64
		replica                     *string
		connAt, discAt, seenAt      *time.Time
		healthy                     *bool
		healthStatus, healthErr     string
		healthStart                 *time.Time
		compHealth                  []byte
		healthUpdated               *time.Time
		rcStatus                    string
		rcHash                      []byte
		rcErr                       string
		rcUpdated                   *time.Time
		effHash                     []byte
		effUpdated                  *time.Time
		compsHash                   []byte
		pkgStatuses                 []byte
		fleet                       *string
		assigned, desired, failed   []byte
		targetVersion, assignErr    string
		assignedAt                  *time.Time
		enrollID                    *uuid.UUID
		linkID                      *uuid.UUID
		linkOnline                  bool
		linkAmbiguous               bool
		certSubject                 string
	)
	err := r.Scan(&a.ID, &kind, &a.ServiceName, &a.Version, &a.Hostname, &a.OSType, &a.OSDescription, &a.Arch,
		&idAttrs, &nonIDAttrs, &labels, &caps,
		&status, &replica, &a.Connection.Transport, &a.Connection.RemoteAddr, &connAt, &discAt, &seenAt,
		&healthy, &healthStatus, &healthErr, &healthStart, &compHealth, &healthUpdated,
		&rcStatus, &rcHash, &rcErr, &rcUpdated,
		&effHash, &effUpdated, &compsHash, &pkgStatuses,
		&fleet, &assigned, &desired, &failed,
		&targetVersion, &assignErr, &assignedAt,
		&enrollID, &a.FirstSeenAt, &a.UpdatedAt,
		&linkID, &linkOnline, &linkAmbiguous, &certSubject)
	if err != nil {
		return nil, err
	}
	a.Kind = domain.AgentKind(kind)
	a.Capabilities = uint64(caps)
	a.IdentifyingAttributes, a.NonIdentifyingAttributes, a.Labels = map[string]string{}, map[string]string{}, map[string]string{}
	if err := decodeJSON(idAttrs, &a.IdentifyingAttributes); err != nil {
		return nil, fmt.Errorf("decode identifying_attributes: %w", err)
	}
	if err := decodeJSON(nonIDAttrs, &a.NonIdentifyingAttributes); err != nil {
		return nil, fmt.Errorf("decode non_identifying_attributes: %w", err)
	}
	if err := decodeJSON(labels, &a.Labels); err != nil {
		return nil, fmt.Errorf("decode labels: %w", err)
	}
	a.IdentifyingAttributes, a.NonIdentifyingAttributes, a.Labels =
		stringMap(a.IdentifyingAttributes), stringMap(a.NonIdentifyingAttributes), stringMap(a.Labels)

	a.Connection.Status = domain.ConnectionStatus(status)
	if replica != nil {
		a.Connection.ReplicaID = *replica
	}
	a.Connection.ConnectedAt, a.Connection.DisconnectedAt, a.Connection.LastSeenAt = utcPtr(connAt), utcPtr(discAt), utcPtr(seenAt)

	if healthy != nil || healthUpdated != nil {
		h := &domain.AgentHealth{Status: healthStatus, LastError: healthErr, StartTime: utcPtr(healthStart)}
		if healthy != nil {
			h.Healthy = *healthy
		}
		if healthUpdated != nil {
			h.UpdatedAt = healthUpdated.UTC()
		}
		if len(compHealth) > 0 {
			if err := decodeJSON(compHealth, &h.Components); err != nil {
				return nil, fmt.Errorf("decode component_health: %w", err)
			}
		}
		a.Health = h
	}

	a.RemoteConfig = domain.RemoteConfigState{
		Status: domain.RemoteConfigStatus(rcStatus), LastHash: domain.Hash(rcHash), ErrorMessage: rcErr, UpdatedAt: utcPtr(rcUpdated),
	}
	a.EffectiveConfigHash, a.EffectiveConfigUpdatedAt = domain.Hash(effHash), utcPtr(effUpdated)
	a.AvailableComponentsHash = domain.Hash(compsHash)
	if len(pkgStatuses) > 0 {
		var ps domain.PackageStatuses
		if err := decodeJSON(pkgStatuses, &ps); err != nil {
			return nil, fmt.Errorf("decode package_statuses: %w", err)
		}
		if ps.Packages == nil {
			ps.Packages = map[string]*domain.PackageStatus{}
		}
		a.PackageStatus = &ps
	}

	a.Assignment = domain.AgentAssignment{
		AssignedConfigHash: domain.Hash(assigned), DesiredConfigHash: domain.Hash(desired), FailedConfigHash: domain.Hash(failed),
		TargetVersion: targetVersion, Error: assignErr, AssignedAt: utcPtr(assignedAt),
	}
	if fleet != nil {
		a.Assignment.FleetName = *fleet
	}
	a.EnrollmentTokenID, a.ClientCertSubject = enrollID, certSubject
	a.FirstSeenAt, a.UpdatedAt = a.FirstSeenAt.UTC(), a.UpdatedAt.UTC()
	if linkID != nil {
		a.Updater = &domain.UpdaterLink{AgentID: *linkID, Online: linkOnline, Ambiguous: linkAmbiguous}
	}
	return &a, nil
}

// hydrate loads full agents by id; the result follows the order of ids.
func (s *agentStore) hydrate(ctx context.Context, ids []uuid.UUID) ([]domain.Agent, error) {
	out := make([]domain.Agent, 0, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.q.Query(ctx, agentSelect+` WHERE a.id = ANY($1)`, ids)
	if err != nil {
		return nil, dbErr("agents.hydrate", err)
	}
	defer rows.Close()
	byID := make(map[uuid.UUID]domain.Agent, len(ids))
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, dbErr("agents.hydrate scan", err)
		}
		byID[a.ID] = *a
	}
	if err := rows.Err(); err != nil {
		return nil, dbErr("agents.hydrate", err)
	}
	for _, id := range ids {
		if a, ok := byID[id]; ok {
			out = append(out, a)
			delete(byID, id) // tolerate duplicate ids in the input
		}
	}
	return out, nil
}

// Get implements store.AgentStore.
func (s *agentStore) Get(ctx context.Context, id domain.AgentID) (*domain.Agent, error) {
	a, err := scanAgent(s.db.q.QueryRow(ctx, agentSelect+` WHERE a.id = $1`, id))
	if err != nil {
		return nil, dbErr("agents.get", err)
	}
	return a, nil
}

// GetMany implements store.AgentStore.
func (s *agentStore) GetMany(ctx context.Context, ids []domain.AgentID) ([]domain.Agent, error) {
	return s.hydrate(ctx, ids)
}

// ------------------------------------------------------------ filtering ---

func buildAgentWhere(f store.AgentFilter) (string, []any, error) {
	var (
		args    argList
		clauses []string
	)
	if len(f.IDs) > 0 {
		clauses = append(clauses, "a.id = ANY("+args.add(f.IDs)+")")
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		p := args.add("%" + likeEscape(q) + "%")
		clauses = append(clauses, fmt.Sprintf(`(a.hostname ILIKE %[1]s OR a.id::text ILIKE %[1]s OR a.service_version ILIKE %[1]s
 OR EXISTS (SELECT 1 FROM jsonb_each_text(a.identifying_attributes) e WHERE e.value ILIKE %[1]s)
 OR EXISTS (SELECT 1 FROM jsonb_each_text(a.non_identifying_attributes) e WHERE e.value ILIKE %[1]s))`, p))
	}
	if len(f.Kinds) > 0 {
		clauses = append(clauses, "a.kind = ANY("+args.add(kindStrings(f.Kinds))+")")
	}
	if len(f.Status) > 0 {
		vals := make([]string, len(f.Status))
		for i, s := range f.Status {
			vals[i] = string(s)
		}
		clauses = append(clauses, "a.status = ANY("+args.add(vals)+")")
	}
	if f.Healthy != nil {
		clauses = append(clauses, "a.healthy = "+args.add(*f.Healthy))
	}
	if len(f.Fleets) > 0 {
		clauses = append(clauses, "a.fleet_name = ANY("+args.add(f.Fleets)+")")
	}
	if f.Unassigned != nil {
		if *f.Unassigned {
			clauses = append(clauses, "a.fleet_name IS NULL")
		} else {
			clauses = append(clauses, "a.fleet_name IS NOT NULL")
		}
	}
	if len(f.Versions) > 0 {
		clauses = append(clauses, "a.service_version = ANY("+args.add(f.Versions)+")")
	}
	if f.OutdatedBelow != "" {
		v, err := semver.NewVersion(domain.NormalizeVersion(f.OutdatedBelow))
		if err != nil {
			return "", nil, fmt.Errorf("%w: invalid outdatedBelow version %q", domain.ErrInvalidArgument, f.OutdatedBelow)
		}
		clauses = append(clauses, fmt.Sprintf("a.kind = 'collector' AND a.version_major IS NOT NULL AND (a.version_major, a.version_minor, a.version_patch) < (%s::int, %s::int, %s::int)",
			args.add(int32(v.Major())), args.add(int32(v.Minor())), args.add(int32(v.Patch()))))
	}
	if f.UpToDateFrom != "" {
		v, err := semver.NewVersion(domain.NormalizeVersion(f.UpToDateFrom))
		if err != nil {
			return "", nil, fmt.Errorf("%w: invalid upToDateFrom version %q", domain.ErrInvalidArgument, f.UpToDateFrom)
		}
		clauses = append(clauses, fmt.Sprintf("a.kind = 'collector' AND a.version_major IS NOT NULL AND (a.version_major, a.version_minor, a.version_patch) >= (%s::int, %s::int, %s::int)",
			args.add(int32(v.Major())), args.add(int32(v.Minor())), args.add(int32(v.Patch()))))
	}
	if len(f.OSTypes) > 0 {
		clauses = append(clauses, "a.os_type = ANY("+args.add(f.OSTypes)+")")
	}
	keys := make([]string, 0, len(f.Attributes))
	for k := range f.Attributes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		vals := f.Attributes[k]
		if len(vals) == 0 {
			continue
		}
		vp := args.add(vals)
		if strings.HasPrefix(k, domain.LabelPrefix) {
			kp := args.add(strings.TrimPrefix(k, domain.LabelPrefix))
			clauses = append(clauses, fmt.Sprintf("a.labels ->> %s::text = ANY(%s::text[])", kp, vp))
			continue
		}
		kp := args.add(k)
		clauses = append(clauses, fmt.Sprintf("(a.identifying_attributes ->> %[1]s::text = ANY(%[2]s::text[]) OR a.non_identifying_attributes ->> %[1]s::text = ANY(%[2]s::text[]))", kp, vp))
	}
	if len(f.ConfigSync) > 0 {
		vals := make([]string, len(f.ConfigSync))
		for i, s := range f.ConfigSync {
			vals[i] = string(s)
		}
		clauses = append(clauses, "a.config_sync = ANY("+args.add(vals)+")")
	}
	if len(f.RemoteConfigStatus) > 0 {
		vals := make([]string, len(f.RemoteConfigStatus))
		for i, s := range f.RemoteConfigStatus {
			vals[i] = string(s)
		}
		clauses = append(clauses, "a.remote_config_status = ANY("+args.add(vals)+")")
	}
	if f.LastSeenBefore != nil {
		clauses = append(clauses, "a.last_seen_at < "+args.add(f.LastSeenBefore.UTC()))
	}
	if len(clauses) == 0 {
		return "", nil, nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args.args, nil
}

func kindStrings(ks []domain.AgentKind) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = string(k)
	}
	return out
}

// agentOrder translates AgentQuery.Sort into an ORDER BY list. The id is the
// final tie breaker so pagination is stable.
func agentOrder(sortSpec string) (string, error) {
	field, desc := sortSpec, false
	if strings.HasPrefix(field, "-") {
		field, desc = strings.TrimPrefix(field, "-"), true
	}
	if field == "" {
		field = "hostname"
	}
	var cols []string
	switch field {
	case "hostname":
		cols = []string{"lower(a.hostname)", "a.hostname"}
	case "version":
		cols = []string{"a.version_major", "a.version_minor", "a.version_patch", "a.service_version"}
	case "status":
		cols = []string{"a.status"}
	case "fleet":
		cols = []string{"a.fleet_name"}
	case "lastSeenAt":
		cols = []string{"a.last_seen_at"}
	case "firstSeenAt":
		cols = []string{"a.first_seen_at"}
	case "health":
		cols = []string{"a.healthy"}
	case "configSync":
		cols = []string{"a.config_sync"}
	case "osType":
		cols = []string{"a.os_type"}
	case "siteId":
		cols = []string{"a.non_identifying_attributes ->> 'site.id'"}
	default:
		return "", fmt.Errorf("%w: unsupported sort field %q", domain.ErrInvalidArgument, field)
	}
	dir := " ASC NULLS LAST"
	if desc {
		dir = " DESC NULLS LAST"
	}
	parts := make([]string, 0, len(cols)+1)
	for _, c := range cols {
		parts = append(parts, c+dir)
	}
	parts = append(parts, "a.id ASC")
	return strings.Join(parts, ", "), nil
}

// List implements store.AgentStore: one query selects the page ids (plus the
// total via a window function), a second hydrates exactly those rows.
func (s *agentStore) List(ctx context.Context, q store.AgentQuery) (store.AgentPage, error) {
	where, args, err := buildAgentWhere(q.Filter)
	if err != nil {
		return store.AgentPage{}, err
	}
	order, err := agentOrder(q.Sort)
	if err != nil {
		return store.AgentPage{}, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	al := argList{args: args}
	sql := "SELECT a.id, count(*) OVER () FROM agents a" + where + " ORDER BY " + order +
		" LIMIT " + al.add(limit) + " OFFSET " + al.add(offset)
	rows, err := s.db.q.Query(ctx, sql, al.args...)
	if err != nil {
		return store.AgentPage{}, dbErr("agents.list", err)
	}
	ids := make([]uuid.UUID, 0, limit)
	total := 0
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id, &total); err != nil {
			rows.Close()
			return store.AgentPage{}, dbErr("agents.list scan", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return store.AgentPage{}, dbErr("agents.list", err)
	}
	if len(ids) == 0 && offset > 0 {
		// offset beyond the last row: the window function produced no row.
		if err := s.db.q.QueryRow(ctx, "SELECT count(*) FROM agents a"+where, args...).Scan(&total); err != nil {
			return store.AgentPage{}, dbErr("agents.list count", err)
		}
	}
	items, err := s.hydrate(ctx, ids)
	if err != nil {
		return store.AgentPage{}, err
	}
	return store.AgentPage{Items: items, Total: total}, nil
}

// ListAll implements store.AgentStore.
func (s *agentStore) ListAll(ctx context.Context, f store.AgentFilter) ([]domain.Agent, error) {
	where, args, err := buildAgentWhere(f)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.q.Query(ctx, agentSelect+where+" ORDER BY lower(a.hostname), a.id", args...)
	if err != nil {
		return nil, dbErr("agents.listAll", err)
	}
	defer rows.Close()
	out := []domain.Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, dbErr("agents.listAll scan", err)
		}
		out = append(out, *a)
	}
	if err := rows.Err(); err != nil {
		return nil, dbErr("agents.listAll", err)
	}
	return out, nil
}

// facetSpec describes one facet: the filter dimensions it ignores (so that
// the other values of the same dimension stay visible and selectable, i.e.
// disjunctive facets) and the SQL for its value.
type facetSpec struct {
	name  string
	clear func(f *store.AgentFilter)
	value string // SQL expression over the base columns
	where string // optional SQL condition over the base columns
}

var agentFacetSpecs = []facetSpec{
	{"status", func(f *store.AgentFilter) { f.Status = nil }, "status", ""},
	{"health", func(f *store.AgentFilter) { f.Healthy = nil }, "health", ""},
	{"kinds", func(f *store.AgentFilter) { f.Kinds = nil }, "kind", ""},
	{"versions", func(f *store.AgentFilter) { f.Versions, f.OutdatedBelow, f.UpToDateFrom = nil, "", "" }, "version", "version <> ''"},
	{"fleets", func(f *store.AgentFilter) { f.Fleets, f.Unassigned = nil, nil }, "fleet", ""},
	{"osTypes", func(f *store.AgentFilter) { f.OSTypes = nil }, "os_type", "os_type <> ''"},
	{"siteTypes", func(f *store.AgentFilter) { f.Attributes = withoutAttr(f.Attributes, "site.type") }, "site_type", "site_type <> ''"},
	{"countries", func(f *store.AgentFilter) { f.Attributes = withoutAttr(f.Attributes, "site.country") }, "country", "country <> ''"},
	{"configSync", func(f *store.AgentFilter) { f.ConfigSync = nil }, "config_sync", ""},
}

func withoutAttr(m map[string][]string, key string) map[string][]string {
	if _, ok := m[key]; !ok {
		return m
	}
	out := make(map[string][]string, len(m))
	for k, v := range m {
		if k != key {
			out[k] = v
		}
	}
	return out
}

const facetBaseSQL = `SELECT a.status AS status,
         CASE WHEN a.healthy IS NULL THEN 'unknown' WHEN a.healthy THEN 'healthy' ELSE 'unhealthy' END AS health,
         a.kind AS kind, a.service_version AS version, COALESCE(a.fleet_name, '') AS fleet, a.os_type AS os_type,
         COALESCE(a.non_identifying_attributes ->> 'site.type', '') AS site_type,
         COALESCE(a.non_identifying_attributes ->> 'site.country', '') AS country,
         a.config_sync AS config_sync
  FROM agents a`

// Facets implements store.AgentFacets. Facet counts are disjunctive: the
// filter dimension of a facet itself is not applied to its own counts (so a
// multi-select filter panel keeps showing the alternatives), all other
// filters are. Total always counts the rows matching the complete filter.
// Facets whose dimension is not filtered share one scan of the filtered rows.
func (s *agentStore) Facets(ctx context.Context, f store.AgentFilter) (store.AgentFacets, error) {
	out := store.AgentFacets{
		Status: []store.FacetValue{}, Health: []store.FacetValue{}, Kinds: []store.FacetValue{}, Versions: []store.FacetValue{},
		Fleets: []store.FacetValue{}, OSTypes: []store.FacetValue{}, SiteTypes: []store.FacetValue{}, Countries: []store.FacetValue{},
		ConfigSync: []store.FacetValue{},
	}
	target := map[string]*[]store.FacetValue{
		"status": &out.Status, "health": &out.Health, "kinds": &out.Kinds, "versions": &out.Versions, "fleets": &out.Fleets,
		"osTypes": &out.OSTypes, "siteTypes": &out.SiteTypes, "countries": &out.Countries, "configSync": &out.ConfigSync,
	}

	// Shared scan: total plus every facet that ignores nothing of the filter.
	var shared []facetSpec
	var own []facetSpec
	for _, sp := range agentFacetSpecs {
		g := f
		sp.clear(&g)
		if reflect.DeepEqual(g, f) {
			shared = append(shared, sp)
		} else {
			own = append(own, sp)
		}
	}
	where, args, err := buildAgentWhere(f)
	if err != nil {
		return store.AgentFacets{}, err
	}
	sql := "WITH base AS MATERIALIZED (" + facetBaseSQL + where + ")\nSELECT 'total', '', count(*) FROM base"
	for _, sp := range shared {
		sql += "\nUNION ALL SELECT '" + sp.name + "', " + sp.value + ", count(*) FROM base" + facetWhere(sp) + " GROUP BY " + sp.value
	}
	if err := s.scanFacets(ctx, sql, args, &out, target); err != nil {
		return store.AgentFacets{}, err
	}
	for _, sp := range own {
		g := f
		sp.clear(&g)
		w, a, err := buildAgentWhere(g)
		if err != nil {
			return store.AgentFacets{}, err
		}
		q := "WITH base AS MATERIALIZED (" + facetBaseSQL + w + ")\nSELECT '" + sp.name + "', " + sp.value + ", count(*) FROM base" + facetWhere(sp) + " GROUP BY " + sp.value
		if err := s.scanFacets(ctx, q, a, &out, target); err != nil {
			return store.AgentFacets{}, err
		}
	}
	for _, l := range [][]store.FacetValue{out.Status, out.Health, out.Kinds, out.Versions, out.Fleets, out.OSTypes, out.SiteTypes, out.Countries, out.ConfigSync} {
		sortFacet(l)
	}
	return out, nil
}

func facetWhere(sp facetSpec) string {
	if sp.where == "" {
		return ""
	}
	return " WHERE " + sp.where
}

func (s *agentStore) scanFacets(ctx context.Context, sql string, args []any, out *store.AgentFacets, target map[string]*[]store.FacetValue) error {
	rows, err := s.db.q.Query(ctx, sql, args...)
	if err != nil {
		return dbErr("agents.facets", err)
	}
	defer rows.Close()
	for rows.Next() {
		var facet, value string
		var n int
		if err := rows.Scan(&facet, &value, &n); err != nil {
			return dbErr("agents.facets scan", err)
		}
		if facet == "total" {
			out.Total = n
			continue
		}
		if l := target[facet]; l != nil {
			*l = append(*l, store.FacetValue{Value: value, Count: n})
		}
	}
	if err := rows.Err(); err != nil {
		return dbErr("agents.facets", err)
	}
	return nil
}

func sortFacet(l []store.FacetValue) {
	sort.Slice(l, func(i, j int) bool {
		if l[i].Count != l[j].Count {
			return l[i].Count > l[j].Count
		}
		return l[i].Value < l[j].Value
	})
}

// ---------------------------------------------------------------- writes ---

func rowsAffectedOrNotFound(op string, tag interface{ RowsAffected() int64 }) error {
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%s: %w", op, domain.ErrNotFound)
	}
	return nil
}

func parseSemver(v string) (major, minor, patch any) {
	sv, err := semver.NewVersion(domain.NormalizeVersion(v))
	if err != nil {
		return nil, nil, nil
	}
	return int32(sv.Major()), int32(sv.Minor()), int32(sv.Patch())
}

// UpsertDescription implements store.AgentStore.
func (s *agentStore) UpsertDescription(ctx context.Context, id domain.AgentID, u store.AgentDescriptionUpdate) (*domain.Agent, bool, error) {
	idAttrs, err := jsonArg(stringMap(u.IdentifyingAttributes), "{}")
	if err != nil {
		return nil, false, err
	}
	nonIDAttrs, err := jsonArg(stringMap(u.NonIdentifyingAttributes), "{}")
	if err != nil {
		return nil, false, err
	}
	labels, err := jsonArg(stringMap(u.InitialLabels), "{}")
	if err != nil {
		return nil, false, err
	}
	kind := u.Kind
	if kind == "" {
		kind = domain.AgentKindUnknown
	}
	major, minor, patch := parseSemver(u.Version)
	var created bool
	err = s.db.q.QueryRow(ctx, `
INSERT INTO agents (id, kind, service_name, service_version, version_major, version_minor, version_patch,
    hostname, os_type, os_description, arch, identifying_attributes, non_identifying_attributes, labels,
    capabilities, enrollment_token_id, client_cert_subject)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
ON CONFLICT (id) DO UPDATE SET
    kind = EXCLUDED.kind, service_name = EXCLUDED.service_name, service_version = EXCLUDED.service_version,
    version_major = EXCLUDED.version_major, version_minor = EXCLUDED.version_minor, version_patch = EXCLUDED.version_patch,
    hostname = EXCLUDED.hostname, os_type = EXCLUDED.os_type, os_description = EXCLUDED.os_description, arch = EXCLUDED.arch,
    identifying_attributes = EXCLUDED.identifying_attributes, non_identifying_attributes = EXCLUDED.non_identifying_attributes,
    capabilities = EXCLUDED.capabilities,
    -- the credential binding is set once (first registration); changing it is
    -- an explicit action (SetCredentialBinding)
    enrollment_token_id = COALESCE(agents.enrollment_token_id, EXCLUDED.enrollment_token_id),
    client_cert_subject = COALESCE(agents.client_cert_subject, EXCLUDED.client_cert_subject),
    updated_at = now()
RETURNING (xmax = 0)`,
		id, string(kind), u.ServiceName, u.Version, major, minor, patch,
		u.Hostname, u.OSType, u.OSDescription, u.Arch, idAttrs, nonIDAttrs, labels,
		int64(u.Capabilities), uuidPtrArg(u.EnrollmentTokenID), textArg(u.ClientCertSubject)).Scan(&created)
	if err != nil {
		return nil, false, dbErr("agents.upsertDescription", err)
	}
	a, err := s.Get(ctx, id)
	if err != nil {
		return nil, false, err
	}
	return a, created, nil
}

// SetConnection implements store.AgentStore.
//
// An OFFLINE write that carries a ReplicaID is conditional: it only applies if the row is not owned by a
// different replica (connected_replica IS NULL or equal). This prevents a
// replica that closes a stale connection from marking an agent offline that
// has meanwhile reconnected to another replica.
func (s *agentStore) SetConnection(ctx context.Context, id domain.AgentID, c domain.AgentConnection) error {
	status := string(c.Status)
	if status == "" {
		status = string(domain.ConnectionOffline)
	}
	conditional := c.Status == domain.ConnectionOffline && c.ReplicaID != ""
	replicaArg := textArg(c.ReplicaID)
	if conditional {
		replicaArg = nil // row ends up without replica
	}
	sql := `UPDATE agents SET status = $2, connected_replica = $3, transport = $4, remote_addr = $5,
    connected_at = COALESCE($6, connected_at), disconnected_at = COALESCE($7, disconnected_at),
    last_seen_at = COALESCE($8, last_seen_at), updated_at = now()
WHERE id = $1`
	args := []any{id, status, replicaArg, c.Transport, c.RemoteAddr, timePtrArg(c.ConnectedAt), timePtrArg(c.DisconnectedAt), timePtrArg(c.LastSeenAt)}
	if conditional {
		sql += ` AND (connected_replica IS NULL OR connected_replica = $9)`
		args = append(args, c.ReplicaID)
	}
	tag, err := s.db.q.Exec(ctx, sql, args...)
	if err != nil {
		return dbErr("agents.setConnection", err)
	}
	if tag.RowsAffected() == 0 {
		if !conditional {
			return fmt.Errorf("agents.setConnection: %w", domain.ErrNotFound)
		}
		var exists bool
		if err := s.db.q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM agents WHERE id = $1)`, id).Scan(&exists); err != nil {
			return dbErr("agents.setConnection", err)
		}
		if !exists {
			return fmt.Errorf("agents.setConnection: %w", domain.ErrNotFound)
		}
	}
	return nil
}

// TouchLastSeen implements store.AgentStore (one statement for the whole batch).
func (s *agentStore) TouchLastSeen(ctx context.Context, ids []domain.AgentID, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.db.q.Exec(ctx, `UPDATE agents a SET last_seen_at = $2
FROM unnest($1::uuid[]) AS t(id)
WHERE a.id = t.id AND (a.last_seen_at IS NULL OR a.last_seen_at < $2)`, ids, at.UTC())
	return dbErr("agents.touchLastSeen", err)
}

// SetHealth implements store.AgentStore.
func (s *agentStore) SetHealth(ctx context.Context, id domain.AgentID, h *domain.AgentHealth) error {
	var (
		healthy         any
		status, lastErr string
		start, updated  any
		components      any
	)
	if h != nil {
		healthy, status, lastErr = h.Healthy, cleanText(h.Status), cleanText(h.LastError)
		start = timePtrArg(h.StartTime)
		upd := h.UpdatedAt
		if upd.IsZero() {
			upd = time.Now()
		}
		updated = upd.UTC()
		if len(h.Components) > 0 {
			b, err := jsonArg(h.Components, "")
			if err != nil {
				return err
			}
			components = b
		}
	}
	tag, err := s.db.q.Exec(ctx, `UPDATE agents SET healthy = $2, health_status = $3, health_last_error = $4,
    health_start_time = $5, component_health = $6, health_updated_at = $7, updated_at = now() WHERE id = $1`,
		id, healthy, status, lastErr, start, components, updated)
	if err != nil {
		return dbErr("agents.setHealth", err)
	}
	return rowsAffectedOrNotFound("agents.setHealth", tag)
}

// SetRemoteConfigState implements store.AgentStore.
func (s *agentStore) SetRemoteConfigState(ctx context.Context, id domain.AgentID, st domain.RemoteConfigState) error {
	status := string(st.Status)
	if status == "" {
		status = string(domain.RemoteConfigUnset)
	}
	tag, err := s.db.q.Exec(ctx, `UPDATE agents SET remote_config_status = $2, remote_config_hash = $3, remote_config_error = $4,
    remote_config_updated_at = COALESCE($5, now()), updated_at = now() WHERE id = $1`,
		id, status, hashArg(st.LastHash), cleanText(st.ErrorMessage), timePtrArg(st.UpdatedAt))
	if err != nil {
		return dbErr("agents.setRemoteConfigState", err)
	}
	return rowsAffectedOrNotFound("agents.setRemoteConfigState", tag)
}

// SetEffectiveConfig implements store.AgentStore.
func (s *agentStore) SetEffectiveConfig(ctx context.Context, id domain.AgentID, hash domain.Hash, at time.Time) error {
	if at.IsZero() {
		at = time.Now()
	}
	tag, err := s.db.q.Exec(ctx, `UPDATE agents SET effective_config_hash = $2, effective_config_updated_at = $3, updated_at = now() WHERE id = $1`,
		id, hashArg(hash), at.UTC())
	if err != nil {
		return dbErr("agents.setEffectiveConfig", err)
	}
	return rowsAffectedOrNotFound("agents.setEffectiveConfig", tag)
}

// SetAvailableComponentsHash implements store.AgentStore.
func (s *agentStore) SetAvailableComponentsHash(ctx context.Context, id domain.AgentID, hash domain.Hash) error {
	tag, err := s.db.q.Exec(ctx, `UPDATE agents SET available_components_hash = $2, updated_at = now() WHERE id = $1`, id, hashArg(hash))
	if err != nil {
		return dbErr("agents.setAvailableComponentsHash", err)
	}
	return rowsAffectedOrNotFound("agents.setAvailableComponentsHash", tag)
}

// SetPackageStatuses implements store.AgentStore.
func (s *agentStore) SetPackageStatuses(ctx context.Context, id domain.AgentID, ps *domain.PackageStatuses) error {
	var arg any
	if ps != nil {
		cp := *ps
		if cp.Packages == nil {
			cp.Packages = map[string]*domain.PackageStatus{}
		}
		if cp.UpdatedAt.IsZero() {
			cp.UpdatedAt = time.Now().UTC()
		}
		b, err := jsonArg(cp, "")
		if err != nil {
			return err
		}
		arg = b
	}
	tag, err := s.db.q.Exec(ctx, `UPDATE agents SET package_statuses = $2, updated_at = now() WHERE id = $1`, id, arg)
	if err != nil {
		return dbErr("agents.setPackageStatuses", err)
	}
	return rowsAffectedOrNotFound("agents.setPackageStatuses", tag)
}

// SetUpdaterLink implements store.AgentStore.
func (s *agentStore) SetUpdaterLink(ctx context.Context, updaterID, collectorID domain.AgentID) error {
	tag, err := s.db.q.Exec(ctx, `UPDATE agents SET linked_agent_id = $2, updated_at = now() WHERE id = $1`, updaterID, uuidArg(collectorID))
	if err != nil {
		return dbErr("agents.setUpdaterLink", err)
	}
	return rowsAffectedOrNotFound("agents.setUpdaterLink", tag)
}

// SetCredentialBinding implements store.AgentStore.
func (s *agentStore) SetCredentialBinding(ctx context.Context, id domain.AgentID, tokenID *uuid.UUID, certSubject string) error {
	tag, err := s.db.q.Exec(ctx, `UPDATE agents SET enrollment_token_id = $2, client_cert_subject = $3, updated_at = now() WHERE id = $1`,
		id, uuidPtrArg(tokenID), textArg(certSubject))
	if err != nil {
		return dbErr("agents.setCredentialBinding", err)
	}
	return rowsAffectedOrNotFound("agents.setCredentialBinding", tag)
}

// SetLabels implements store.AgentStore.
func (s *agentStore) SetLabels(ctx context.Context, id domain.AgentID, labels map[string]string) error {
	arg, err := jsonArg(stringMap(labels), "{}")
	if err != nil {
		return err
	}
	tag, err := s.db.q.Exec(ctx, `UPDATE agents SET labels = $2, updated_at = now() WHERE id = $1`, id, arg)
	if err != nil {
		return dbErr("agents.setLabels", err)
	}
	return rowsAffectedOrNotFound("agents.setLabels", tag)
}

func collectIDs(rows pgx.Rows) ([]domain.AgentID, error) {
	defer rows.Close()
	out := []domain.AgentID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// MarkReplicaOffline implements store.AgentStore.
func (s *agentStore) MarkReplicaOffline(ctx context.Context, replicaID string, at time.Time) ([]domain.AgentID, error) {
	rows, err := s.db.q.Query(ctx, `UPDATE agents SET status = 'offline', connected_replica = NULL, disconnected_at = $2, updated_at = now()
WHERE connected_replica = $1 RETURNING id`, replicaID, at.UTC())
	if err != nil {
		return nil, dbErr("agents.markReplicaOffline", err)
	}
	ids, err := collectIDs(rows)
	return ids, dbErr("agents.markReplicaOffline", err)
}

// MarkStaleOffline implements store.AgentStore.
func (s *agentStore) MarkStaleOffline(ctx context.Context, before time.Time) ([]domain.AgentID, error) {
	rows, err := s.db.q.Query(ctx, `UPDATE agents SET status = 'offline', connected_replica = NULL, disconnected_at = now(), updated_at = now()
WHERE status = 'online' AND COALESCE(last_seen_at, connected_at, updated_at) < $1 RETURNING id`, before.UTC())
	if err != nil {
		return nil, dbErr("agents.markStaleOffline", err)
	}
	ids, err := collectIDs(rows)
	return ids, dbErr("agents.markStaleOffline", err)
}

// Delete implements store.AgentStore.
func (s *agentStore) Delete(ctx context.Context, id domain.AgentID) error {
	tag, err := s.db.q.Exec(ctx, `DELETE FROM agents WHERE id = $1`, id)
	if err != nil {
		return dbErr("agents.delete", err)
	}
	return rowsAffectedOrNotFound("agents.delete", tag)
}

// DeleteOfflineBefore implements store.AgentStore.
func (s *agentStore) DeleteOfflineBefore(ctx context.Context, before time.Time) (int64, error) {
	n, err := s.db.execMaintenance(ctx, `DELETE FROM agents WHERE status = 'offline' AND COALESCE(disconnected_at, last_seen_at, updated_at) < $1`, before.UTC())
	if err != nil {
		return 0, dbErr("agents.deleteOfflineBefore", err)
	}
	return n, nil
}

// SetAssignment implements store.AgentStore.
func (s *agentStore) SetAssignment(ctx context.Context, id domain.AgentID, u store.AgentAssignmentUpdate) error {
	assignedAt := u.AssignedAt
	if assignedAt.IsZero() {
		assignedAt = time.Now()
	}
	tag, err := s.db.q.Exec(ctx, `UPDATE agents SET fleet_name = $2, assigned_config_hash = $3, target_version = $4,
    assignment_error = $5, assigned_at = $6, updated_at = now() WHERE id = $1`,
		id, textArg(u.FleetName), hashArg(u.AssignedConfigHash), u.TargetVersion, cleanText(u.Error), assignedAt.UTC())
	if err != nil {
		return dbErr("agents.setAssignment", err)
	}
	return rowsAffectedOrNotFound("agents.setAssignment", tag)
}

// SetFleetTargetVersion implements store.AgentStore.
func (s *agentStore) SetFleetTargetVersion(ctx context.Context, fleet, version string) (int64, error) {
	tag, err := s.db.q.Exec(ctx, `UPDATE agents SET target_version = $2, updated_at = now()
WHERE fleet_name = $1 AND kind = 'collector' AND target_version IS DISTINCT FROM $2`, fleet, version)
	if err != nil {
		return 0, dbErr("agents.setFleetTargetVersion", err)
	}
	return tag.RowsAffected(), nil
}

// SetDesiredConfig implements store.AgentStore.
func (s *agentStore) SetDesiredConfig(ctx context.Context, ids []domain.AgentID, hash domain.Hash) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.db.q.Exec(ctx, `UPDATE agents SET
    failed_config_hash = CASE WHEN desired_config_hash IS DISTINCT FROM $2 THEN NULL ELSE failed_config_hash END,
    desired_config_hash = $2, updated_at = now()
WHERE id = ANY($1)`, ids, hashArg(hash))
	return dbErr("agents.setDesiredConfig", err)
}

// SetDesiredConfigIfUnset implements store.AgentStore.
func (s *agentStore) SetDesiredConfigIfUnset(ctx context.Context, ids []domain.AgentID, hash domain.Hash) ([]domain.AgentID, error) {
	if len(ids) == 0 {
		return []domain.AgentID{}, nil
	}
	rows, err := s.db.q.Query(ctx, `UPDATE agents SET desired_config_hash = $2, updated_at = now()
WHERE id = ANY($1) AND desired_config_hash IS NULL RETURNING id`, ids, hashArg(hash))
	if err != nil {
		return nil, dbErr("agents.setDesiredConfigIfUnset", err)
	}
	out, err := collectIDs(rows)
	return out, dbErr("agents.setDesiredConfigIfUnset", err)
}

// SetFailedConfig implements store.AgentStore.
func (s *agentStore) SetFailedConfig(ctx context.Context, id domain.AgentID, hash domain.Hash) error {
	tag, err := s.db.q.Exec(ctx, `UPDATE agents SET failed_config_hash = $2, updated_at = now() WHERE id = $1`, id, hashArg(hash))
	if err != nil {
		return dbErr("agents.setFailedConfig", err)
	}
	return rowsAffectedOrNotFound("agents.setFailedConfig", tag)
}

// ClearFailedConfig implements store.AgentStore.
func (s *agentStore) ClearFailedConfig(ctx context.Context, id domain.AgentID) error {
	tag, err := s.db.q.Exec(ctx, `UPDATE agents SET failed_config_hash = NULL, updated_at = now() WHERE id = $1`, id)
	if err != nil {
		return dbErr("agents.clearFailedConfig", err)
	}
	return rowsAffectedOrNotFound("agents.clearFailedConfig", tag)
}

// AppendEvents implements store.AgentStore (one statement for the batch;
// events of agents that no longer exist are skipped).
func (s *agentStore) AppendEvents(ctx context.Context, events []domain.AgentEvent) error {
	if len(events) == 0 {
		return nil
	}
	n := len(events)
	ids, times := make([]uuid.UUID, n), make([]time.Time, n)
	types, sevs, msgs, details := make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	now := time.Now().UTC()
	for i, e := range events {
		ids[i] = e.AgentID
		times[i] = e.Time.UTC()
		if e.Time.IsZero() {
			times[i] = now
		}
		types[i] = string(e.Type)
		sevs[i] = e.Severity
		switch sevs[i] {
		case "info", "warning", "error":
		default:
			sevs[i] = "info"
		}
		msgs[i] = cleanText(e.Message)
		if len(e.Details) > 0 {
			b, err := jsonArg(e.Details, "")
			if err != nil {
				return err
			}
			details[i] = string(b.([]byte))
		}
	}
	_, err := s.db.q.Exec(ctx, `INSERT INTO agent_events (agent_id, time, type, severity, message, details)
SELECT t.agent_id, t.time, t.type, t.severity, t.message, NULLIF(t.details, '')::jsonb
FROM unnest($1::uuid[], $2::timestamptz[], $3::text[], $4::text[], $5::text[], $6::text[])
     WITH ORDINALITY AS t(agent_id, time, type, severity, message, details, ord)
JOIN agents a ON a.id = t.agent_id
ORDER BY t.ord`, ids, times, types, sevs, msgs, details)
	return dbErr("agents.appendEvents", err)
}

// ListEvents implements store.AgentStore.
func (s *agentStore) ListEvents(ctx context.Context, id domain.AgentID, limit int, beforeID int64) ([]domain.AgentEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.db.q.Query(ctx, `SELECT id, agent_id, time, type, severity, message, details FROM agent_events
WHERE agent_id = $1 AND ($3::bigint <= 0 OR id < $3) ORDER BY id DESC LIMIT $2`, id, limit, beforeID)
	if err != nil {
		return nil, dbErr("agents.listEvents", err)
	}
	defer rows.Close()
	out := []domain.AgentEvent{}
	for rows.Next() {
		var e domain.AgentEvent
		var typ string
		var details []byte
		if err := rows.Scan(&e.ID, &e.AgentID, &e.Time, &typ, &e.Severity, &e.Message, &details); err != nil {
			return nil, dbErr("agents.listEvents scan", err)
		}
		e.Type, e.Time = domain.AgentEventType(typ), e.Time.UTC()
		if len(details) > 0 {
			if err := decodeJSON(details, &e.Details); err != nil {
				return nil, fmt.Errorf("decode event details: %w", err)
			}
		}
		out = append(out, e)
	}
	return out, dbErr("agents.listEvents", rows.Err())
}

// DeleteEventsBefore implements store.AgentStore.
func (s *agentStore) DeleteEventsBefore(ctx context.Context, before time.Time) (int64, error) {
	n, err := s.db.execMaintenance(ctx, `DELETE FROM agent_events WHERE time < $1`, before.UTC())
	if err != nil {
		return 0, dbErr("agents.deleteEventsBefore", err)
	}
	return n, nil
}

// cleanText makes free text from agents safe for PostgreSQL text columns
// (no NUL bytes, valid UTF-8).
func cleanText(s string) string {
	if s == "" {
		return s
	}
	s = strings.ToValidUTF8(s, "�")
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	return s
}
