// codex.mjs — Codex CLI adapter (research.md R1).
//
// Codex's config is TOML, and this feature deliberately adds no TOML library
// (research.md R2/R4) — Codex ships its own `codex mcp add`/`codex mcp
// remove` for writes, and idempotency/conflict detection here is a narrow,
// literal match against exactly the `[mcp_servers.landfall]` block landfall
// itself would produce, never a general TOML parse. If Codex's own renderer
// ever formats that block differently than expected, the safe failure mode is
// reporting `conflict`/`left-in-place` rather than guessing — see research.md
// R3 for the open verification items this adapter carries.
import path from 'node:path';
import { promises as fs } from 'node:fs';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { isOnPath, homedir } from '../platform.mjs';

const execFileAsync = promisify(execFile);

export const id = 'codex';
export const displayName = 'Codex CLI';

// Tolerant of the exact quoting/spacing `codex mcp add` is expected to emit;
// intentionally not a general TOML parser (see file header).
const BLOCK_RE = /\[mcp_servers\.landfall\][^[]*/;
const EXPECTED_RE = /command\s*=\s*"landfall"[^[]*args\s*=\s*\[\s*"serve"\s*\]/;

function configPath() {
  return path.join(homedir(), '.codex', 'config.toml');
}

async function readConfigText() {
  try {
    return await fs.readFile(configPath(), 'utf8');
  } catch (err) {
    if (err.code === 'ENOENT') return '';
    throw err;
  }
}

export async function detect() {
  return isOnPath('codex');
}

export async function install() {
  let text;
  try {
    text = await readConfigText();
  } catch (err) {
    return { status: 'failed', detail: err.message, configPath: configPath() };
  }
  const match = text.match(BLOCK_RE);
  if (match) {
    if (EXPECTED_RE.test(match[0])) return { status: 'already-installed', configPath: configPath() };
    return {
      status: 'conflict',
      detail: 'an existing [mcp_servers.landfall] block differs from what this installer would write',
      configPath: configPath(),
    };
  }
  try {
    await execFileAsync('codex', ['mcp', 'add', 'landfall', '--', 'landfall', 'serve']);
  } catch (err) {
    return { status: 'failed', detail: err.message, configPath: configPath() };
  }
  return { status: 'configured', configPath: configPath() };
}

/** Non-mutating peek used by `landfall uninstall` to build its candidate list. */
export async function hasEntry() {
  try {
    return BLOCK_RE.test(await readConfigText());
  } catch {
    return true;
  }
}

export async function uninstall() {
  let text;
  try {
    text = await readConfigText();
  } catch (err) {
    return { status: 'failed', detail: err.message, configPath: configPath() };
  }
  const match = text.match(BLOCK_RE);
  if (!match) return { status: 'not-installed', configPath: configPath() };
  if (!EXPECTED_RE.test(match[0])) return { status: 'left-in-place', configPath: configPath() };

  try {
    await execFileAsync('codex', ['mcp', 'remove', 'landfall']);
    return { status: 'removed', configPath: configPath() };
  } catch {
    // `codex mcp remove` not confirmed to exist (research.md R3) — fall back
    // to deleting only the exact block we just matched, byte-for-byte.
  }
  try {
    const next = text.replace(match[0], '');
    await fs.writeFile(configPath(), next, 'utf8');
    return { status: 'removed', configPath: configPath() };
  } catch (err) {
    return { status: 'failed', detail: err.message, configPath: configPath() };
  }
}
