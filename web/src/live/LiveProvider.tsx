// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { useQueryClient } from '@tanstack/react-query';
import { useEffect, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { notifyInfo } from '@/lib/notify';
import { useAuth } from '@/auth/useAuth';
import { createLiveApplier } from './apply';
import { LiveConnection, type EventSourceFactory, type LiveStatus } from './connection';
import { LiveContext } from './context';

export const STREAM_URL = '/api/v1/events/stream';

async function loadFactory(): Promise<EventSourceFactory> {
  if (import.meta.env.VITE_MOCK === '1') {
    const mod = await import('@/mocks/fakeEventSource');
    return mod.createFakeEventSource;
  }
  return (url) => new EventSource(url, { withCredentials: true });
}

/** Connects to the event stream while the user is signed in and feeds the query cache. */
export function LiveProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();
  const { status: authStatus } = useAuth();
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [status, setStatus] = useState<LiveStatus>('closed');
  const authenticated = authStatus === 'authenticated';

  useEffect(() => {
    if (!authenticated) return;
    let disposed = false;
    let conn: LiveConnection | null = null;
    const applier = createLiveApplier(qc, {
      onNewRelease: (r) => {
        // Every stored release raises the event, including the back-filled history of a first
        // poll and supervisor releases: only a new latest collector version deserves a toast.
        if (r.distribution !== 'otelcol-contrib' || !r.isLatest || r.prerelease) return;
        notifyInfo(
          t('live.newRelease', { version: r.version }),
          t('releases.noticeBreaking', { count: r.breakingCount }),
          () => void navigate(`/releases/${r.distribution}/${r.version}`),
        );
      },
    });
    void loadFactory().then((factory) => {
      if (disposed) return;
      conn = new LiveConnection({
        url: STREAM_URL,
        factory,
        onEvent: applier.apply,
        onStatus: setStatus,
        onReconnected: applier.reconnected,
      });
      conn.start();
    });
    return () => {
      disposed = true;
      conn?.stop();
      applier.dispose();
    };
    // t/navigate identity changes must not tear the connection down.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [authenticated, qc]);

  return (
    <LiveContext.Provider value={authenticated ? status : 'closed'}>
      {children}
    </LiveContext.Provider>
  );
}
