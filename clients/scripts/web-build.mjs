// Builds the shared protocol package and the web app, then atomically swaps the
// Vite output into the Go embed directory. Building into a temp dir first means
// a failed build never leaves a half-written bundle in internal/webui/web.
import { spawnSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, rmSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const clients = resolve(here, '..');
const webApp = join(clients, 'apps', 'web');
const finalDir = resolve(clients, '..', 'internal', 'webui', 'web');
const tmpDir = join(webApp, '.web-build-tmp');
const isWindows = process.platform === 'win32';

function run(command, args, cwd) {
  const result = spawnSync(command, args, {
    cwd,
    stdio: 'inherit',
    shell: isWindows,
    env: process.env,
  });
  if (result.status !== 0) {
    throw new Error(`${command} ${args.join(' ')} failed with code ${result.status}`);
  }
}

run('npm', ['run', 'build', '-w', '@harness/protocol'], clients);
run('npm', ['exec', '--no', '--', 'tsc', '--noEmit'], webApp);

rmSync(tmpDir, { recursive: true, force: true });
run('npm', ['exec', '--no', '--', 'vite', 'build', '--outDir', tmpDir, '--emptyOutDir'], webApp);

if (!existsSync(join(tmpDir, 'index.html'))) {
  throw new Error('vite build did not produce index.html');
}

rmSync(finalDir, { recursive: true, force: true });
mkdirSync(finalDir, { recursive: true });
cpSync(tmpDir, finalDir, { recursive: true });
rmSync(tmpDir, { recursive: true, force: true });

console.log(`web bundle written to ${finalDir}`);
