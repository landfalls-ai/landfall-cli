// us2-idempotent.test.mjs — T021: running install twice for the same
// harness never produces a duplicate entry; the second run reports
// `already-installed` and the file is byte-identical to after the first run.
import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { promises as fs } from 'node:fs';
import { withSandbox, seedSession, runCli } from './helpers.mjs';

test('install run twice: second run is already-installed and the file does not change', async () => {
  await withSandbox(async ({ homeDir }) => {
    await seedSession(homeDir);
    const cursorConfig = path.join(homeDir, '.cursor', 'mcp.json');
    await fs.mkdir(path.dirname(cursorConfig), { recursive: true });
    await fs.writeFile(cursorConfig, '{}\n');

    const first = await runCli(['install', '--only', 'cursor', '--yes']);
    assert.equal(first.stdout.trim(), `Cursor: configured (${cursorConfig})`);
    const afterFirst = await fs.readFile(cursorConfig, 'utf8');

    const second = await runCli(['install', '--only', 'cursor', '--yes']);
    assert.equal(second.stdout.trim(), `Cursor: already-installed (${cursorConfig})`);
    const afterSecond = await fs.readFile(cursorConfig, 'utf8');

    assert.equal(afterSecond, afterFirst);
  });
});
