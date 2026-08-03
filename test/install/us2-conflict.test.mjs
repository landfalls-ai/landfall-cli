// us2-conflict.test.mjs — T022: an existing, differently-configured
// "landfall" entry is never overwritten — install() reports `conflict`.
import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { withSandbox, seedSession, readJson, writeJson, runCli } from './helpers.mjs';

test('install reports conflict and does not overwrite a differing existing entry', async () => {
  await withSandbox(async ({ homeDir }) => {
    await seedSession(homeDir);
    const cursorConfig = path.join(homeDir, '.cursor', 'mcp.json');
    const before = { mcpServers: { landfall: { command: 'some-other-thing', args: ['--weird'] } } };
    await writeJson(cursorConfig, before);

    const { stdout, exitCode } = await runCli(['install', '--only', 'cursor', '--yes']);

    assert.match(stdout, /^Cursor: conflict — /);
    assert.equal(exitCode, 0); // conflict is not `failed` (contracts/cli.md)
    assert.deepEqual(await readJson(cursorConfig), before);
  });
});
