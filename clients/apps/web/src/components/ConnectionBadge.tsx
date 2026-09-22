import type { ClientState } from '@harness/protocol';

const LABELS: Record<ClientState['phase'], string> = {
  idle: 'idle',
  connecting: 'connecting',
  pairing: 'pairing',
  syncing: 'syncing',
  live: 'live',
  reconnecting: 'reconnecting',
  unauthorized: 'unauthorized',
  error: 'error',
};

export function ConnectionBadge({ state }: { state: ClientState }) {
  const { phase, syncing, retryInMs } = state;
  let label = LABELS[phase];
  if (phase === 'syncing' && syncing) {
    label = `syncing ${syncing.applied}/${syncing.total}`;
  } else if (phase === 'reconnecting' && retryInMs !== null) {
    label = `reconnecting in ${Math.ceil(retryInMs / 1000)}s`;
  }
  return <span className={`badge ${phase}`}>{label}</span>;
}
