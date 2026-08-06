// cursor-adapter.test.mjs — the Cursor hook adapter (#228, story #190).
//
// Cursor is the one host whose hook contract is not the exit-2 convention the
// other two share, and the failure mode of getting it wrong is silent: an
// entry sits in the user's config, fires on every turn, and its verdict is
// discarded (or, worse, its empty stdout is a JSON parse error). So these
// tests assert the WIRE, not the intent — what is on stdout, what the exit
// code is, and what ends up in ~/.cursor/hooks.json.
//
// Three layers, mirroring stop-hook.test.mjs:
//   1. the rendering  — pure, per protocol
//   2. the handler    — runStopHook under cursor-json, including the loop guard
//   3. the wire + CLI — the real binary, and the real config merge
import test from 'node:test';
import assert from 'node:assert/strict';
import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

import { createBridgeSession, consumeUpTo } from '../../src/tools.mjs';
import { handleSocketRequest, startHookSocket } from '../../src/hooks/socket.mjs';
import { runStopHook, isStopHookActive, isConcludedTurn } from '../../src/hooks/stop.mjs';
import { runHookEvent } from '../../src/hooks/run.mjs';
import {
  CURSOR_JSON,
  EXIT2,
  protocolForHost,
  renderNoOp,
  renderStopVerdict,
} from '../../src/hooks/protocol.mjs';
import { hookCommand, supersededHookCommands } from '../../src/hooks/spec.mjs';
import { withSandbox, runCli, readJson, writeJson } from '../install/helpers.mjs';

const CURSOR_STOP = 'landfall hooks stop --host cursor';
const LEGACY_STOP = 'landfall hooks stop';
const USER_HOOK = { command: 'my-own-script.sh' };

function sessionWith(n) {
  const session = createBridgeSession({ client: { cfg: { slug: 'acme', incidentId: 'inc-1' } } });
  for (let i = 0; i < n; i += 1) {
    session.enqueueEvent({
      seq: i + 1,
      type: 'edge.finding',
      payload: { displayName: 'Ana', text: `origin 5xx spiking on shard ${i}` },
    });
  }
  return session;
}

const peekOf = (session) => [{ socketPath: '/s', response: handleSocketRequest({ op: 'peek' }, session, { pid: 1 }) }];

async function withRuntimeDir(fn) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'landfall-cursor-hook-'));
  const saved = process.env.XDG_RUNTIME_DIR;
  process.env.XDG_RUNTIME_DIR = root; // runCli spawns with the current env
  try {
    return await fn({ env: { ...process.env, XDG_RUNTIME_DIR: root }, root });
  } finally {
    if (saved === undefined) delete process.env.XDG_RUNTIME_DIR;
    else process.env.XDG_RUNTIME_DIR = saved;
    await fs.rm(root, { recursive: true, force: true });
  }
}

const cursorConfig = (homeDir) => path.join(homeDir, '.cursor', 'hooks.json');

async function cursorHome(homeDir) {
  await fs.mkdir(path.join(homeDir, '.cursor'), { recursive: true });
  return cursorConfig(homeDir);
}

// --------------------------------------------------------------- 1. rendering

test('a host is mapped to a protocol, and anything unknown falls back to exit 2', () => {
  assert.equal(protocolForHost('cursor'), CURSOR_JSON);
  assert.equal(protocolForHost('claude-code'), EXIT2);
  assert.equal(protocolForHost('codex'), EXIT2);
  // A hook runs on every turn; an unrecognized --host must not be fatal.
  assert.equal(protocolForHost('windsurf-someday'), EXIT2);
  assert.equal(protocolForHost(undefined), EXIT2);
});

test('exit2: block is exit 2 + stderr, allow is silence', () => {
  const blocked = renderStopVerdict(EXIT2, { block: true, reason: 'read the room' });
  assert.equal(blocked.exitCode, 2);
  assert.equal(blocked.channel, 'stderr');
  assert.equal(blocked.text, 'read the room\n');

  const allowed = renderStopVerdict(EXIT2, { block: false, reason: '' });
  assert.equal(allowed.exitCode, 0);
  assert.equal(allowed.text, '', 'silence is a valid answer under exit2 and the one we already ship');
});

