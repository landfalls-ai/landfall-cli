// cursor.mjs — Cursor hook host (#222, story #190).
//
// Cursor's hook config is its own file with lowercase event names and bare
// `{command}` entries (no `type`, no matcher group). Only `stop` is registered
// here: mapping the rest of our hook scripts onto Cursor's event vocabulary
// and its `{continue:false}` block semantics is #228's job, and guessing at it
// now would put entries in a user's config that nothing yet honors.
import path from 'node:path';
import {
  planHookInstall,
  applyHookInstall,
  planHookUninstall,
  applyHookUninstall,
} from '../merge.mjs';
import { writeJsonPretty } from '../../install/json-merge.mjs';
import { hookCommand, isLandfallCommand, eventsForHost } from '../spec.mjs';
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
    entry: { command: hookCommand(eventId) },
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
