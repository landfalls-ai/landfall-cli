// install-cli.test.mjs — T007: contract test for `landfall install`'s
// argv/exit-code/output shape (contracts/cli.md), spawning the real binary.
import test from 'node:test';
import assert from 'node:assert/strict';
import { withSandbox, seedSession, runCli } from './helpers.mjs';

const HARNESS_ORDER = ['Claude Code', 'Cursor', 'VS Code', 'Codex CLI', 'Claude Desktop', 'Windsurf'];

test('landfall install --yes: nothing detected on a clean machine reports all six as not-detected, exit 0', async () => {
  await withSandbox(async ({ homeDir }) => {
    await seedSession(homeDir);
    const { stdout, exitCode } = await runCli(['install', '--yes']);
    const lines = stdout.trim().split('\n');
    assert.equal(lines.length, HARNESS_ORDER.length);
    HARNESS_ORDER.forEach((name, i) => {
      assert.equal(lines[i], `${name}: not-detected`);
    });
    assert.equal(exitCode, 0);
  });
});

test('landfall install --only <unknown-id> is a usage error (exit 2), no session required', async () => {
  await withSandbox(async () => {
    const { exitCode } = await runCli(['install', '--only', 'not-a-real-harness']);
    assert.equal(exitCode, 2);
  });
});
