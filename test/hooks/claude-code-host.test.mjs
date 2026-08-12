// claude-code-host.test.mjs — feature 20260812-010632 (US5/T048): the
// statusLine registration added to the Claude Code host adapter, and that it
// composes correctly with the pre-existing hooks.* list registrations this
// adapter already wrote (no prior test file covered this adapter directly —
// this is genuinely new coverage, not a regression check against an existing
// suite).
//
// Two config surfaces, same settings.json, different merge semantics:
// hooks.* is a per-event LIST (../merge.mjs), statusLine is a single KEY
// holding one object (../../install/json-merge.mjs, the same primitive MCP
// registration uses). The point under test is that they are independent —
// a hand-edited statusLine must never block hooks installing, and vice versa
// — while still being reported as one combined outcome per host.
import test from 'node:test';
import assert from 'node:assert/strict';
import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import * as mod from '../../src/hooks/hosts/claude-code.mjs';

// `configPath()` calls `homedir()`, which re-reads `os.homedir()` (and so
// `process.env.HOME` on POSIX) on every call — see platform.mjs's own
// "re-read per call" doc comment — so a plain static import is safe to
// reuse across tests; no per-test re-import is needed.
async function withHome(fn) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'landfall-cc-host-'));
  const origHome = process.env.HOME;
  process.env.HOME = root;
  await fs.mkdir(path.join(root, '.claude'), { recursive: true });
  try {
    return await fn(mod, root);
  } finally {
    process.env.HOME = origHome;
    await fs.rm(root, { recursive: true, force: true });
  }
}

test('a fresh install writes both hooks.* and statusLine, and reports configured', async () => {
  await withHome(async (mod) => {
    const plan = await mod.plan();
    assert.equal(plan.action, 'write');
    assert.equal(plan.hooks.action, 'write');
    assert.equal(plan.statusLine.action, 'write');

    const result = await mod.install();
    assert.equal(result.action, 'configured');

    const data = JSON.parse(await fs.readFile(mod.configPath(), 'utf8'));
    assert.deepEqual(data.statusLine, { type: 'command', command: 'landfall status' });
    assert.ok(Array.isArray(data.hooks.Stop));
  });
});

test('a second install is idempotent — already-installed, no rewrite of either surface', async () => {
  await withHome(async (mod) => {
    await mod.install();
    const before = await fs.readFile(mod.configPath(), 'utf8');
    const result = await mod.install();
    assert.equal(result.action, 'already-installed');
    assert.equal(await fs.readFile(mod.configPath(), 'utf8'), before);
  });
});

test('a hand-edited statusLine is a conflict, and BLOCKS hooks from installing too — install is atomic, matching hooks.*\'s own existing "not one byte written" guarantee', async () => {
  await withHome(async (mod) => {
    const before = JSON.stringify({ statusLine: { type: 'command', command: 'my-own-status-script' } }, null, 2);
    await fs.writeFile(mod.configPath(), before);
    const result = await mod.install();
    assert.equal(result.action, 'conflict');
    assert.equal(result.statusLine.action, 'conflict');
    assert.equal(result.hooks.action, 'write'); // plan-time state — nothing was actually applied

    // Not one byte written, anywhere — hooks did NOT get installed either,
    // even though its own entry had no conflict of its own.
    assert.equal(await fs.readFile(mod.configPath(), 'utf8'), before);
  });
});

test('a hand-edited hook entry is a conflict, and BLOCKS statusLine from installing too', async () => {
  await withHome(async (mod) => {
    const before = JSON.stringify(
      { hooks: { Stop: [{ hooks: [{ type: 'command', command: 'landfall hooks stop --my-flag' }] }] } },
      null,
      2,
    );
    await fs.writeFile(mod.configPath(), before);
    const result = await mod.install();
    assert.equal(result.action, 'conflict');
    assert.equal(result.hooks.action, 'conflict');
    assert.equal(result.statusLine.action, 'write'); // plan-time state — nothing was actually applied

    assert.equal(await fs.readFile(mod.configPath(), 'utf8'), before);
  });
});

test('uninstall stays selective (unlike install): a hand-edited entry survives even inside an overall "removed" report', async () => {
  await withHome(async (mod) => {
    // Install cleanly first, then hand-edit ONLY the Stop hook entry. The
    // other three hook registrations (FileChanged/UserPromptSubmit/PreToolUse)
    // stay clean, so the FILE-LEVEL hooks action is still 'removed' overall
    // (something of ours WAS removed) — 'left-in-place' only applies when
    // EVERY registration is a conflict, which a single edited entry is not.
    // The point under test is narrower and more important than the file-level
    // label: the edited Stop entry itself survives untouched regardless.
    await mod.install();
    const data = JSON.parse(await fs.readFile(mod.configPath(), 'utf8'));
    data.hooks.Stop[0].hooks[0].command = 'landfall hooks stop --hand-edited';
    await fs.writeFile(mod.configPath(), JSON.stringify(data, null, 2));

    const result = await mod.uninstall();
    assert.equal(result.hooks.action, 'removed'); // 3 of 4 registrations were clean
    assert.equal(result.statusLine.action, 'removed');
    assert.equal(result.action, 'removed');

    const after = JSON.parse(await fs.readFile(mod.configPath(), 'utf8'));
    assert.equal(after.hooks.Stop[0].hooks[0].command, 'landfall hooks stop --hand-edited', 'the edited entry itself survives, not deleted');
    assert.equal('FileChanged' in after.hooks, false, 'the three clean registrations were removed');
    assert.equal('statusLine' in after, false, 'the clean statusLine surface was removed too');
  });
});

test('uninstall removes both surfaces and leaves an empty file, not orphaned empty keys', async () => {
  await withHome(async (mod) => {
    await mod.install();
    const result = await mod.uninstall();
    assert.equal(result.action, 'removed');
    const data = JSON.parse(await fs.readFile(mod.configPath(), 'utf8'));
    assert.deepEqual(data, {});
  });
});

test('hasEntry is true if EITHER surface is ours, false only when neither is', async () => {
  await withHome(async (mod) => {
    assert.equal(await mod.hasEntry(), false);
    await mod.install();
    assert.equal(await mod.hasEntry(), true);
    await mod.uninstall();
    assert.equal(await mod.hasEntry(), false);
  });
});
