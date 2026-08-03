// us3-only-flag.test.mjs — T034: `--only <id>` restricts to one named
// harness even when others are also detected; they are neither prompted for
// nor touched. An unknown id is a usage error (also covered by the CLI
// contract test; repeated here in the "several detected" context).
import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { promises as fs } from 'node:fs';
import { withSandbox, seedSession, readJson, writeJson, runCli } from './helpers.mjs';

test('--only cursor ignores Claude Desktop even though it is also detected', async () => {
  await withSandbox(async ({ homeDir, appRoot }) => {
    await seedSession(homeDir);

    const cursorConfig = path.join(homeDir, '.cursor', 'mcp.json');
    await writeJson(cursorConfig, {});
    await fs.mkdir(path.join(appRoot, 'Claude.app'), { recursive: true });
    const claudeDesktopConfig = path.join(
      homeDir,
      'Library',
      'Application Support',
      'Claude',
      'claude_desktop_config.json',
    );

    const { stdout, exitCode } = await runCli(['install', '--only', 'cursor', '--yes']);

    assert.equal(exitCode, 0);
    assert.equal(stdout.trim(), `Cursor: configured (${cursorConfig})`);
    assert.equal(stdout.includes('Claude Desktop'), false);
    await assert.rejects(fs.access(claudeDesktopConfig)); // never even written
  });
});

test('--only accepts a comma-separated list', async () => {
  await withSandbox(async ({ homeDir }) => {
    await seedSession(homeDir);
    const { stdout, exitCode } = await runCli(['install', '--only', 'cursor,codex', '--yes']);
    assert.equal(exitCode, 0);
    const lines = stdout.trim().split('\n');
    assert.deepEqual(lines, ['Cursor: not-detected', 'Codex CLI: not-detected']);
  });
});
