import { StyleSheet, Text } from 'react-native';
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

const COLORS: Partial<Record<ClientState['phase'], string>> = {
  live: '#34d399',
  syncing: '#fbbf24',
  reconnecting: '#fbbf24',
  unauthorized: '#f87171',
  error: '#f87171',
};

export function ConnectionBadge({ state }: { state: ClientState }) {
  let label = LABELS[state.phase];
  if (state.phase === 'syncing' && state.syncing) {
    label = `syncing ${state.syncing.applied}/${state.syncing.total}`;
  } else if (state.phase === 'reconnecting' && state.retryInMs !== null) {
    label = `reconnecting in ${Math.ceil(state.retryInMs / 1000)}s`;
  }
  const color = COLORS[state.phase] ?? '#94a3b8';
  return <Text style={[styles.badge, { color, borderColor: color }]}>{label}</Text>;
}

const styles = StyleSheet.create({
  badge: {
    fontSize: 12,
    paddingHorizontal: 8,
    paddingVertical: 2,
    borderRadius: 999,
    borderWidth: 1,
  },
});
