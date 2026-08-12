// hosts.test.mjs — per-host config shape (#222): what each host is actually
// handed, since "passes that host's validation" is the acceptance criterion
// and each of the three has a different format.
import test from 'node:test';
import assert from 'node:assert/strict';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import { withSandbox, runCli, readJson } from '../install/helpers.mjs';
import { enableFlagText } from '../../src/hooks/hosts/codex.mjs';

test('Claude Code: matcher-group shape, every event, under hooks.<Event>', async () => {
  await withSandbox(async ({ homeDir }) => {
    await fs.mkdir(path.join(homeDir, '.claude'), { recursive: true });
    await runCli(['hooks', 'install', '--only', 'claude-code']);

    assert.deepEqual(await readJson(path.join(homeDir, '.claude', 'settings.json')), {
      hooks: {
        Stop: [{ hooks: [{ type: 'command', command: 'landfall hooks stop' }] }],
        // FileChanged carries a matcher and Stop does not, deliberately (#227).
        // For FileChanged the matcher is REQUIRED, not a refinement: Claude
        // Code watches a list of literal filenames, and an empty or omitted
        // matcher watches nothing and never fires — which is what #222's
        // matcher-less registration did. A path or glob is equally inert.
        FileChanged: [
          {
            matcher: 'room_events',
            hooks: [{ type: 'command', command: 'landfall hooks file-changed' }],
          },
        ],
        // The delivery half (#227). No matcher — the event does not support one
        // and always fires, which is precisely why it is the half that speaks:
        // it is guaranteed to run when an idle session resumes.
        UserPromptSubmit: [{ hooks: [{ type: 'command', command: 'landfall hooks user-prompt-submit' }] }],
        // PreToolUse is the one event carrying a matcher for a different reason
        // than FileChanged: #233 requires a non-matching command to add zero
        // overhead, and scoping the registration to the shell tool is what
        // stops an Edit or a Read from spawning a process at all.
        PreToolUse: [
          { matcher: 'Bash', hooks: [{ type: 'command', command: 'landfall hooks pre-tool-use' }] },
        ],
      },
      // Feature 20260812-010632 (US5/T048): a separate config surface in the
      // same file — Claude Code's statusLine, one key holding one object, not
      // a hooks.<Event> list. Only Claude Code gets this (Cursor/Codex have no
      // statusLine concept), so it appears only in this test, not the two below.
      statusLine: { type: 'command', command: 'landfall status' },
    });
  });
});

test('Cursor: lowercase event name, bare {command} entry, version stamped', async () => {
  await withSandbox(async ({ homeDir }) => {
    await fs.mkdir(path.join(homeDir, '.cursor'), { recursive: true });
    await runCli(['hooks', 'install', '--only', 'cursor']);

    // `--host cursor` is what tells the handler to answer in JSON on stdout
    // rather than exit 2 + stderr, which Cursor never reads (#228).
    assert.deepEqual(await readJson(path.join(homeDir, '.cursor', 'hooks.json')), {
      version: 1,
      hooks: { stop: [{ command: 'landfall hooks stop --host cursor' }] },
    });
  });
});

test('Claude Code and Codex keep the bare command — only a host needing another output shape is flagged', async () => {
  await withSandbox(async ({ homeDir }) => {
    await fs.mkdir(path.join(homeDir, '.claude'), { recursive: true });
    await fs.mkdir(path.join(homeDir, '.codex'), { recursive: true });
    await runCli(['hooks', 'install', '--only', 'claude-code,codex']);

    const claude = await readJson(path.join(homeDir, '.claude', 'settings.json'));
    const codex = await readJson(path.join(homeDir, '.codex', 'hooks.json'));
    for (const command of [
      claude.hooks.Stop[0].hooks[0].command,
      claude.hooks.FileChanged[0].hooks[0].command,
      codex.hooks.Stop[0].hooks[0].command,
    ]) {
      assert.equal(command.includes('--host'), false, `${command} must stay byte-identical across the #228 upgrade`);
    }
  });
});

test('Cursor: FileChanged is not registered — it is a Claude Code event (#227)', async () => {
  await withSandbox(async ({ homeDir }) => {
    await fs.mkdir(path.join(homeDir, '.cursor'), { recursive: true });
    await runCli(['hooks', 'install', '--only', 'cursor']);
    const config = await readJson(path.join(homeDir, '.cursor', 'hooks.json'));
    assert.deepEqual(Object.keys(config.hooks), ['stop']);
  });
});

