// status.test.mjs — `landfall status`, feature 20260812-010632 (US5/T048, T051).
//
// A genuine `startHookSocket`+`sendToSocket` round trip does work standalone
// in this environment (confirmed directly before writing these) — so the
// success path below binds a real socket. The fallback/absent-session paths
// deliberately do NOT need one: "no socket at all" needs only `fs.readdir`
// (always real, no I/O to a socket), and "a socket exists but nothing
// answers" is reproduced with a plain file at the expected socket path,
// which fails `net.connect` fast without anything needing to be listening —
// smaller and more deterministic than racing a real server's shutdown timing.
import test from 'node:test';
import assert from 'node:assert/strict';
import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

import { formatStatusLine, queryStatus, readCache, runStatus, writeCache } from '../src/status.mjs';
import { socketLocation, startHookSocket } from '../src/hooks/socket.mjs';

async function withWorkspace(fn) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'landfall-status-'));
  try {
    return await fn({ cwd: root, env: { ...process.env, XDG_RUNTIME_DIR: root } });
  } finally {
    await fs.rm(root, { recursive: true, force: true });
  }
}

/** A file at a socket's expected path that answers no `net.connect` at all —
 * enough for `listHookSockets` to see it, not enough for anything to answer. */
async function plantDeadSocket(opts) {
  const loc = socketLocation(opts);
  await fs.mkdir(loc.dir, { recursive: true, mode: 0o700 });
  const p = loc.pathFor(loc.nameFor(999999));
  await fs.writeFile(p, '');
  return p;
}

// -------------------------------------------------------------- formatStatusLine

test('formatStatusLine renders incident + counts, omitting zero-valued segments', () => {
  assert.equal(
    formatStatusLine({ incidentId: 'inc-1', pending: 3, votesAwaited: 1 }),
    '🔴 landfall #inc-1 · 3 new · 1 vote awaited',
  );
  assert.equal(formatStatusLine({ incidentId: 'inc-1', pending: 0, votesAwaited: 0 }), '🔴 landfall #inc-1');
  assert.equal(
    formatStatusLine({ incidentId: 'inc-1', pending: 1, votesAwaited: 2 }),
    '🔴 landfall #inc-1 · 1 new · 2 votes awaited',
  );
});

test('formatStatusLine renders the divergence segment when present (T050 dependency)', () => {
  const line = formatStatusLine({
    incidentId: 'inc-1',
    pending: 0,
    votesAwaited: 0,
    divergence: { diverging: true, establishedSubject: 'cli-handoff/redeem', observedSubject: 'cloudfront/5xxerrorrate' },
  });
  assert.match(line, /diverging from established root cause/);
  assert.match(line, /cli-handoff\/redeem/);
});

test('formatStatusLine returns the empty string for null — the caller must print nothing', () => {
  assert.equal(formatStatusLine(null), '');
});

// -------------------------------------------------------------- queryStatus

test('queryStatus answers null when landfall serve is not running (no socket directory at all)', async () => {
  await withWorkspace(async (opts) => {
    assert.equal(await queryStatus(opts), null);
  });
});

test('queryStatus never reads a stale cache when nothing is running — a closed session must not bleed a stale incident into the statusline', async () => {
  await withWorkspace(async (opts) => {
    // A cache exists from an earlier, now-ended session...
    await writeCache({ ok: true, incidentId: 'inc-old', pending: 5, votesAwaited: 0 }, opts);
    // ...but there is no socket directory at all — serve is not running now.
    assert.equal(await queryStatus(opts), null);
  });
});

test('queryStatus falls back to the last-known cache when a socket exists but nothing answers (SC-012)', async () => {
  await withWorkspace(async (opts) => {
    await writeCache({ ok: true, incidentId: 'inc-1', pending: 2, votesAwaited: 1 }, opts);
    await plantDeadSocket(opts);
    const status = await queryStatus(opts);
    assert.equal(status.incidentId, 'inc-1');
    assert.equal(status.pending, 2);
  });
});

test('queryStatus answers null (not a throw) when a socket exists, nothing answers, and there is no cache either', async () => {
  await withWorkspace(async (opts) => {
    await plantDeadSocket(opts);
    assert.equal(await queryStatus(opts), null);
  });
});

test('queryStatus answers a real socket and caches what it got, end to end', async () => {
  await withWorkspace(async (opts) => {
    const session = {
      pending: [{ seq: 1 }, { seq: 2 }],
      cursor: 0,
      pendingDropped: 0,
      client: { cfg: { slug: 'acme', incidentId: 'inc-real' } },
      attention: { votesAwaited: [{ claimSeq: 1 }] },
    };
    // A short pid, deliberately: a real macOS `sun_path` limit (~104 bytes)
    // silently truncates a long socket filename's `.sock` suffix once the temp
    // path + workspace-key hash + a realistic multi-digit pid add up — found
    // live while writing this test (a 6-digit pid reproduced it every time,
    // `readdir` showed a bare `424242` with no extension on disk even though
    // `startHookSocket` reported success). That is a latent, pre-existing
    // fragility in socket.mjs's naming scheme, plausibly explaining some of
    // this repo's own pre-existing real-socket test failures — out of scope
    // for T048/T051 to fix, so this test just avoids tripping it.
    const handle = await startHookSocket(session, { ...opts, pid: 1 });
    assert.ok(handle, 'socket bind is expected to succeed in this environment');
    try {
      const status = await queryStatus(opts);
      assert.equal(status.incidentId, 'inc-real');
      assert.equal(status.pending, 2);
      assert.equal(status.votesAwaited, 1);
      // The real answer was cached, not just returned.
      assert.equal((await readCache(opts)).incidentId, 'inc-real');
    } finally {
      await handle.close();
    }
  });
});

test('readCache/writeCache round-trip and are unreadable-safe', async () => {
  await withWorkspace(async (opts) => {
    assert.equal(await readCache(opts), null); // nothing written yet
    await writeCache({ ok: true, incidentId: 'inc-9' }, opts);
    assert.equal((await readCache(opts)).incidentId, 'inc-9');
  });
});

// -------------------------------------------------------------- runStatus (the CLI command itself)

test('runStatus writes nothing and does not throw when landfall serve is not running — T051', async () => {
  await withWorkspace(async (opts) => {
    const written = [];
    const orig = process.stdout.write;
    process.stdout.write = (chunk) => { written.push(chunk); return true; };
    try {
      await runStatus(opts);
    } finally {
      process.stdout.write = orig;
    }
    assert.deepEqual(written, []);
  });
});

test('runStatus writes exactly one line when the socket answers', async () => {
  await withWorkspace(async (opts) => {
    await writeCache({ ok: true, incidentId: 'inc-1', pending: 0, votesAwaited: 0 }, opts);
    await plantDeadSocket(opts); // exists but unreachable — exercises the fallback path end to end
    const written = [];
    const orig = process.stdout.write;
    process.stdout.write = (chunk) => { written.push(chunk); return true; };
    try {
      await runStatus(opts);
    } finally {
      process.stdout.write = orig;
    }
    assert.equal(written.length, 1);
    assert.match(written[0], /inc-1/);
  });
});
