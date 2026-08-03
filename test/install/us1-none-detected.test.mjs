// us1-none-detected.test.mjs — T009: no supported harness present → the
// report says so and zero files are touched anywhere under HOME.
import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { promises as fsp } from 'node:fs';
import { withSandbox, seedSession, runCli } from './helpers.mjs';

test('install --yes on a clean machine touches nothing under HOME', async () => {
  await withSandbox(async ({ homeDir }) => {
    await seedSession(homeDir);
    const before = await fsp.readdir(homeDir, { recursive: true });

    const { stdout, exitCode } = await runCli(['install', '--yes']);

    assert.equal(exitCode, 0);
    assert.ok(stdout.trim().split('\n').every((line) => /: not-detected$/.test(line)));

    const after = await fsp.readdir(homeDir, { recursive: true });
    // Only the seeded session file (and its directory) may exist — nothing
    // install() itself would have created.
    const unexpected = after.filter((entry) => !before.includes(entry));
    assert.deepEqual(unexpected, []);
    for (const configFile of ['.cursor/mcp.json', '.claude.json', '.codex/config.toml', '.codeium/windsurf/mcp_config.json']) {
      await assert.rejects(fsp.access(path.join(homeDir, configFile)));
    }
  });
});
