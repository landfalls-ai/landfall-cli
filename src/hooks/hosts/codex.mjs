// codex.mjs — Codex CLI hook host (#222, story #190).
//
// Two files, because Codex gates hooks behind a feature flag:
//   ~/.codex/hooks.json   the hook entries themselves
//   ~/.codex/config.toml  `codex_hooks = true`, without which none of them run
//
// Registering entries into a file the host is configured to ignore is the
// silent-dead-end failure this whole command exists to avoid, so install does
// both or reports what it could not do.
//
// hooks.json is written in Claude Code's shape (`{hooks: {Stop: [...]}}`)
// because story #190 records Codex's hook surface as modeled on Claude Code's,
// down to `hookSpecificOutput.additionalContext` and exit-2 semantics. That
// nesting is the one thing here taken on the story's word rather than verified
// against Codex itself — the same class of open item research.md R3 already
// carries for `codex mcp remove`. It is safe to be wrong about: every write is
// an append into a list, so a mismatch leaves an inert key rather than
// breaking a config, and correcting it is a one-line change to KEY_PREFIX.
//
// The TOML flag is handled by targeted text edit, not by parsing: this repo
// ships no TOML library on purpose (specs/049 research.md R2/R4), and the
// alternative — writing a parser — is a far larger risk to a user's config
// than inserting one root key.
import path from 'node:path';
import { promises as fs } from 'node:fs';
import {
  planHookInstall,
  applyHookInstall,
  planHookUninstall,
  applyHookUninstall,
} from '../merge.mjs';
import { hookCommand, isLandfallCommand, eventsForHost } from '../spec.mjs';
import { isOnPath, pathExists, homedir } from '../../install/platform.mjs';

export const id = 'codex';
export const displayName = 'Codex CLI';

const EVENT_KEY = { stop: 'Stop' };
const KEY_PREFIX = 'hooks';

const FLAG_RE = /^[ \t]*codex_hooks[ \t]*=[ \t]*(true|false)[ \t]*$/m;
const FLAG_LINE = 'codex_hooks = true';
const TABLE_RE = /^[ \t]*\[/;

export function configPath() {
  return path.join(homedir(), '.codex', 'hooks.json');
}

export function tomlPath() {
  return path.join(homedir(), '.codex', 'config.toml');
}

function registrations() {
  return eventsForHost(id).map((eventId) => ({
    keyPath: `${KEY_PREFIX}.${EVENT_KEY[eventId]}`,
    entry: { hooks: [{ type: 'command', command: hookCommand(eventId) }] },
  }));
}

function isOurs(element) {
  return Array.isArray(element?.hooks) && element.hooks.some((h) => isLandfallCommand(h?.command));
}

export async function detect() {
  return (await isOnPath('codex')) || pathExists(path.join(homedir(), '.codex'));
}

async function readTomlText() {
  try {
    return await fs.readFile(tomlPath(), 'utf8');
  } catch (err) {
    if (err.code === 'ENOENT') return '';
    throw err;
  }
}

/**
 * Insert or flip `codex_hooks = true` as a ROOT key. A root key must appear
 * before the first `[table]` header or TOML would read it as a member of that
 * table, so this inserts at the top rather than appending — appending to a
 * file that ends inside `[some.table]` would silently write the wrong key.
 * Returns 'already-enabled' | 'enabled' | 'flipped'.
 */
export function enableFlagText(text) {
  const lines = text.length ? text.split('\n') : [];
  const firstTable = lines.findIndex((line) => TABLE_RE.test(line));
  const rootEnd = firstTable < 0 ? lines.length : firstTable;

  // Only the ROOT region counts. `codex_hooks` under `[some.table]` is a
  // different key entirely, and treating it as this flag would leave hooks
  // registered but permanently disabled — the exact silent dead end this
  // function exists to prevent.
  const rootLines = lines.slice(0, rootEnd);
  const at = rootLines.findIndex((line) => FLAG_RE.test(line));
  if (at >= 0) {
    if (/true/.test(rootLines[at])) return { text, result: 'already-enabled' };
    const next = [...lines];
    next[at] = FLAG_LINE;
    return { text: next.join('\n'), result: 'flipped' };
  }

  const next = [...lines];
  next.splice(rootEnd, 0, FLAG_LINE, '');
  return { text: next.join('\n'), result: 'enabled' };
}

async function ensureFlag() {
  const text = await readTomlText();
  const { text: next, result } = enableFlagText(text);
  if (result !== 'already-enabled') {
    await fs.mkdir(path.dirname(tomlPath()), { recursive: true });
    await fs.writeFile(tomlPath(), next, 'utf8');
  }
  return result;
}

export async function plan() {
  const result = await planHookInstall(configPath(), registrations(), isOurs);
  const { result: flag } = enableFlagText(await readTomlText());
  return { ...result, flag };
}

export async function install() {
  const result = await applyHookInstall(configPath(), registrations(), isOurs);
  if (result.action === 'conflict') return result;
  const flag = await ensureFlag();
  return {
    ...result,
    // A file that already held our entries but had the flag off was NOT
    // installed in any sense that mattered — say what changed.
    detail:
      flag === 'already-enabled'
        ? undefined
        : `${flag === 'flipped' ? 'flipped' : 'set'} codex_hooks = true in ${tomlPath()}`,
  };
}

/** Non-mutating peek used to build `hooks uninstall`'s candidate list. */
export async function hasEntry() {
  try {
    return (await planHookUninstall(configPath(), registrations(), isOurs)).action !== 'not-installed';
  } catch {
    return true;
  }
}

/**
 * Removes our hook entries and deliberately LEAVES `codex_hooks = true` alone.
 * It is a host-wide feature flag, not a landfall entry — the user may have
 * hooks of their own depending on it, and turning it off would break them
 * silently. "Removes only our entries" is meant literally.
 */
export async function uninstall() {
  return applyHookUninstall(configPath(), registrations(), isOurs);
}
