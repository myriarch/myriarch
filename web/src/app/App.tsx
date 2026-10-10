// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { lazy, Suspense } from 'react';
import { Navigate, Route, Routes, useParams } from 'react-router';
import { Layout } from './Layout';
import { FullPageLoader, RequireAuth } from './RequireAuth';

const LoginPage = lazy(() => import('@/pages/login/LoginPage'));
const DashboardPage = lazy(() => import('@/pages/dashboard/DashboardPage'));
const AgentsPage = lazy(() => import('@/pages/agents/AgentsPage'));
const AgentDetailPage = lazy(() => import('@/pages/agents/AgentDetailPage'));
const FleetsPage = lazy(() => import('@/pages/fleets/FleetsPage'));
const FleetDetailPage = lazy(() => import('@/pages/fleets/FleetDetailPage'));
const PolicyDetailPage = lazy(() => import('@/pages/fleets/PolicyDetailPage'));
const BlueprintsPage = lazy(() => import('@/pages/blueprints/BlueprintsPage'));
const BlueprintDetailPage = lazy(() => import('@/pages/blueprints/BlueprintDetailPage'));
const RolloutsPage = lazy(() => import('@/pages/rollouts/RolloutsPage'));
const RolloutDetailPage = lazy(() => import('@/pages/rollouts/RolloutDetailPage'));
const ReleasesPage = lazy(() => import('@/pages/releases/ReleasesPage'));
const ReleaseDetailPage = lazy(() => import('@/pages/releases/ReleaseDetailPage'));
const GitOpsPage = lazy(() => import('@/pages/gitops/GitOpsPage'));
const ArtifactsPage = lazy(() => import('@/pages/artifacts/ArtifactsPage'));
const AuditPage = lazy(() => import('@/pages/audit/AuditPage'));
const SettingsLayout = lazy(() => import('@/pages/settings/SettingsLayout'));
const SystemTab = lazy(() => import('@/pages/settings/SystemTab'));
const NotificationsTab = lazy(() => import('@/pages/settings/NotificationsTab'));
const TokensTab = lazy(() => import('@/pages/settings/TokensTab'));
const UsersTab = lazy(() => import('@/pages/settings/UsersTab'));
const ProfileTab = lazy(() => import('@/pages/settings/ProfileTab'));
const NotFoundPage = lazy(() => import('@/pages/NotFoundPage'));

function PolicyRedirect() {
  const { name } = useParams();
  return <Navigate to={`/policies/${name ?? ''}`} replace />;
}

export function App() {
  return (
    <Suspense fallback={<FullPageLoader />}>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route element={<RequireAuth />}>
          <Route element={<Layout />}>
            <Route index element={<DashboardPage />} />
            <Route path="agents" element={<AgentsPage />} />
            <Route path="agents/:id" element={<AgentDetailPage />} />
            <Route path="fleets" element={<FleetsPage />} />
            <Route path="fleets/:name" element={<FleetDetailPage />} />
            <Route path="policies" element={<Navigate to="/fleets?tab=policies" replace />} />
            <Route path="policies/:name" element={<PolicyDetailPage />} />
            <Route path="rollout-policies/:name" element={<PolicyRedirect />} />
            <Route path="blueprints" element={<BlueprintsPage />} />
            <Route path="blueprints/:name" element={<BlueprintDetailPage />} />
            <Route path="rollouts" element={<RolloutsPage />} />
            <Route path="rollouts/:id" element={<RolloutDetailPage />} />
            <Route path="releases" element={<ReleasesPage />} />
            <Route path="releases/:distribution/:version" element={<ReleaseDetailPage />} />
            <Route path="gitops" element={<GitOpsPage />} />
            <Route path="artifacts" element={<ArtifactsPage />} />
            <Route path="audit" element={<AuditPage />} />
            <Route path="settings" element={<SettingsLayout />}>
              <Route index element={<Navigate to="system" replace />} />
              <Route path="system" element={<SystemTab />} />
              <Route path="notifications" element={<NotificationsTab />} />
              <Route path="tokens" element={<TokensTab />} />
              <Route path="users" element={<UsersTab />} />
              <Route path="profile" element={<ProfileTab />} />
            </Route>
            <Route path="*" element={<NotFoundPage />} />
          </Route>
        </Route>
      </Routes>
    </Suspense>
  );
}
