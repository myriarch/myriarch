// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

/** Builds `/agents?...` links; array values become repeated keys (same shape as the API query). */
export function agentsLink(
  params: Record<string, string | string[] | boolean | undefined>,
): string {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined) continue;
    if (Array.isArray(v)) v.forEach((x) => sp.append(k, x));
    else sp.set(k, String(v));
  }
  const qs = sp.toString();
  return qs ? `/agents?${qs}` : '/agents';
}

export const releaseLink = (distribution: string, version: string) =>
  `/releases/${encodeURIComponent(distribution)}/${encodeURIComponent(version)}`;
export const fleetLink = (name: string) => `/fleets/${encodeURIComponent(name)}`;
export const policyLink = (name: string) => `/policies/${encodeURIComponent(name)}`;
export const blueprintLink = (name: string) => `/blueprints/${encodeURIComponent(name)}`;
export const rolloutLink = (id: string) => `/rollouts/${encodeURIComponent(id)}`;
export const agentLink = (id: string) => `/agents/${encodeURIComponent(id)}`;
