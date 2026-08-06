// cursor.mjs — Cursor hook host (#222, story #190; the adapter itself #228).
//
// Cursor's hook config is its own file with lowercase event names and bare
// `{command}` entries (no `type`, no matcher group).
//
// #222 registered `stop` here and left the adapter to #228, on the assumption
// that Cursor's event vocabulary was a rename away from ours. It is not, and
// the entry #222 wrote could not work: Cursor's hook runtime reads a hook's
// verdict as JSON on stdout and never sees an exit code or a line of stderr,
// which is the only way our handler had of saying "do not conclude yet". So
// this file now registers `stop` with `--host cursor`, and the handler answers
// on the channel Cursor actually reads (see ../protocol.mjs). The old bare
// entry is declared superseded in ../spec.mjs so an upgrade replaces it in
// place instead of stranding it as a permanent conflict.
//
// WHAT IS DELIBERATELY NOT REGISTERED HERE: `beforeSubmitPrompt`. It was named
// as Cursor's stand-in for Claude Code's `FileChanged` — Cursor has no
// file-watch hook — but its output schema is `{continue}` alone: it can refuse
// a prompt and it cannot inject context, so it cannot deliver #227's spooled
// digest to the model. What to do instead is a product question (drop it, or
// repurpose it as a gate that refuses the HUMAN's next prompt while unread
// room context exists), and it is open on issue #228. Registering it on a
// guess would put an entry in a user's config that either does nothing or
// silently blocks their typing — the exact mistake #222 avoided.
import path from 'node:path';
import {
  planHookInstall,
  applyHookInstall,
  planHookUninstall,
  applyHookUninstall,
} from '../merge.mjs';
import { writeJsonPretty } from '../../install/json-merge.mjs';
import { hookCommand, isLandfallCommand, eventsForHost, supersededHookCommands } from '../spec.mjs';
import { pathExists, homedir, appBundleCandidates } from '../../install/platform.mjs';

export const id = 'cursor';
export const displayName = 'Cursor';

const EVENT_KEY = { stop: 'stop' };

export function configPath() {
  return path.join(homedir(), '.cursor', 'hooks.json');
}

function registrations() {
  return eventsForHost(id).map((eventId) => ({
    keyPath: `hooks.${EVENT_KEY[eventId]}`,
    entry: { command: hookCommand(eventId, id) },
    superseded: supersededHookCommands(eventId, id).map((command) => ({ command })),
  }));
}

function isOurs(element) {
  return isLandfallCommand(element?.command);
}

export async function detect() {
  for (const candidate of appBundleCandidates('Cursor', 'cursor')) {
    if (await pathExists(candidate)) return true;
  }
  return pathExists(path.join(homedir(), '.cursor'));
}

export async function plan() {
  return planHookInstall(configPath(), registrations(), isOurs);
}

export async function install() {
  const result = await applyHookInstall(configPath(), registrations(), isOurs);
  // Cursor's hook file carries a schema version. Stamp it only on a file we
  // actually wrote, and only when it is absent — never correcting a version a
  // future Cursor (or the user) put there.
  if (result.action === 'configured' && result.data.version === undefined) {
    const next = { version: 1, ...result.data };
    await writeJsonPretty(configPath(), next);
    return { ...result, data: next };
  }
  return result;
}

/** Non-mutating peek used to build `hooks uninstall`'s candidate list. */
export async function hasEntry() {
  try {
    return (await planHookUninstall(configPath(), registrations(), isOurs)).action !== 'not-installed';
  } catch {
    return true;
  }
}

export async function uninstall() {
  return applyHookUninstall(configPath(), registrations(), isOurs);
}
