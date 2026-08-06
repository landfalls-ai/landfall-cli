// hooks-cli.test.mjs — `landfall hooks`'s argv/exit-code/output contract (#222),
// spawning the real binary against a sandboxed machine.
import test from 'node:test';
import assert from 'node:assert/strict';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import { withSandbox, runCli } from '../install/helpers.mjs';

const HOST_ORDER = ['Claude Code', 'Codex CLI', 'Cursor'];

test('hooks install on a clean machine: every host not-detected, exit 0', async () => {
  await withSandbox(async () => {
    const { stdout, exitCode } = await runCli(['hooks', 'install']);
    const lines = stdout.trim().split('\n');
    assert.deepEqual(lines, HOST_ORDER.map((n) => `${n}: not-detected`));
    assert.equal(exitCode, 0);
  });
});

test('hooks install needs no sign-in — a clean machine with no cached session still reports', async () => {
  await withSandbox(async ({ homeDir }) => {
    // No seedSession() anywhere in this file, deliberately: unlike `landfall
    // install`, editing local hook config is not an authenticated action.
    await fs.mkdir(path.join(homeDir, '.cursor'), { recursive: true });
    const { stdout, exitCode } = await runCli(['hooks', 'install', '--only', 'cursor']);
    assert.match(stdout, /^Cursor: configured /m);
    assert.equal(exitCode, 0);
  });
});

test('hooks install --only <unknown> is a usage error (exit 2) and writes nothing', async () => {
  await withSandbox(async ({ homeDir }) => {
    await fs.mkdir(path.join(homeDir, '.cursor'), { recursive: true });
    const { exitCode } = await runCli(['hooks', 'install', '--only', 'not-a-host']);
    assert.equal(exitCode, 2);
    assert.equal(
      await fs.access(path.join(homeDir, '.cursor', 'hooks.json')).then(() => true, () => false),
      false,
    );
  });
});

test('hooks with no subcommand prints usage and exits 2', async () => {
  await withSandbox(async () => {
    const { stdout, stderr, exitCode } = await runCli(['hooks']);
    assert.equal(exitCode, 2);
    assert.match(
      stderr,
      /usage: landfall hooks <install\|uninstall\|policy\|stop\|file-changed\|user-prompt-submit\|pre-tool-use>/,
    );
    assert.equal(stdout, '');
  });
});

test('hooks install --dry-run reports would-configure and writes nothing', async () => {
  await withSandbox(async ({ homeDir }) => {
    await fs.mkdir(path.join(homeDir, '.claude'), { recursive: true });
    const settings = path.join(homeDir, '.claude', 'settings.json');

    const { stdout, exitCode } = await runCli(['hooks', 'install', '--only', 'claude-code', '--dry-run']);
    assert.equal(stdout.trim(), `Claude Code: would-configure (${settings})`);
    assert.equal(exitCode, 0);
    assert.equal(await fs.access(settings).then(() => true, () => false), false);
  });
});

test('landfall hooks stop exits 0 and prints nothing on stdout', async () => {
  // The host parses stdout, and #225 has not landed: the safe default for
  // "no room context pending" is silence plus a zero exit.
  await withSandbox(async () => {
    const { stdout, exitCode } = await runCli(['hooks', 'stop']);
    assert.equal(stdout, '');
    assert.equal(exitCode, 0);
  });
});

test('landfall hooks <unknown-event> exits 2 rather than silently succeeding', async () => {
  await withSandbox(async () => {
    const { exitCode } = await runCli(['hooks', 'not-an-event']);
    assert.equal(exitCode, 2);
  });
});
