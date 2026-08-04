// idempotent-and-nondestructive.test.mjs — the two guarantees #222 is
// actually judged on: "repeated installs are no-ops" and "existing user hooks
// are never clobbered".
import test from 'node:test';
import assert from 'node:assert/strict';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import { withSandbox, runCli, readJson, writeJson } from '../install/helpers.mjs';

const USER_HOOK = { hooks: [{ type: 'command', command: 'my-own-script.sh' }] };

async function claudeHome(homeDir) {
  await fs.mkdir(path.join(homeDir, '.claude'), { recursive: true });
  return path.join(homeDir, '.claude', 'settings.json');
}

test('install run twice: the second run is already-installed and the file is byte-identical', async () => {
  await withSandbox(async ({ homeDir }) => {
    const settings = await claudeHome(homeDir);

    const first = await runCli(['hooks', 'install', '--only', 'claude-code']);
    assert.equal(first.stdout.trim(), `Claude Code: configured (${settings})`);
    const afterFirst = await fs.readFile(settings, 'utf8');

    const second = await runCli(['hooks', 'install', '--only', 'claude-code']);
    assert.equal(second.stdout.trim(), `Claude Code: already-installed (${settings})`);
    assert.equal(await fs.readFile(settings, 'utf8'), afterFirst);

    // And not merely "no second write" — no duplicate entry either.
    const config = await readJson(settings);
    assert.equal(config.hooks.Stop.length, 1);
    assert.equal(config.hooks.FileChanged.length, 1);
  });
});

test('a user hook on the same event survives install, and survives uninstall', async () => {
  await withSandbox(async ({ homeDir }) => {
    const settings = await claudeHome(homeDir);
    await writeJson(settings, {
      model: 'opus',
      hooks: { Stop: [USER_HOOK] },
    });

    await runCli(['hooks', 'install', '--only', 'claude-code']);
    const installed = await readJson(settings);
    assert.equal(installed.model, 'opus', 'unrelated settings preserved');
    assert.deepEqual(installed.hooks.Stop[0], USER_HOOK, 'user hook still first');
    assert.equal(installed.hooks.Stop.length, 2);
    assert.equal(installed.hooks.Stop[1].hooks[0].command, 'landfall hooks stop');

    const { stdout } = await runCli(['hooks', 'uninstall', '--only', 'claude-code']);
    assert.equal(stdout.trim(), `Claude Code: removed (${settings})`);

    const after = await readJson(settings);
    assert.deepEqual(after.hooks.Stop, [USER_HOOK], 'ours gone, theirs untouched');
    assert.equal(after.model, 'opus');
    assert.equal('FileChanged' in after.hooks, false, 'a list we emptied is pruned, not left as []');
  });
});

test('install → uninstall on a file we created leaves no landfall residue', async () => {
  await withSandbox(async ({ homeDir }) => {
    const settings = await claudeHome(homeDir);
    await runCli(['hooks', 'install', '--only', 'claude-code']);
    await runCli(['hooks', 'uninstall', '--only', 'claude-code']);
    assert.deepEqual(await readJson(settings), {}, 'the empty hooks container is pruned too');
  });
});

test('a landfall entry edited by hand is a conflict: never overwritten, never duplicated', async () => {
  await withSandbox(async ({ homeDir }) => {
    const settings = await claudeHome(homeDir);
    const edited = { hooks: [{ type: 'command', command: 'landfall hooks stop --verbose' }] };
    await writeJson(settings, { hooks: { Stop: [edited] } });
    const before = await fs.readFile(settings, 'utf8');

    const { stdout, exitCode } = await runCli(['hooks', 'install', '--only', 'claude-code']);
    assert.match(stdout, /^Claude Code: conflict — an existing landfall hook entry differs/m);
    assert.equal(exitCode, 0, 'a conflict is a report, not a failure');
    assert.equal(await fs.readFile(settings, 'utf8'), before, 'not one byte written');
  });
});

test('uninstall leaves a hand-edited landfall entry in place rather than deleting it', async () => {
  await withSandbox(async ({ homeDir }) => {
    const settings = await claudeHome(homeDir);
    const edited = { hooks: [{ type: 'command', command: 'landfall hooks stop --verbose' }] };
    await writeJson(settings, { hooks: { Stop: [edited] } });

    const { stdout } = await runCli(['hooks', 'uninstall', '--only', 'claude-code']);
    assert.equal(stdout.trim(), `Claude Code: left-in-place (${settings})`);
    assert.deepEqual((await readJson(settings)).hooks.Stop, [edited]);
  });
});

test('an unparseable config is reported, never overwritten', async () => {
  await withSandbox(async ({ homeDir }) => {
    const settings = await claudeHome(homeDir);
    await fs.writeFile(settings, '{ this is not json');

    const { stdout, exitCode } = await runCli(['hooks', 'install', '--only', 'claude-code']);
    assert.match(stdout, /^Claude Code: failed — .*could not be parsed as JSON/m);
    assert.equal(exitCode, 1);
    assert.equal(await fs.readFile(settings, 'utf8'), '{ this is not json');
  });
});

test('uninstall on a machine that was never installed reports not-installed, exit 0', async () => {
  await withSandbox(async ({ homeDir }) => {
    await claudeHome(homeDir);
    const { stdout, exitCode } = await runCli(['hooks', 'uninstall', '--only', 'claude-code']);
    assert.match(stdout, /^Claude Code: not-installed /m);
    assert.equal(exitCode, 0);
  });
});

test('hooks install --uninstall is the ticket\'s flag form of hooks uninstall', async () => {
  await withSandbox(async ({ homeDir }) => {
    const settings = await claudeHome(homeDir);
    await runCli(['hooks', 'install', '--only', 'claude-code']);
    const { stdout } = await runCli(['hooks', 'install', '--only', 'claude-code', '--uninstall']);
    assert.equal(stdout.trim(), `Claude Code: removed (${settings})`);
    assert.deepEqual(await readJson(settings), {});
  });
});
