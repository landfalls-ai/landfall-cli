// us3-yes-flag.test.mjs — T035: `--yes` configures every detected harness
// with no prompt interaction at all (no stdin is ever read).
import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { promises as fs } from 'node:fs';
import { withSandbox, seedSession, readJson, writeJson, runCli } from './helpers.mjs';

test('--yes configures every detected harness without any prompt', async () => {
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

    // No `input` at all — if this ever tried to prompt interactively it
    // would hang waiting on stdin instead of exiting promptly.
    const { stdout, exitCode } = await runCli(['install', '--yes']);

    assert.equal(exitCode, 0);
    assert.match(stdout, /^Cursor: configured/m);
    assert.match(stdout, /^Claude Desktop: configured/m);
    assert.deepEqual((await readJson(cursorConfig)).mcpServers.landfall, { command: 'landfall', args: ['serve'] });
    assert.deepEqual((await readJson(claudeDesktopConfig)).mcpServers.landfall, { command: 'landfall', args: ['serve'] });
  });
});
