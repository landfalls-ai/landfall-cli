// stop-hook.test.mjs — the `stop` hook and the local query socket it asks (#225).
//
// Three layers, because they fail in different ways:
//   1. the protocol      — handleSocketRequest against a real session object
//   2. the decision      — buildStopDecision / the loop guard, pure
//   3. the wire + CLI    — a real socket, and the real binary spawned against it
import test from 'node:test';
import assert from 'node:assert/strict';
import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

import { createBridgeSession, consumeUpTo } from '../../src/tools.mjs';
import {
  handleSocketRequest,
  listHookSockets,
  queryHookSockets,
  sendToSocket,
  socketLocation,
  startHookSocket,
  workspaceKey,
} from '../../src/hooks/socket.mjs';
import { buildStopDecision, isStopHookActive, runStopHook, HOOK_OUTPUT_MAX } from '../../src/hooks/stop.mjs';
import { runHookEvent } from '../../src/hooks/run.mjs';
import { runCli } from '../install/helpers.mjs';

/** A session with `n` queued room events, as `watchIncident` would have parked them. */
function sessionWith(n, { from = 1 } = {}) {
  const session = createBridgeSession({ client: { cfg: { slug: 'acme', incidentId: 'inc-1' } } });
  for (let i = 0; i < n; i += 1) {
    session.enqueueEvent({
      seq: from + i,
      type: 'edge.finding',
      payload: { displayName: 'Ana', text: `origin 5xx spiking on shard ${i}` },
    });
  }
  return session;
}

/** A temp runtime dir, so a test never binds into the developer's real one. */
async function withRuntimeDir(fn) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'landfall-hook-socket-'));
  try {
    return await fn({ env: { ...process.env, XDG_RUNTIME_DIR: root }, root });
  } finally {
    await fs.rm(root, { recursive: true, force: true });
  }
}

// ---------------------------------------------------------------- 1. protocol

