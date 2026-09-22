import { requireNativeModule } from 'expo-modules-core';

export interface PinnedHostInput {
  host: string;
  port: number;
  /** SHA-256 hex of the host certificate's SubjectPublicKeyInfo. */
  spki: string;
}

interface HarnessTlsNativeModule {
  setPinnedHosts(hosts: PinnedHostInput[]): void;
}

const native = requireNativeModule<HarnessTlsNativeModule>('HarnessTls');

/**
 * Install SPKI pins for harness host:port pairs. RN's WebSocket goes through
 * OkHttp, so this must run before the socket opens.
 */
export function setPinnedHosts(hosts: PinnedHostInput[]): void {
  native.setPinnedHosts(hosts);
}