test('cursor-json: block is a followup_message, allow is {}, and BOTH exit 0', () => {
  const blocked = renderStopVerdict(CURSOR_JSON, { block: true, reason: 'read the room' });
  assert.equal(blocked.channel, 'stdout');
  assert.deepEqual(JSON.parse(blocked.text), { followup_message: 'read the room' });
  assert.equal(blocked.exitCode, 0, 'non-zero means "the hook failed", not "the hook objected"');

  const allowed = renderStopVerdict(CURSOR_JSON, { block: false, reason: '' });
  assert.equal(allowed.exitCode, 0);
  assert.deepEqual(JSON.parse(allowed.text), {}, 'empty stdout is a parse error, not an allow');
  assert.equal(renderNoOp(CURSOR_JSON).text, allowed.text);
});

test('cursor-json output is exactly one JSON object and nothing else', () => {
  const text = renderStopVerdict(CURSOR_JSON, { block: true, reason: 'line one\nline two' }).text;
  assert.equal(text.trimEnd().split('\n').length, 1, 'a multi-line reason must not become multi-line stdout');
  assert.deepEqual(JSON.parse(text), { followup_message: 'line one\nline two' });
});

// ----------------------------------------------------------------- 2. handler

test('under cursor-json a refusal is a followup_message, and it still consumes', async () => {
  const session = sessionWith(2);
  const sent = [];
  let emitted = null;
  const res = await runStopHook({
    input: JSON.stringify({ hook_event_name: 'stop', status: 'completed' }),
    protocol: CURSOR_JSON,
    query: async () => peekOf(session),
    send: async (_p, req) => sent.push(req),
    emit: (text, { channel }) => { emitted = { text, channel }; },
  });

  assert.equal(res.blocked, true);
  assert.equal(res.exitCode, 0);
  assert.equal(emitted.channel, 'stdout');
  assert.match(JSON.parse(emitted.text).followup_message, /Do not conclude yet — 2 update\(s\)/);
  assert.deepEqual(sent, [{ op: 'consume', upTo: 2 }], 'consume-after-block is what bounds the followups');
});

test('under cursor-json an ALLOW still speaks — silence would be a parse error', async () => {
  let emitted = null;
  const res = await runStopHook({
    protocol: CURSOR_JSON,
    query: async () => [],
    emit: (text, { channel }) => { emitted = { text, channel }; },
  });
  assert.equal(res.exitCode, 0);
  assert.equal(emitted.channel, 'stdout');
  assert.deepEqual(JSON.parse(emitted.text), {});
});

test('every early exit under cursor-json still answers: guard, aborted turn, and a failed query', async () => {
  const cases = [
    ['loop guard', { input: JSON.stringify({ loop_count: 1 }), query: async () => peekOf(sessionWith(2)) }],
    ['aborted turn', { input: JSON.stringify({ status: 'aborted' }), query: async () => peekOf(sessionWith(2)) }],
    ['query failed', { query: async () => { throw new Error('permission denied'); } }],
  ];
  for (const [name, deps] of cases) {
    let emitted = null;
    const res = await runStopHook({
      protocol: CURSOR_JSON,
      send: () => assert.fail(`${name} must not consume`),
      emit: (text) => { emitted = text; },
      ...deps,
    });
    assert.equal(res.exitCode, 0, name);
    assert.deepEqual(JSON.parse(emitted), {}, `${name} must still print one JSON object`);
  }
});

test('loop_count is the guard Cursor has, since it sends no stop_hook_active', () => {
  assert.equal(isStopHookActive(JSON.stringify({ loop_count: 1 })), true);
  assert.equal(isStopHookActive(JSON.stringify({ loop_count: 3 })), true);
  assert.equal(isStopHookActive(JSON.stringify({ loop_count: 0 })), false, 'the first Stop of a turn is not guarded');
  // A Cursor version that sends no count is not thereby "already guarded" —
  // it falls back to consume-after-block plus Cursor's own followup cap.
  assert.equal(isStopHookActive(JSON.stringify({ hook_event_name: 'stop', status: 'completed' })), false);
  assert.equal(isStopHookActive(JSON.stringify({ stop_hook_active: true })), true, 'the exit2 guard is untouched');
});

