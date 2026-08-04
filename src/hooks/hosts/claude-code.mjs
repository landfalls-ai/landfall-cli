// claude-code.mjs — Claude Code hook host (#222, story #190).
//
// Unlike MCP registration, Claude Code ships no `claude hooks add` CLI, so
// hook entries go through the shared non-destructive merge against the user's
// own settings file. That file holds far more than hooks, which is exactly
// why every write here is an append into one list at one key path.
import path from 'node:path';
import {
  planHookInstall,
  applyHookInstall,
  planHookUninstall,
  applyHookUninstall,
} from '../merge.mjs';
import { hookCommand, isLandfallCommand, eventsForHost, matcherFor } from '../spec.mjs';
import { isOnPath, pathExists, homedir } from '../../install/platform.mjs';

export const id = 'claude-code';
export const displayName = 'Claude Code';

// Claude Code's hook shape: each event holds a list of matcher groups, each
// group a list of `{type: 'command', command}` hooks. `matcher` selects tool
// names, so it is omitted for Stop and FileChanged (which have no tool) and
// carried for PreToolUse (where narrowing to the shell tool is what keeps a
// non-matching command free — see spec.mjs).
const EVENT_KEY = { stop: 'Stop', 'file-changed': 'FileChanged', 'pre-tool-use': 'PreToolUse' };

export function configPath() {
  return path.join(homedir(), '.claude', 'settings.json');
}

function registrations() {
  return eventsForHost(id).map((eventId) => {
    const matcher = matcherFor(eventId);
    return {
      keyPath: `hooks.${EVENT_KEY[eventId]}`,
      entry: {
        ...(matcher ? { matcher } : {}),
        hooks: [{ type: 'command', command: hookCommand(eventId) }],
      },
    };
  });
}

function isOurs(element) {
  return Array.isArray(element?.hooks) && element.hooks.some((h) => isLandfallCommand(h?.command));
}

export async function detect() {
  return (await isOnPath('claude')) || pathExists(path.join(homedir(), '.claude'));
}

export async function plan() {
  return planHookInstall(configPath(), registrations(), isOurs);
}

export async function install() {
  return applyHookInstall(configPath(), registrations(), isOurs);
}

/** Non-mutating peek used to build `hooks uninstall`'s candidate list. */
export async function hasEntry() {
  try {
    return (await planHookUninstall(configPath(), registrations(), isOurs)).action !== 'not-installed';
  } catch {
    return true; // unreadable/unparseable — surface it rather than hide it
  }
}

export async function uninstall() {
  return applyHookUninstall(configPath(), registrations(), isOurs);
}
