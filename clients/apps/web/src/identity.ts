import type { HarnessIdentity } from '@harness/protocol';

// XSS on this origin grants a remote shell; mitigated by CSP + no-HTML + the
// dependency allowlist. A cookie upgrade is deferred to Phase 3.
const KEY = 'harness.identity';

export function loadIdentity(): HarnessIdentity | null {
  try {
    const raw = localStorage.getItem(KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as HarnessIdentity;
    if (!parsed || typeof parsed.credential !== 'string' || typeof parsed.url !== 'string') {
      return null;
    }
    return parsed;
  } catch {
    return null;
  }
}

export function saveIdentity(identity: HarnessIdentity): void {
  localStorage.setItem(KEY, JSON.stringify(identity));
}

export function clearIdentity(): void {
  localStorage.removeItem(KEY);
}