test('an aborted or errored turn is not a conclusion worth interrupting', () => {
  assert.equal(isConcludedTurn(JSON.stringify({ status: 'completed' })), true);
  assert.equal(isConcludedTurn(JSON.stringify({ status: 'aborted' })), false);
  assert.equal(isConcludedTurn(JSON.stringify({ status: 'error' })), false);
  // Hosts that send no status at all are unaffected: absent means concluded.
  assert.equal(isConcludedTurn('{}'), true);
  assert.equal(isConcludedTurn(''), true);
  assert.equal(isConcludedTurn('not json'), true);
});

test('an aborted turn keeps its events queued for the next real conclusion', async () => {
  const session = sessionWith(2);
  await runStopHook({
    input: JSON.stringify({ status: 'aborted' }),
    protocol: CURSOR_JSON,
    query: async () => peekOf(session),
    send: () => assert.fail('an interrupted turn must not consume what it was never told'),
    emit: () => {},
  });
  assert.equal(session.pending.length, 2);
});

test('an event with no behaviour yet still answers its host correctly', async () => {
  assert.deepEqual(JSON.parse((await runHookEvent('file-changed', { host: 'cursor' })).stdout), {});
  const exit2 = await runHookEvent('file-changed', { host: 'claude-code' });
  assert.equal(exit2.exitCode, 0);
  assert.equal(exit2.stdout ?? '', '', 'stdout is Claude Code\'s own channel — #227 has not landed either way');
});

// -------------------------------------------------------------- 3. wire + CLI

