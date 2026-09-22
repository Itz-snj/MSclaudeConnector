import { useState, type FormEvent } from 'react';
import type { ClientState, HarnessClient } from '@harness/protocol';
import { pairingCode, parsePairingPayload } from '@harness/protocol';
import { clearIdentity, saveIdentity } from '../identity';
import { wsUrl } from '../store';

export function PairScreen({ state, client }: { state: ClientState; client: HarnessClient }) {
  const [url, setUrl] = useState(wsUrl());
  const [token, setToken] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const derivedCode = token.trim() ? pairingCode(token.trim()) : '';

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      let finalUrl = url;
      let finalToken = token.trim();
      let finalSpki = '';
      if (finalToken.startsWith('{')) {
        const info = parsePairingPayload(finalToken);
        finalToken = info.token;
        finalSpki = info.spki;
        if (info.addrs.length > 0) {
          const scheme = info.ws ?? (info.scheme === 'http' ? 'ws' : 'wss');
          finalUrl = `${scheme}://${info.addrs[0]}:${info.port}/ws`;
        }
      }
      const identity = await client.pair({
        token: finalToken,
        url: finalUrl,
        ...(finalSpki ? { spki: finalSpki } : {}),
      });
      saveIdentity(identity);
      client.connect();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const forget = () => {
    clearIdentity();
    client.close();
    client.reset();
    client.setIdentity(null);
  };

  return (
    <form className="pair" onSubmit={submit}>
      <h1>Pair with host</h1>
      {state.phase === 'unauthorized' ? (
        <div className="error-text">
          This device is no longer authorized ({state.error?.code ?? 'unauthorized'}). Re-pair to continue.
        </div>
      ) : (
        <div className="hint">
          Paste the pairing token (or the full JSON blob) from the host screen, then confirm the 4-character
          code matches the host console.
        </div>
      )}

      <div className="field">
        <label htmlFor="ws-url">WebSocket URL</label>
        <input id="ws-url" value={url} onChange={(e) => setUrl(e.target.value)} />
      </div>

      <div className="field">
        <label htmlFor="token">Pairing token or JSON</label>
        <textarea
          id="token"
          value={token}
          rows={2}
          onChange={(e) => setToken(e.target.value)}
          placeholder='e.g. a3f9… or {"addrs":["192.168.1.5"],"port":7432,…}'
        />
      </div>

      {derivedCode ? (
        <div className="field">
          <label>Approval code</label>
          <div className="code">{derivedCode}</div>
        </div>
      ) : null}

      {error ? <div className="error-text">{error}</div> : null}

      <button type="submit" disabled={busy || token.trim() === ''}>
        {busy ? 'Pairing…' : 'Pair'}
      </button>

      {state.myDeviceId ? (
        <button type="button" onClick={forget}>
          Forget this device
        </button>
      ) : null}
    </form>
  );
}
