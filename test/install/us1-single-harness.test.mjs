// us1-single-harness.test.mjs — T008: one harness (Cursor) present, select
// it, assert its entry is written and every other setting untouched.
import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { withSandbox, seedSession, readJson, writeJson, runCli } from './helpers.mjs';

test('install --only cursor --yes writes mcpServers.landfall and leaves other Cursor settings untouched', async () => {
  await withSandbox(async ({ homeDir }) => {
    await seedSession(homeDir);
    const cursorConfig = path.join(homeDir, '.cursor', 'mcp.json');
    await writeJson(cursorConfig, { mcpServers: { other: { command: 'foo', args: ['bar'] } }, someOtherSetting: true });

    const { stdout, exitCode } = await runCli(['install', '--only', 'cursor', '--yes']);

    assert.equal(exitCode, 0);
    assert.equal(stdout.trim(), `Cursor: configured (${cursorConfig})`);

    const after = await readJson(cursorConfig);
    assert.deepEqual(after.mcpServers.landfall, { command: 'landfall', args: ['serve'] });
    assert.deepEqual(after.mcpServers.other, { command: 'foo', args: ['bar'] });
    assert.equal(after.someOtherSetting, true);
  });
});
