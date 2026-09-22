import { useEffect } from 'react';
import { getClient, useClientState } from './store';
import { PairScreen } from './components/PairScreen';
import { SessionScreen } from './components/SessionScreen';

export function App() {
  const state = useClientState();
  const client = getClient();
  const needsPairing = state.myDeviceId === null || state.phase === 'unauthorized';

  // On reload with a stored identity, reconnect automatically.
  useEffect(() => {
    const current = client.getState();
    if (current.myDeviceId && current.phase === 'idle') client.connect();
  }, [client, state.myDeviceId]);

  if (needsPairing) {
    return <PairScreen state={state} client={client} />;
  }
  return <SessionScreen state={state} client={client} />;
}
