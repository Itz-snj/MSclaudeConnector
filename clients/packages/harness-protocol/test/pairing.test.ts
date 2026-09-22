import { describe, expect, it } from 'vitest';
import { candidateUrls, pairingCode, parsePairingPayload } from '../src/pairing.js';

describe('pairing payload', () => {
  it('parses the host JSON blob and derives the approval code', () => {
    const info = parsePairingPayload(
      JSON.stringify({
        addrs: ['192.168.1.20'],
        port: 7432,
        token: 'abcdef012345',
        spki: 'deadbeef',
        scheme: 'https',
        ws: 'wss',
      }),
    );
    expect(info.token).toBe('abcdef012345');
    expect(info.spki).toBe('deadbeef');
    expect(info.code).toBe('ABCD');
    expect(info.ws).toBe('wss');
  });

  it('rejects payloads missing required fields', () => {
    expect(() => parsePairingPayload('{}')).toThrow();
    expect(() => parsePairingPayload(JSON.stringify({ port: 1, token: 't' }))).toThrow();
    expect(() => parsePairingPayload('not-a-payload')).toThrow();
  });

  it('parses a harness:// URL', () => {
    const info = parsePairingPayload('harness://192.168.1.9:7432?token=tok1234&spki=aa&scheme=http');
    expect(info.addrs).toEqual(['192.168.1.9']);
    expect(info.port).toBe(7432);
    expect(info.ws).toBe('ws');
  });

  it('derives the code as the first 4 uppercase hex chars', () => {
    expect(pairingCode('a3f9c0d1e2')).toBe('A3F9');
  });
});

describe('candidateUrls', () => {
  it('orders tailnet first, then RFC1918, then loopback', () => {
    const urls = candidateUrls({
      addrs: ['127.0.0.1', '192.168.1.5', '100.101.102.103'],
      port: 7432,
      token: 't',
      spki: 's',
      ws: 'wss',
    });
    expect(urls).toEqual([
      'wss://100.101.102.103:7432/ws',
      'wss://192.168.1.5:7432/ws',
      'wss://127.0.0.1:7432/ws',
    ]);
  });

  it('wraps IPv6 literals in brackets and dedupes', () => {
    const urls = candidateUrls({
      addrs: ['::1', '::1'],
      port: 7432,
      token: 't',
      spki: 's',
      scheme: 'http',
    });
    expect(urls).toEqual(['ws://[::1]:7432/ws']);
  });
});
