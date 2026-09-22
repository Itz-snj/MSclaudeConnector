import { HarnessClient, type ClientState } from '@harness/protocol';
import { useSyncExternalStore } from 'react';
import { loadIdentity } from './identity';

// Module-level singleton. In production the app is served same-origin at
// https://<host>:7432/, so the WS URL derives from location.host. In dev the
// Vite proxy forwards the same /ws path to the daemon.
let client: HarnessClient | null = null;

export function wsUrl(): string {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  return `${proto}://${location.host}/ws`;
}

export function getClient(): HarnessClient {
  if (!client) {
    client = new HarnessClient({
      url: wsUrl(),
      authMode: 'hello',
      identity: loadIdentity(),
    });
  }
  return client;
}

export function useClientState(): ClientState {
  const c = getClient();
  return useSyncExternalStore(c.subscribe, c.getState, c.getState);
}
