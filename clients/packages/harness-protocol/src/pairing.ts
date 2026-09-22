import { asArray, asNumber, asString, isRecord } from './types.js';

/** Parsed contents of the host's pairing QR payload. */
export interface PairingInfo {
  addrs: string[];
  port: number;
  token: string;
  /** SHA-256 of the host certificate's SubjectPublicKeyInfo. */
  spki: string;
  /** First 4 uppercase hex chars of the token; shown on both screens. */
  code?: string;
  scheme?: string;
  ws?: string;
}

/** The 4-character approval code derived from a pairing token. */
export function pairingCode(token: string): string {
  return token.slice(0, 4).toUpperCase();
}

/** Parse a QR payload. Accepts the host's JSON blob, or a harness:// URL. */
export function parsePairingPayload(raw: string): PairingInfo {
  const trimmed = raw.trim();
  if (!trimmed) throw new Error('empty pairing payload');

  if (trimmed.startsWith('{')) {
    let parsed: unknown;
    try {
      parsed = JSON.parse(trimmed);
    } catch {
      throw new Error('pairing payload is not valid JSON');
    }
    if (!isRecord(parsed)) throw new Error('pairing payload is not an object');
    const addrs = asArray<unknown>(parsed.addrs).filter((a): a is string => typeof a === 'string');
    const port = asNumber(parsed.port, 0);
    const token = asString(parsed.token);
    const spki = asString(parsed.spki);
    if (!token) throw new Error('pairing payload is missing a token');
    if (!spki) throw new Error('pairing payload is missing an spki pin');
    if (!port) throw new Error('pairing payload is missing a port');
    const info: PairingInfo = { addrs, port, token, spki };
    const code = asString(parsed.code);
    if (code) info.code = code;
    else info.code = pairingCode(token);
    const scheme = asString(parsed.scheme);
    if (scheme) info.scheme = scheme;
    const ws = asString(parsed.ws);
    if (ws) info.ws = ws;
    return info;
  }

  if (trimmed.startsWith('harness://')) {
    const url = new URL(trimmed);
    const addrs = url.hostname ? [url.hostname] : [];
    const port = url.port ? Number(url.port) : 7432;
    const token = url.searchParams.get('token') ?? '';
    const spki = url.searchParams.get('spki') ?? '';
    const scheme = url.searchParams.get('scheme') ?? 'https';
    if (!token || !spki) throw new Error('pairing URL is missing token or spki');
    return {
      addrs,
      port,
      token,
      spki,
      code: pairingCode(token),
      scheme,
      ws: scheme === 'http' ? 'ws' : 'wss',
    };
  }

  throw new Error('unrecognized pairing payload');
}

/** Order candidate hosts: tailnet (CGNAT) first, then RFC1918, then public, then loopback. */
export function rankHost(host: string): number {
  if (isIPv4(host)) {
    const [a, b] = host.split('.').map(Number);
    if (a === 100 && b >= 64 && b <= 127) return 0; // 100.64.0.0/10 (Tailscale)
    if (a === 10) return 1;
    if (a === 172 && b >= 16 && b <= 31) return 1;
    if (a === 192 && b === 168) return 1;
    if (a === 127) return 3;
    return 2;
  }
  if (isIPv6(host)) {
    const lower = host.toLowerCase();
    if (lower === '::1') return 3;
    if (lower.startsWith('fc') || lower.startsWith('fd')) return 1; // ULA
    return 2;
  }
  return 1; // hostnames are as good as RFC1918
}

/** Build an ordered list of websocket URLs to try for a pairing. */
export function candidateUrls(info: PairingInfo, path = '/ws'): string[] {
  const scheme = info.ws ?? (info.scheme === 'http' ? 'ws' : 'wss');
  const hosts = info.addrs.length > 0 ? info.addrs : ['localhost'];
  const ranked = [...hosts].sort((a, b) => rankHost(a) - rankHost(b));
  const seen = new Set<string>();
  const out: string[] = [];
  for (const host of ranked) {
    const literal = isIPv6(host) ? `[${host}]` : host;
    const url = `${scheme}://${literal}:${info.port}${path}`;
    if (seen.has(url)) continue;
    seen.add(url);
    out.push(url);
  }
  return out;
}

function isIPv4(host: string): boolean {
  return /^\d{1,3}(\.\d{1,3}){3}$/.test(host);
}

function isIPv6(host: string): boolean {
  return host.includes(':');
}