test('`landfall hooks stop --host cursor` writes JSON to stdout and exits 0', async () => {
  await withRuntimeDir(async ({ env }) => {
    const session = sessionWith(2);
    const bound = await startHookSocket(session, { env, consume: consumeUpTo, pid: 6100 });
    try {
      const blocked = await runCli(['hooks', 'stop', '--host', 'cursor'], { input: '{}' });
      assert.equal(blocked.exitCode, 0, 'a non-zero exit reads as a broken hook to Cursor');
      assert.equal(blocked.stderr, '', 'Cursor never shows the model stderr');
      const verdict = JSON.parse(blocked.stdout);
      assert.match(verdict.followup_message, /Do not conclude yet — 2 update\(s\)/);
      assert.match(verdict.followup_message, /#1 edge\.finding \[Ana\]/);

      // Consumed, so an unchanged room lets the next stop through — with `{}`,
      // not with silence.
      assert.equal(session.pending.length, 0);
      const allowed = await runCli(['hooks', 'stop', '--host', 'cursor'], { input: '{}' });
      assert.equal(allowed.exitCode, 0);
      assert.deepEqual(JSON.parse(allowed.stdout), {});
    } finally {
      await bound.close();
    }
  });
});

test('the exit2 hosts are untouched by the flag existing: no --host is still exit 2 + stderr', async () => {
  await withRuntimeDir(async ({ env }) => {
    const session = sessionWith(1);
    const bound = await startHookSocket(session, { env, consume: consumeUpTo, pid: 6101 });
    try {
      const res = await runCli(['hooks', 'stop'], { input: '{}' });
      assert.equal(res.exitCode, 2);
      assert.equal(res.stdout, '');
      assert.match(res.stderr, /Do not conclude yet/);
    } finally {
      await bound.close();
    }
  });
});

test('an unknown --host answers on the convention two of three hosts share, rather than dying', async () => {
  await withRuntimeDir(async ({ env }) => {
    const session = sessionWith(1);
    const bound = await startHookSocket(session, { env, consume: consumeUpTo, pid: 6102 });
    try {
      const res = await runCli(['hooks', 'stop', '--host', 'not-a-host'], { input: '{}' });
      assert.equal(res.exitCode, 2);
      assert.match(res.stderr, /Do not conclude yet/);
    } finally {
      await bound.close();
    }
  });
});

test('upgrade: the bare entry v0.2.0 wrote is replaced in place, not duplicated, not conflicted', async () => {
  await withSandbox(async ({ homeDir }) => {
    const config = await cursorHome(homeDir);
    await writeJson(config, {
      version: 1,
      hooks: { stop: [{ command: LEGACY_STOP }, USER_HOOK] },
    });

    const { stdout, exitCode } = await runCli(['hooks', 'install', '--only', 'cursor']);
    assert.match(stdout, /^Cursor: configured /m);
    assert.equal(exitCode, 0);

    const after = await readJson(config);
    assert.deepEqual(after.hooks.stop, [{ command: CURSOR_STOP }, USER_HOOK], 'ours upgraded where it stood, theirs untouched');
  });
});

test('upgrade is idempotent: the run after it is already-installed and writes nothing', async () => {
  await withSandbox(async ({ homeDir }) => {
    const config = await cursorHome(homeDir);
    await writeJson(config, { version: 1, hooks: { stop: [{ command: LEGACY_STOP }] } });

    await runCli(['hooks', 'install', '--only', 'cursor']);
    const afterFirst = await fs.readFile(config, 'utf8');
    const { stdout } = await runCli(['hooks', 'install', '--only', 'cursor']);

    assert.match(stdout, /^Cursor: already-installed /m);
    assert.equal(await fs.readFile(config, 'utf8'), afterFirst);
    assert.equal((await readJson(config)).hooks.stop.length, 1);
  });
});

test('a config holding BOTH the old and the new entry ends up holding one', async () => {
  await withSandbox(async ({ homeDir }) => {
    const config = await cursorHome(homeDir);
    await writeJson(config, {
      version: 1,
      hooks: { stop: [{ command: LEGACY_STOP }, { command: CURSOR_STOP }] },
    });

    await runCli(['hooks', 'install', '--only', 'cursor']);
    assert.deepEqual((await readJson(config)).hooks.stop, [{ command: CURSOR_STOP }]);
  });
});

test('uninstall clears a stale entry too — "removed everything landfall added" is meant literally', async () => {
  await withSandbox(async ({ homeDir }) => {
    const config = await cursorHome(homeDir);
    await writeJson(config, { version: 1, hooks: { stop: [{ command: LEGACY_STOP }, USER_HOOK] } });

    const { stdout } = await runCli(['hooks', 'uninstall', '--only', 'cursor']);
    assert.equal(stdout.trim(), `Cursor: removed (${config})`);
    assert.deepEqual((await readJson(config)).hooks.stop, [USER_HOOK]);
  });
});

test('a hand-edited entry is still a conflict — superseding is an enumerated list, not a prefix rule', async () => {
  await withSandbox(async ({ homeDir }) => {
    const config = await cursorHome(homeDir);
    await writeJson(config, { version: 1, hooks: { stop: [{ command: 'landfall hooks stop --verbose' }] } });
    const before = await fs.readFile(config, 'utf8');

    const { stdout, exitCode } = await runCli(['hooks', 'install', '--only', 'cursor']);
    assert.match(stdout, /^Cursor: conflict — an existing landfall hook entry differs/m);
    assert.equal(exitCode, 0);
    assert.equal(await fs.readFile(config, 'utf8'), before, 'not one byte written');

    const removal = await runCli(['hooks', 'uninstall', '--only', 'cursor']);
    assert.equal(removal.stdout.trim(), `Cursor: left-in-place (${config})`);
  });
});

test('the superseded list names the exact string the old installer wrote', () => {
  assert.equal(hookCommand('stop', 'cursor'), CURSOR_STOP);
  assert.deepEqual(supersededHookCommands('stop', 'cursor'), [LEGACY_STOP]);
  // Nothing is superseded for a host whose command never changed — declaring
  // one there would make a hand-edit look like an upgrade.
  assert.deepEqual(supersededHookCommands('stop', 'claude-code'), []);
  assert.deepEqual(supersededHookCommands('file-changed', 'claude-code'), []);
});

test('beforeSubmitPrompt is deliberately not registered while its behaviour is undecided (#228)', async () => {
  await withSandbox(async ({ homeDir }) => {
    const config = await cursorHome(homeDir);
    await runCli(['hooks', 'install', '--only', 'cursor']);
    // Cursor's beforeSubmitPrompt output schema is `{continue}` alone — it can
    // refuse a prompt, it cannot inject context, so it cannot carry #227's
    // digest. Registering it on a guess would put an entry in a user's config
    // that either does nothing or silently blocks their typing.
    assert.deepEqual(Object.keys((await readJson(config)).hooks), ['stop']);
  });
});