test('peek reports what the session owes, and is non-destructive', () => {
  const session = sessionWith(3);
  const first = handleSocketRequest({ op: 'peek' }, session, { pid: 1 });

  assert.equal(first.ok, true);
  assert.equal(first.count, 3);
  assert.equal(first.cursor, -1);
  assert.equal(first.maxSeq, 3);
  assert.equal(first.digest.length, 3);
  assert.match(first.digest[0], /^#1 edge\.finding \[Ana\] — origin 5xx/);

  // Asked twice, answered the same: a hook that crashes after peeking must
  // leave the events queued for the next attempt.
  assert.deepEqual(handleSocketRequest({ op: 'peek' }, session, { pid: 1 }), first);
  assert.equal(session.pending.length, 3);
  assert.equal(session.cursor, -1);
});

test('consume advances the cursor and drops what it accounted for', () => {
  const session = sessionWith(3);
  const res = handleSocketRequest({ op: 'consume', upTo: 3 }, session, { pid: 1, consume: consumeUpTo });

  assert.equal(res.ok, true);
  assert.equal(res.cursor, 3);
  assert.equal(session.pending.length, 0);
  assert.equal(handleSocketRequest({ op: 'peek' }, session, { pid: 1 }).count, 0);
});

test('consume never moves the cursor backwards, and leaves newer events queued', () => {
  const session = sessionWith(4);
  consumeUpTo(session, 3);
  assert.equal(session.cursor, 3);
  assert.deepEqual(session.pending.map((e) => e.seq), [4]);

  handleSocketRequest({ op: 'consume', upTo: 1 }, session, { pid: 1, consume: consumeUpTo });
  assert.equal(session.cursor, 3, 'a stale upTo must not rewind the cursor');
  assert.deepEqual(session.pending.map((e) => e.seq), [4]);
});

test('consume without a numeric upTo is refused rather than guessed', () => {
  const session = sessionWith(2);
  const res = handleSocketRequest({ op: 'consume' }, session, { pid: 1, consume: consumeUpTo });
  assert.equal(res.ok, false);
  assert.equal(session.cursor, -1);
  assert.equal(session.pending.length, 2);
});

test('status answers diagnostics without exposing the session token', () => {
  const session = createBridgeSession({
    client: { cfg: { slug: 'acme', incidentId: 'inc-1', token: 'super-secret-token' } },
  });
  const res = handleSocketRequest({ op: 'status' }, session, { pid: 42 });

  assert.equal(res.slug, 'acme');
  assert.equal(res.incidentId, 'inc-1');
  assert.equal(res.connected, true);
  assert.ok(!JSON.stringify(res).includes('super-secret-token'));
});

test('the verb set is the security boundary — nothing writes to the room', () => {
  const session = sessionWith(1);
  // The socket must never become a second way to ACT in a war room. Anything
  // outside the three read/cursor verbs is an error, not a silent no-op.
  for (const op of ['post_finding', 'propose_action', 'contribute', 'join_war_room', 'token', 'get_updates']) {
    const res = handleSocketRequest({ op }, session, { pid: 1, consume: consumeUpTo });
    assert.equal(res.ok, false, `${op} must not be a verb`);
    assert.match(res.error, /unknown op/);
  }
  const malformed = handleSocketRequest(null, session, { pid: 1 });
  assert.equal(malformed.ok, false);
});

// ---------------------------------------------------------------- 2. decision

test('nothing owed → no block, nothing to say', () => {
  assert.deepEqual(buildStopDecision([]), { block: false, reason: '', consumes: [] });
  assert.equal(buildStopDecision([{ socketPath: '/s', response: { ok: true, count: 0, digest: [], cursor: 7 } }]).block, false);
});

test('events owed → block, with the digest and a resume pointer', () => {
  const peek = handleSocketRequest({ op: 'peek' }, sessionWith(2), { pid: 1 });
  const decision = buildStopDecision([{ socketPath: '/s', response: peek }]);

  assert.equal(decision.block, true);
  assert.match(decision.reason, /Do not conclude yet — 2 update\(s\)/);
  assert.match(decision.reason, /#1 edge\.finding \[Ana\]/);
  assert.match(decision.reason, /#2 edge\.finding \[Ana\]/);
  assert.deepEqual(decision.consumes, [{ socketPath: '/s', upTo: 2 }]);
});

test('a busy room is summarized, not replayed — output stays under the cap', () => {
  const peek = handleSocketRequest({ op: 'peek' }, sessionWith(50), { pid: 1 });
  const decision = buildStopDecision([{ socketPath: '/s', response: peek }]);

  assert.equal(decision.block, true);
  assert.ok(decision.reason.length <= HOOK_OUTPUT_MAX, `reason was ${decision.reason.length} chars`);
  assert.match(decision.reason, /earlier update\(s\) not shown — call get_updates with sinceSeq=-1/);
  // Every line the digest omits is still counted in the header total.
  assert.match(decision.reason, /50 update\(s\)/);
});

test('a single enormous event still fits the cap', () => {
  const session = createBridgeSession({ client: { cfg: {} } });
  session.enqueueEvent({ seq: 9, type: 'edge.finding', payload: { text: 'x'.repeat(40_000) } });
  const peek = handleSocketRequest({ op: 'peek' }, session, { pid: 1 });
  const decision = buildStopDecision([{ socketPath: '/s', response: peek }]);
  assert.ok(decision.reason.length <= HOOK_OUTPUT_MAX, `reason was ${decision.reason.length} chars`);
});

test('overflow the session already dropped is still reported', () => {
  const session = sessionWith(60); // PENDING_MAX is 50 — 10 are dropped
  assert.ok(session.pendingDropped > 0);
  const peek = handleSocketRequest({ op: 'peek' }, session, { pid: 1 });
  const decision = buildStopDecision([{ socketPath: '/s', response: peek }]);
  assert.match(decision.reason, new RegExp(`${peek.count + peek.dropped} update\\(s\\)`));
});

test('two local sessions are unioned — over-reporting, never under-reporting', () => {
  const a = handleSocketRequest({ op: 'peek' }, sessionWith(2, { from: 1 }), { pid: 1 });
  const b = handleSocketRequest({ op: 'peek' }, sessionWith(1, { from: 9 }), { pid: 2 });
  const decision = buildStopDecision([
    { socketPath: '/a', response: a },
    { socketPath: '/b', response: b },
  ]);

  assert.match(decision.reason, /3 update\(s\)/);
  assert.deepEqual(decision.consumes, [
    { socketPath: '/a', upTo: 2 },
    { socketPath: '/b', upTo: 9 },
  ]);
});

test('the loop guard reads stop_hook_active in either casing, and defaults to "first attempt"', () => {
  assert.equal(isStopHookActive(JSON.stringify({ stop_hook_active: true })), true);
  assert.equal(isStopHookActive({ stopHookActive: true }), true);
  assert.equal(isStopHookActive(JSON.stringify({ stop_hook_active: false })), false);
  assert.equal(isStopHookActive(''), false);
  assert.equal(isStopHookActive('not json at all'), false);
  assert.equal(isStopHookActive(undefined), false);
});

test('a session can always terminate: the second Stop after a block is allowed through', async () => {
  const peek = handleSocketRequest({ op: 'peek' }, sessionWith(3), { pid: 1 });
  const query = async () => [{ socketPath: '/s', response: peek }];
  let sent = 0;
  const send = async () => { sent += 1; };

  const first = await runStopHook({ input: '{}', query, send, emit: () => {} });
  assert.equal(first.exitCode, 2);
  assert.equal(sent, 1);

  const second = await runStopHook({
    input: JSON.stringify({ stop_hook_active: true }),
    query: () => assert.fail('the loop guard must short-circuit before any socket is touched'),
    send,
    emit: () => assert.fail('a guarded Stop must say nothing'),
  });
  assert.equal(second.exitCode, 0);
});

test('the digest is emitted BEFORE any cursor moves', async () => {
  const order = [];
  const peek = handleSocketRequest({ op: 'peek' }, sessionWith(1), { pid: 1 });
  await runStopHook({
    query: async () => [{ socketPath: '/s', response: peek }],
    send: async (_p, req) => { order.push(`consume:${req.upTo}`); },
    emit: () => order.push('emit'),
  });
  assert.deepEqual(order, ['emit', 'consume:1'], 'a crash mid-way must leave events queued, not consumed');
});

test('a failed consume does not suppress a block that was already emitted', async () => {
  const peek = handleSocketRequest({ op: 'peek' }, sessionWith(1), { pid: 1 });
  let emitted = '';
  const res = await runStopHook({
    query: async () => [{ socketPath: '/s', response: peek }],
    send: async () => { throw new Error('socket vanished'); },
    emit: (t) => { emitted = t; },
  });
  assert.equal(res.exitCode, 2);
  assert.match(emitted, /Do not conclude yet/);
});

test('a hook that cannot ask never becomes a hook that blocks', async () => {
  const res = await runStopHook({
    query: async () => { throw new Error('permission denied'); },
    emit: () => assert.fail('a failed query must stay silent'),
  });
  assert.equal(res.exitCode, 0);
});

test('runHookEvent routes stop to the handler and leaves file-changed alone', async () => {
  const peek = handleSocketRequest({ op: 'peek' }, sessionWith(1), { pid: 1 });
  const deps = { query: async () => [{ socketPath: '/s', response: peek }], send: async () => {}, emit: () => {} };
  assert.equal((await runHookEvent('stop', deps)).exitCode, 2);
  assert.equal((await runHookEvent('file-changed', deps)).exitCode, 0); // #227
  assert.equal((await runHookEvent('nope', deps)).exitCode, 2);
});

// ------------------------------------------------------------ 3. wire + CLI

test('socket path is keyed by workspace, per-user, and never on a network', () => {
  const loc = socketLocation({ cwd: '/tmp', env: { XDG_RUNTIME_DIR: '/run/user/1000' }, platform: 'linux' });
  assert.equal(loc.dir, path.join('/run/user/1000', 'landfall', workspaceKey('/tmp')));
  assert.equal(loc.nameFor(77), '77.sock');

  // macOS sets no XDG_RUNTIME_DIR.
  const mac = socketLocation({ cwd: '/tmp', env: { HOME: '/Users/ana' }, platform: 'darwin' });
  assert.equal(mac.dir, path.join('/Users/ana', '.local', 'state', 'landfall', 'run', workspaceKey('/tmp')));

  const win = socketLocation({ cwd: '/tmp', env: {}, platform: 'win32' });
  assert.match(win.pathFor(win.nameFor(77)), /^\\\\\.\\pipe\\landfall-/);

  // Two workspaces are two namespaces. Two fixed, always-distinct paths — the previous
  // `os.tmpdir() === '/tmp' ? '/usr' : '/tmp'` ternary was trying to avoid comparing against
  // whatever the platform's real tmpdir is, but on macOS (where os.tmpdir() is never '/tmp')
  // it resolved to comparing workspaceKey('/tmp') against itself, making this assertion
  // fail on every Mac. The test's intent never depended on the platform's tmpdir at all.
  assert.notEqual(workspaceKey('/tmp'), workspaceKey('/usr'));
});

test('a real socket answers peek, then consume, over the wire', async () => {
  await withRuntimeDir(async ({ env }) => {
    const session = sessionWith(2);
    const bound = await startHookSocket(session, { env, consume: consumeUpTo, pid: 4242 });
    assert.ok(bound, 'expected the socket to bind');
    try {
      const dir = socketLocation({ env }).dir;
      assert.equal((await fs.stat(dir)).mode & 0o777, 0o700, 'the directory mode IS the access control');
      assert.equal((await fs.stat(bound.socketPath)).mode & 0o777, 0o600);

      const peeks = await queryHookSockets({ op: 'peek' }, { env });
      assert.equal(peeks.length, 1);
      assert.equal(peeks[0].response.count, 2);

      await sendToSocket(bound.socketPath, { op: 'consume', upTo: 2 });
      assert.equal(session.cursor, 2);
      assert.equal((await queryHookSockets({ op: 'peek' }, { env }))[0].response.count, 0);
    } finally {
      await bound.close();
    }
    assert.deepEqual(await listHookSockets({ env }), [], 'close() must not leave a stale node behind');
  });
});

test('two serve processes in one workspace keep two cursors', async () => {
  await withRuntimeDir(async ({ env }) => {
    const a = sessionWith(2, { from: 1 });
    const b = sessionWith(1, { from: 9 });
    const boundA = await startHookSocket(a, { env, consume: consumeUpTo, pid: 111 });
    const boundB = await startHookSocket(b, { env, consume: consumeUpTo, pid: 222 });
    try {
      assert.equal((await listHookSockets({ env })).length, 2);

      const decision = buildStopDecision(await queryHookSockets({ op: 'peek' }, { env }));
      assert.match(decision.reason, /3 update\(s\)/);

      // Consuming one session's queue leaves the other's untouched.
      await sendToSocket(boundA.socketPath, { op: 'consume', upTo: 2 });
      assert.equal(a.pending.length, 0);
      assert.equal(b.pending.length, 1);
    } finally {
      await boundA.close();
      await boundB.close();
    }
  });
});

test('an unreachable socket is skipped, not fatal', async () => {
  await withRuntimeDir(async ({ env }) => {
    const loc = socketLocation({ env });
    await fs.mkdir(loc.dir, { recursive: true, mode: 0o700 });
    await fs.writeFile(path.join(loc.dir, '999.sock'), ''); // a crashed session's leftover
    const session = sessionWith(1);
    const bound = await startHookSocket(session, { env, consume: consumeUpTo, pid: 4243 });
    try {
      const peeks = await queryHookSockets({ op: 'peek' }, { env });
      assert.equal(peeks.length, 1, 'the live socket still answers');
      assert.equal(peeks[0].response.count, 1);
    } finally {
      await bound.close();
    }
  });
});

test('no serve session anywhere → the hook asks nothing and costs nothing', async () => {
  await withRuntimeDir(async ({ env }) => {
    assert.deepEqual(await listHookSockets({ env }), []);
    const started = process.hrtime.bigint();
    const res = await runStopHook({ env, emit: () => assert.fail('nothing owed must say nothing') });
    const ms = Number(process.hrtime.bigint() - started) / 1e6;
    assert.equal(res.exitCode, 0);
    assert.ok(ms < 100, `the empty path took ${ms.toFixed(1)}ms — it must not wait on anything`);
  });
});

test('`landfall hooks stop` blocks the real binary when room context is owed', async () => {
  await withRuntimeDir(async ({ root }) => {
    const env = { ...process.env, XDG_RUNTIME_DIR: root };
    const session = sessionWith(2);
    const bound = await startHookSocket(session, { env, consume: consumeUpTo, pid: 5150 });
    const saved = process.env.XDG_RUNTIME_DIR;
    process.env.XDG_RUNTIME_DIR = root; // runCli spawns with the current env
    try {
      const blocked = await runCli(['hooks', 'stop'], { input: '{}' });
      assert.equal(blocked.exitCode, 2);
      assert.equal(blocked.stdout, '', 'stdout is the host\'s channel — the hook must not write to it');
      assert.match(blocked.stderr, /Do not conclude yet — 2 update\(s\)/);
      assert.match(blocked.stderr, /#1 edge\.finding \[Ana\]/);

      // The block consumed them, so an unchanged room lets the next Stop through.
      assert.equal(session.pending.length, 0);
      const allowed = await runCli(['hooks', 'stop'], { input: '{}' });
      assert.equal(allowed.exitCode, 0);
      assert.equal(allowed.stdout, '');
      assert.equal(allowed.stderr, '');
    } finally {
      if (saved === undefined) delete process.env.XDG_RUNTIME_DIR;
      else process.env.XDG_RUNTIME_DIR = saved;
      await bound.close();
    }
  });
});

test('`landfall hooks stop` with the loop guard set never blocks, even with context owed', async () => {
  await withRuntimeDir(async ({ root }) => {
    const env = { ...process.env, XDG_RUNTIME_DIR: root };
    const session = sessionWith(3);
    const bound = await startHookSocket(session, { env, consume: consumeUpTo, pid: 5151 });
    const saved = process.env.XDG_RUNTIME_DIR;
    process.env.XDG_RUNTIME_DIR = root;
    try {
      const res = await runCli(['hooks', 'stop'], { input: JSON.stringify({ stop_hook_active: true }) });
      assert.equal(res.exitCode, 0);
      assert.equal(res.stderr, '');
      assert.equal(session.pending.length, 3, 'a guarded Stop must not consume either');
    } finally {
      if (saved === undefined) delete process.env.XDG_RUNTIME_DIR;
      else process.env.XDG_RUNTIME_DIR = saved;
      await bound.close();
    }
  });
});

test('`landfall hooks stop` exits 0 with no stdin at all', async () => {
  await withRuntimeDir(async ({ root }) => {
    const saved = process.env.XDG_RUNTIME_DIR;
    process.env.XDG_RUNTIME_DIR = root;
    try {
      const res = await runCli(['hooks', 'stop']);
      assert.equal(res.exitCode, 0);
      assert.equal(res.stdout, '');
    } finally {
      if (saved === undefined) delete process.env.XDG_RUNTIME_DIR;
      else process.env.XDG_RUNTIME_DIR = saved;
    }
  });
});
