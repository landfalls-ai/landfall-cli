// us2-left-in-place.test.mjs — T024: install → hand-edit the landfall entry
// to something else → uninstall reports `left-in-place` and the hand-edit
// survives (uninstall never deletes an entry it no longer recognizes as its own).
import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { withSandbox, seedSession, readJson, writeJson, runCli } from './helpers.mjs';

test('uninstall leaves a hand-edited entry in place instead of deleting it', async () => {
  await withSandbox(async ({ homeDir }) => {
    await seedSession(homeDir);
    const cursorConfig = path.join(homeDir, '.cursor', 'mcp.json');
    await writeJson(cursorConfig, { mcpServers: {} });

    await runCli(['install', '--only', 'cursor', '--yes']);

    const handEdited = await readJson(cursorConfig);
    handEdited.mcpServers.landfall = { command: 'landfall', args: ['serve', '--verbose'] };
    await writeJson(cursorConfig, handEdited);

    const { stdout } = await runCli(['uninstall', '--only', 'cursor', '--yes']);
    assert.equal(stdout.trim(), `Cursor: left-in-place (${cursorConfig})`);

    const after = await readJson(cursorConfig);
    assert.deepEqual(after.mcpServers.landfall, { command: 'landfall', args: ['serve', '--verbose'] });
  });
});
