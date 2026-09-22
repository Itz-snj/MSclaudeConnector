import { setPinnedHosts as nativeSetPinnedHosts } from '../modules/harness-tls';

export interface PinnedHost {
  host: string;
  port: number;
  /** SHA-256 hex of the host certificate's SubjectPublicKeyInfo. */
  spki: string;
}

/**
 * Register host:port -> SPKI pins with the native Android TLS module. RN's
 * WebSocket goes through OkHttp, so this is the only place the self-signed
 * host certificate can be trusted.
 */
export function setPinnedHosts(hosts: PinnedHost[]): void {
  nativeSetPinnedHosts(hosts);
}
