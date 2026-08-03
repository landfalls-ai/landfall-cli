// us3-selective.test.mjs — T033: two harnesses detected, decline one in the
// interactive prompt, accept the other; only the accepted one changes.
import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { promises as fs } from 'node:fs';
import { withSandbox, seedSession, readJson, writeJson, runCli } from './helpers.mjs';

// Claude Desktop app-bundle detection only has real semantics on macOS/Windows
// (platform.mjs#appBundleCandidates returns [] on every other platform), so this
// test is structurally unrunnable on Linux CI — skip there rather than fail.
const skip =
  process.platform === 'darwin' || process.platform === 'win32'
    ? false
    : 'Claude Desktop app-bundle detection requires macOS or Windows';

test('interactive install: decline Cursor, accept Claude Desktop — only Claude Desktop changes', { skip }, async () => {
  await withSandbox(async ({ homeDir, appRoot }) => {
    await seedSession(homeDir);

    const cursorConfig = path.join(homeDir, '.cursor', 'mcp.json');
    const cursorBefore = { mcpServers: { keep: { command: 'keep-me' } } };
    await writeJson(cursorConfig, cursorBefore);

    // Simulate Claude Desktop being installed (app-bundle presence).
    await fs.mkdir(path.join(appRoot, 'Claude.app'), { recursive: true });
    const claudeDesktopConfig = path.join(
      homeDir,
      'Library',
      'Application Support',
      'Claude',
      'claude_desktop_config.json',
    );

    // Prompt order follows the fixed registry order restricted to detected
    // harnesses: Cursor comes before Claude Desktop. Decline the first,
    // accept the second.
    const { stdout, exitCode } = await runCli(['install'], { input: 'n\ny\n' });

    assert.equal(exitCode, 0);
    assert.match(stdout, /Cursor: skipped/);
    assert.match(stdout, new RegExp(`Claude Desktop: configured \\(${claudeDesktopConfig.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\)`));

    assert.deepEqual(await readJson(cursorConfig), cursorBefore); // declined — untouched
    const desktopAfter = await readJson(claudeDesktopConfig);
    assert.deepEqual(desktopAfter.mcpServers.landfall, { command: 'landfall', args: ['serve'] });
  });
});
