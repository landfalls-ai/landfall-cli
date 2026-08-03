// claude-code-plugin.test.mjs — the Claude Code harness's install() also
// best-effort wires up the landfall-edge-bridge Claude Code PLUGIN (the
// investigation-dashboard agent), not just the MCP server registration. This
// rides `claude plugin marketplace add` / `claude plugin install` after the
// existing `claude mcp add-json` call succeeds, and must never turn a
// successful MCP registration into a reported failure if the plugin step
// fails (offline, an unreleased branch, an older `claude` CLI).
import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { promises as fs } from 'node:fs';
import { withSandbox, seedSession, runCli, writeJson } from './helpers.mjs';

/**
 * A `claude` stub that logs every invocation and can fail selectively by
 * subcommand — unlike the shared `stubExecutable` helper (one fixed exit
 * code for every call), this test needs `mcp` calls to succeed independently
 * of whether `plugin` calls do. A plain POSIX shell script (not the shared
 * helper's `#!/usr/bin/env node`): the sandboxed test PATH deliberately
 * excludes the real Node install directory (helpers.mjs `minimalSystemPath`),
 * so an `env node` shebang can't resolve here, but `/bin/sh` is always
 * present. None of this test's argv tokens contain spaces, so a
 * space-joined log line round-trips cleanly through `String#split(' ')`.
 */
async function stubClaudeCli(pathDir, { failPlugin = false } = {}) {
  const logPath = path.join(pathDir, 'claude.invocations.log');
  const scriptPath = path.join(pathDir, 'claude');
  const script = [
    '#!/bin/sh',
    `echo "$@" >> ${JSON.stringify(logPath)}`,
    failPlugin ? 'if [ "$1" = "plugin" ]; then exit 1; fi' : '',
    'exit 0',
  ]
    .filter(Boolean)
    .join('\n');
  await fs.writeFile(scriptPath, script, { mode: 0o755 });
  await fs.chmod(scriptPath, 0o755);
  return {
    async readInvocations() {
      const text = await fs.readFile(logPath, 'utf8').catch(() => '');
      return text.split('\n').filter(Boolean).map((line) => line.split(' '));
    },
  };
}

test('install --only claude-code: registers the MCP server AND the plugin, reports both', async () => {
  await withSandbox(async ({ homeDir, pathDir }) => {
    await seedSession(homeDir);
    const claude = await stubClaudeCli(pathDir);

    const { stdout, exitCode } = await runCli(['install', '--only', 'claude-code', '--yes']);

    assert.equal(exitCode, 0);
    assert.equal(stdout.trim(), `Claude Code: configured (${path.join(homeDir, '.claude.json')}) [plugin: installed]`);

    const invocations = await claude.readInvocations();
    assert.deepEqual(invocations[0], ['mcp', 'add-json', 'landfall', JSON.stringify({ type: 'stdio', command: 'landfall', args: ['serve'] }), '--scope', 'user']);
    assert.deepEqual(invocations[1], ['plugin', 'marketplace', 'add', 'landfalls-ai/landfall-cli', '--scope', 'user']);
    assert.deepEqual(invocations[2], ['plugin', 'install', 'landfall-edge-bridge@landfall', '--scope', 'user']);
  });
});

test('install --only claude-code: a failing plugin step is best-effort and does not fail the MCP registration', async () => {
  await withSandbox(async ({ homeDir, pathDir }) => {
    await seedSession(homeDir);
    await stubClaudeCli(pathDir, { failPlugin: true });

    const { stdout, exitCode } = await runCli(['install', '--only', 'claude-code', '--yes']);

    assert.equal(exitCode, 0);
    assert.equal(stdout.trim(), `Claude Code: configured (${path.join(homeDir, '.claude.json')}) [plugin: skipped]`);
  });
});

test('install --only claude-code when already registered: reports already-installed but still retries the plugin step', async () => {
  await withSandbox(async ({ homeDir, pathDir }) => {
    await seedSession(homeDir);
    // The real `claude mcp add-json` writes this shape into ~/.claude.json;
    // our stub only logs invocations, so pre-seed it directly to simulate a
    // prior successful `landfall install` run.
    await writeJson(path.join(homeDir, '.claude.json'), {
      mcpServers: { landfall: { type: 'stdio', command: 'landfall', args: ['serve'] } },
    });
    const claude = await stubClaudeCli(pathDir);

    const { stdout, exitCode } = await runCli(['install', '--only', 'claude-code', '--yes']);

    assert.equal(exitCode, 0);
    assert.equal(stdout.trim(), `Claude Code: already-installed (${path.join(homeDir, '.claude.json')}) [plugin: installed]`);

    const invocations = await claude.readInvocations();
    // no `mcp add-json` call (nothing to register), but the plugin step
    // still runs — it's idempotent on the claude side, so re-attempting a
    // possibly-earlier-failed plugin install is exactly the point.
    assert.equal(invocations.filter((a) => a[0] === 'mcp').length, 0);
    assert.equal(invocations.filter((a) => a[0] === 'plugin').length, 2);
  });
});
