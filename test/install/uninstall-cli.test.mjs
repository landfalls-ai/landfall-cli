// uninstall-cli.test.mjs — T020: contract test for `landfall uninstall`'s
// argv/exit-code/output shape (contracts/cli.md), spawning the real binary.
// Unlike install, uninstall requires no signed-in session.
import test from 'node:test';
import assert from 'node:assert/strict';
import { withSandbox, runCli } from './helpers.mjs';

const HARNESS_ORDER = ['Claude Code', 'Cursor', 'VS Code', 'Codex CLI', 'Claude Desktop', 'Windsurf'];

test('landfall uninstall --yes: nothing installed reports all six as not-installed, exit 0, no session needed', async () => {
  await withSandbox(async () => {
    const { stdout, exitCode } = await runCli(['uninstall', '--yes']);
    const lines = stdout.trim().split('\n');
    assert.equal(lines.length, HARNESS_ORDER.length);
    HARNESS_ORDER.forEach((name, i) => {
      assert.equal(lines[i], `${name}: not-installed`);
    });
    assert.equal(exitCode, 0);
  });
});

test('landfall uninstall --only <unknown-id> is a usage error (exit 2)', async () => {
  await withSandbox(async () => {
    const { exitCode } = await runCli(['uninstall', '--only', 'not-a-real-harness']);
    assert.equal(exitCode, 2);
  });
});
