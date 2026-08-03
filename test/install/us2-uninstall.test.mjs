// us2-uninstall.test.mjs — T023: install → hand-add an unrelated MCP server
// → uninstall → the landfall entry is gone and everything else survives.
import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { withSandbox, seedSession, readJson, writeJson, runCli } from './helpers.mjs';

test('uninstall removes only the landfall entry; unrelated settings and servers survive', async () => {
  await withSandbox(async ({ homeDir }) => {
    await seedSession(homeDir);
    const cursorConfig = path.join(homeDir, '.cursor', 'mcp.json');
    await writeJson(cursorConfig, { mcpServers: {} });

    const install = await runCli(['install', '--only', 'cursor', '--yes']);
    assert.equal(install.stdout.trim(), `Cursor: configured (${cursorConfig})`);

    // Simulate the user adding their own, unrelated server by hand afterward.
    const withUnrelated = await readJson(cursorConfig);
    withUnrelated.mcpServers.someOtherTool = { command: 'other-tool', args: [] };
    withUnrelated.unrelatedTopLevelSetting = 'keep-me';
    await writeJson(cursorConfig, withUnrelated);

    const uninstall = await runCli(['uninstall', '--only', 'cursor', '--yes']);
    assert.equal(uninstall.stdout.trim(), `Cursor: removed (${cursorConfig})`);

    const after = await readJson(cursorConfig);
    assert.equal(after.mcpServers.landfall, undefined);
    assert.deepEqual(after.mcpServers.someOtherTool, { command: 'other-tool', args: [] });
    assert.equal(after.unrelatedTopLevelSetting, 'keep-me');
  });
});