test('Cursor: an existing version is never rewritten', async () => {
  await withSandbox(async ({ homeDir }) => {
    const config = path.join(homeDir, '.cursor', 'hooks.json');
    await fs.mkdir(path.dirname(config), { recursive: true });
    await fs.writeFile(config, JSON.stringify({ version: 2 }) + '\n');
    await runCli(['hooks', 'install', '--only', 'cursor']);
    assert.equal((await readJson(config)).version, 2);
  });
});

test('Codex: hooks.json written AND codex_hooks flipped on in config.toml', async () => {
  await withSandbox(async ({ homeDir }) => {
    await fs.mkdir(path.join(homeDir, '.codex'), { recursive: true });
    const toml = path.join(homeDir, '.codex', 'config.toml');
    await fs.writeFile(toml, 'model = "gpt-5"\n\n[mcp_servers.other]\ncommand = "x"\n');

    const { stdout } = await runCli(['hooks', 'install', '--only', 'codex']);
    assert.match(stdout, /^Codex CLI: configured — set codex_hooks = true in /m);

    assert.deepEqual(await readJson(path.join(homeDir, '.codex', 'hooks.json')), {
      hooks: {
        Stop: [{ hooks: [{ type: 'command', command: 'landfall hooks stop' }] }],
        PreToolUse: [
          { matcher: 'Bash', hooks: [{ type: 'command', command: 'landfall hooks pre-tool-use' }] },
        ],
      },
    });

    const text = await fs.readFile(toml, 'utf8');
    assert.match(text, /^codex_hooks = true$/m);
    assert.ok(
      text.indexOf('codex_hooks') < text.indexOf('[mcp_servers.other]'),
      'a root key must precede the first table or TOML reads it as a member of that table',
    );
    assert.match(text, /model = "gpt-5"/, 'existing config preserved');
  });
});

test('Codex: uninstall removes our hook entry and deliberately leaves codex_hooks alone', async () => {
  await withSandbox(async ({ homeDir }) => {
    await fs.mkdir(path.join(homeDir, '.codex'), { recursive: true });
    await runCli(['hooks', 'install', '--only', 'codex']);
    await runCli(['hooks', 'uninstall', '--only', 'codex']);

    assert.deepEqual(await readJson(path.join(homeDir, '.codex', 'hooks.json')), {});
    // The flag is a host-wide switch other hooks may depend on — "removes only
    // our entries" is meant literally.
    assert.match(await fs.readFile(path.join(homeDir, '.codex', 'config.toml'), 'utf8'), /codex_hooks = true/);
  });
});

test('Codex: entries already present but the flag off is still reported as work done', async () => {
  await withSandbox(async ({ homeDir }) => {
    await fs.mkdir(path.join(homeDir, '.codex'), { recursive: true });
    const toml = path.join(homeDir, '.codex', 'config.toml');
    await runCli(['hooks', 'install', '--only', 'codex']);
    await fs.writeFile(toml, 'codex_hooks = false\n');

    const { stdout } = await runCli(['hooks', 'install', '--only', 'codex']);
    assert.match(stdout, /^Codex CLI: already-installed — flipped codex_hooks = true/m);
    assert.match(await fs.readFile(toml, 'utf8'), /^codex_hooks = true$/m);
  });
});

test('enableFlagText: placement and idempotency, without touching a disk', () => {
  assert.equal(enableFlagText('codex_hooks = true\n').result, 'already-enabled');
  assert.equal(enableFlagText('codex_hooks = false\n').text, 'codex_hooks = true\n');
  assert.equal(enableFlagText('').text.trim(), 'codex_hooks = true');

  // The case that makes this a text edit rather than an append: a file ending
  // inside a table. Appending would define [tbl].codex_hooks, not the root key.
  const { text } = enableFlagText('[tbl]\nk = 1\n');
  assert.equal(text, 'codex_hooks = true\n\n[tbl]\nk = 1\n');

  // A same-named key INSIDE a table is a different key and must not be read as
  // the root flag being present — otherwise hooks end up registered but
  // permanently disabled.
  const shadowed = enableFlagText('[tbl]\ncodex_hooks = true\n');
  assert.equal(shadowed.result, 'enabled');
  assert.equal(shadowed.text, 'codex_hooks = true\n\n[tbl]\ncodex_hooks = true\n');
});
