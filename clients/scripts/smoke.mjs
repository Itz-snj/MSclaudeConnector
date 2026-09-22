// End-to-end smoke: builds the daemon, runs it with the mock agent over
// plaintext loopback, pairs a real @harness/protocol client, sends a prompt,
// and asserts the streamed reply. Run with:
//   node --experimental-websocket scripts/smoke.mjs
import { spawn, spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import net from 'node:net';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { HarnessClient } from '@harness/protocol';

const here = dirname(fileURLToPath(import.meta.url));
const repo = resolve(here, '..', '..');

function getFreePort() {
  return new Promise((res, rej) => {
    const server = net.createServer();
    server.unref();
    server.on('error', rej);
    server.listen(0, '127.0.0.1', () => {
      const { port } = server.address();
      server.close(() => res(port));
    });
  });
}

function waitFor(predicate, timeoutMs, label) {
  const start = Date.now();
  return new Promise((res, rej) => {
    const tick = () => {
      if (predicate()) return res();
      if (Date.now() - start > timeoutMs) return rej(new Error(`timeout waiting for ${label}`));
      setTimeout(tick, 25);
    };
    tick();
  });
}

const workDir = mkdtempSync(join(tmpdir(), 'harness-smoke-'));
const dataDir = join(workDir, 'data');
const binPath = join(workDir, process.platform === 'win32' ? 'harness.exe' : 'harness');
let server;

try {
  console.log('building daemon…');
  const build = spawnSync('go', ['build', '-o', binPath, './cmd/harness'], {
    cwd: repo,
    stdio: 'inherit',
  });
  if (build.status !== 0) throw new Error('go build failed');

  const port = await getFreePort();
  const url = `ws://127.0.0.1:${port}/ws`;

  console.log(`starting daemon on ${url}…`);
  server = spawn(
    binPath,
    ['serve', '--agent', 'mock', '--bind', '127.0.0.1', '--insecure-http', '--port', String(port), '--data', dataDir],
    { cwd: repo, stdio: ['ignore', 'pipe', 'pipe'] },
  );

  let output = '';
  server.stdout.on('data', (chunk) => (output += chunk.toString()));
  server.stderr.on('data', (chunk) => (output += chunk.toString()));

  await waitFor(() => /Token:\s*([0-9a-f]{64})/.test(output), 20_000, 'pairing token');
  const token = output.match(/Token:\s*([0-9a-f]{64})/)[1];
  console.log('got pairing token, approving out-of-band…');

  const approve = spawnSync(binPath, ['pair', '--approve', `${token}:smoke`, '--data', dataDir], {
    cwd: repo,
    stdio: 'inherit',
  });
  if (approve.status !== 0) throw new Error('pair --approve failed');

  const client = new HarnessClient({ url, authMode: 'hello' });
  const identity = await client.pair({ token, url });
  console.log(`paired as ${identity.deviceId} (${identity.name})`);

  await waitFor(() => client.getState().phase === 'live', 10_000, 'live connection');

  const reply = new Promise((res, rej) => {
    const unsubscribe = client.subscribe(() => {
      const items = client.getState().session.items;
      const assistant = items.find((i) => i.kind === 'assistant' && i.text.includes('Ack: smoke'));
      const prompt = items.find((i) => i.kind === 'prompt' && i.text === 'smoke');
      if (assistant && prompt) {
        unsubscribe();
        res();
      }
    });
    setTimeout(() => {
      unsubscribe();
      rej(new Error('timeout waiting for streamed reply'));
    }, 10_000);
  });

  await client.sendPrompt('smoke');
  await reply;

  const state = client.getState();
  console.log('smoke OK:');
  console.log(`  phase=${state.phase} lastSeq=${state.session.lastSeq}`);
  console.log(`  items=${state.session.items.map((i) => i.kind).join(',')}`);
  client.close();
} finally {
  if (server) server.kill('SIGTERM');
  rmSync(workDir, { recursive: true, force: true });
}
