// Fails if any shipped (production) workspace dependency is not on the
// allowlist. This is the supply-chain half of the untrusted-rendering policy:
// no markdown renderers, no sanitizers, nothing unvetted in the bundle.
import { readdirSync, readFileSync, existsSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const clients = resolve(dirname(fileURLToPath(import.meta.url)), '..');

const allowed = new Set(
  readFileSync(join(clients, 'allowed-deps.txt'), 'utf8')
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line && !line.startsWith('#')),
);

const packageFiles = [];
for (const group of ['packages', 'apps']) {
  const groupDir = join(clients, group);
  if (!existsSync(groupDir)) continue;
  for (const entry of readdirSync(groupDir, { withFileTypes: true })) {
    if (!entry.isDirectory()) continue;
    const pkg = join(groupDir, entry.name, 'package.json');
    if (existsSync(pkg)) packageFiles.push(pkg);
  }
}

const violations = [];
for (const pkg of packageFiles) {
  const json = JSON.parse(readFileSync(pkg, 'utf8'));
  const deps = Object.keys(json.dependencies ?? {});
  for (const dep of deps) {
    if (!allowed.has(dep)) {
      violations.push(`${json.name}: ${dep}`);
    }
  }
}

if (violations.length > 0) {
  console.error('Disallowed production dependencies found:');
  for (const v of violations) console.error(`  - ${v}`);
  console.error('\nAdd approved packages to clients/allowed-deps.txt or remove them.');
  process.exit(1);
}

console.log(`dependency allowlist OK (${packageFiles.length} packages checked)`);
