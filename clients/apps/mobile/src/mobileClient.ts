import {
  HarnessClient,
  candidateUrls,
  type ClientState,
  type HarnessIdentity,
  type PairingInfo,
  type SocketLike,
} from '@harness/protocol';
import { useSyncExternalStore } from 'react';
import { AppState } from 'react-native';
import { clearIdentity, loadIdentity, loadLastSeq, saveIdentity, saveLastSeq } from './storage';
import { setPinnedHosts } from './tls';

const MAX_ITEMS = 2000;
const CANDIDATE_TIMEOUT_MS = 3000;
const LAST_SEQ_DEBOUNCE_MS = 2000;

let client: HarnessClient | null = null;

function createSocket(url: string, headers: Record<string, string>): SocketLike {
  const WS = WebSocket as unknown as new (
    u: string,
    protocols?: unknown,
    options?: unknown,
  ) => SocketLike;
  return new WS(url, undefined, { headers });
}

export async function bootstrap(): Promise<HarnessClient> {
  if (client) return client;

  const identity = await loadIdentity();
  const lastSeq = await loadLastSeq();
  if (identity?.spki) {
    pinFor(identity.url, identity.spki);
  }

  const instance = new HarnessClient({
    url: identity?.url ?? 'wss://localhost:7432/ws',
    authMode: 'header',
    identity,
    maxItems: MAX_ITEMS,
    createSocket,
  });
  instance.setLastSeq(lastSeq);

  // Persist lastSeq, debounced ~2s.
  let timer: ReturnType<typeof setTimeout> | null = null;
  let lastSaved = lastSeq;
  instance.subscribe(() => {
    const seq = instance.getState().session.lastSeq;
    if (seq <= lastSaved) return;
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => {
      lastSaved = seq;
      void saveLastSeq(seq);
    }, LAST_SEQ_DEBOUNCE_MS);
  });

  AppState.addEventListener('change', (status) => {
    if (status === 'active' && instance.getState().phase !== 'live') {
      instance.connect();
    }
  });

  client = instance;
  if (identity) instance.connect();
  return instance;
}

export function getMobileClient(): HarnessClient {
  if (!client) throw new Error('mobile client not bootstrapped');
  return client;
}

export function useMobileState(): ClientState {
  const instance = getMobileClient();
  return useSyncExternalStore(instance.subscribe, instance.getState, instance.getState);
}

/** Pair against each candidate host in order, 3s per candidate. */
export async function pairWithInfo(info: PairingInfo): Promise<HarnessIdentity> {
  const instance = getMobileClient();
  const urls = candidateUrls(info);
  let lastError: unknown = null;

  for (const url of urls) {
    try {
      pinFor(url, info.spki);
      const identity = await withTimeout(
        instance.pair({ token: info.token, url, spki: info.spki }),
        CANDIDATE_TIMEOUT_MS,
      );
      await saveIdentity(identity);
      return identity;
    } catch (err) {
      lastError = err;
    }
  }
  throw lastError instanceof Error ? lastError : new Error('no reachable host');
}

export async function forgetDevice(): Promise<void> {
  await clearIdentity();
  const instance = getMobileClient();
  instance.close();
  instance.reset();
  instance.setIdentity(null);
}

function pinFor(url: string, spki: string): void {
  try {
    const parsed = new URL(url);
    setPinnedHosts([{ host: parsed.hostname, port: Number(parsed.port || 7432), spki }]);
  } catch {
    /* malformed URL: skip pinning */
  }
}

function withTimeout<T>(promise: Promise<T>, ms: number): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('candidate timed out')), ms);
    promise.then(
      (value) => {
        clearTimeout(timer);
        resolve(value);
      },
      (err) => {
        clearTimeout(timer);
        reject(err);
      },
    );
  });
}
