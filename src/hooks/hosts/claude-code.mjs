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
import { planInstall, applyInstall, planUninstall, applyUninstall } from '../../install/json-merge.mjs';

// Feature 20260812-010632 (US5/T048): `landfall status`'s statusLine
// registration. A SEPARATE config surface from the `hooks.*` lists above —
// Claude Code's `statusLine` key holds ONE object, not a per-event list — so
// it goes through ../../install/json-merge.mjs's single-key merge (the same
// primitive every direct-JSON MCP-registration adapter already uses, per
// that file's own header), not ../merge.mjs's list machinery. Both surfaces
// live in the same settings.json but are otherwise independent: a customer
// can reasonably want hooks without a statusline or vice versa, so neither
// blocks the other, and each is its own non-destructive read-modify-write —
// sequential, not simultaneous, so the second call always sees what the
// first one wrote.
export const STATUS_LINE_KEY = 'statusLine';
export const STATUS_LINE_ENTRY = Object.freeze({ type: 'command', command: 'landfall status' });

/** Worst-case-wins across two sub-operations' actions, same precedence
 * `planHookInstall`/`applyHookInstall` already use internally across
 * multiple registrations, generalized to combine two INDEPENDENT surfaces
 * (hooks, statusLine) into the one action `hooks install`/`uninstall`'s
 * per-host report expects. */
function combine(actions, order) {
  return actions.reduce((worst, a) => (order.indexOf(a) > order.indexOf(worst) ? a : worst));
}
const INSTALL_ORDER = ['already-installed', 'write', 'configured', 'conflict'];
const UNINSTALL_ORDER = ['not-installed', 'left-in-place', 'remove', 'removed'];

export const id = 'claude-code';
export const displayName = 'Claude Code';

// Claude Code's hook shape: each event holds a list of matcher groups, each
// group a list of `{type: 'command', command}` hooks. `matcher` is omitted for
// Stop and UserPromptSubmit, neither of which supports one; carried for
// FileChanged, where it is not a refinement but the whole watch list, since an
// absent matcher there watches nothing at all; and carried for PreToolUse,
// where narrowing to the shell tool is what keeps a non-matching command free
// — see spec.mjs.
const EVENT_KEY = {
  stop: 'Stop',
  'file-changed': 'FileChanged',
  'user-prompt-submit': 'UserPromptSubmit',
  'pre-tool-use': 'PreToolUse',
};

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
        hooks: [{ type: 'command', command: hookCommand(eventId, id) }],
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
  const hooks = await planHookInstall(configPath(), registrations(), isOurs);
  const statusLine = await planInstall(configPath(), STATUS_LINE_KEY, STATUS_LINE_ENTRY);
  return { action: combine([hooks.action, statusLine.action], INSTALL_ORDER), hooks, statusLine };
}

export async function install() {
  // ATOMIC across both surfaces, on purpose — matching the existing,
  // already-tested guarantee for hooks.* alone ("not one byte written" on a
  // conflict, idempotent-and-nondestructive.test.mjs). Install and uninstall
  // are NOT symmetric here: applyHookUninstall already removes what it safely
  // can and leaves what's hand-edited (selective, per-registration), but
  // install has always been all-or-nothing — a conflict is a report, not a
  // partial write. Extending "one surface's conflict must not block the
  // other's write" to install would have been a NEW, weaker guarantee than
  // hooks.* alone already gives today; check the combined plan first and
  // touch the file at all only when nothing conflicts.
  const combined = await plan();
  if (combined.action === 'conflict') return combined;
  const hooks = await applyHookInstall(configPath(), registrations(), isOurs);
  const statusLine = await applyInstall(configPath(), STATUS_LINE_KEY, STATUS_LINE_ENTRY);
  return { action: combine([hooks.action, statusLine.action], INSTALL_ORDER), hooks, statusLine };
}

/** Non-mutating peek used to build `hooks uninstall`'s candidate list. */
export async function hasEntry() {
  try {
    const hooksInstalled = (await planHookUninstall(configPath(), registrations(), isOurs)).action !== 'not-installed';
    const statusLineInstalled = (await planUninstall(configPath(), STATUS_LINE_KEY, STATUS_LINE_ENTRY)).action !== 'not-installed';
    return hooksInstalled || statusLineInstalled;
  } catch {
    return true; // unreadable/unparseable — surface it rather than hide it
  }
}

export async function uninstall() {
  const hooks = await applyHookUninstall(configPath(), registrations(), isOurs);
  const statusLine = await applyUninstall(configPath(), STATUS_LINE_KEY, STATUS_LINE_ENTRY);
  return { action: combine([hooks.action, statusLine.action], UNINSTALL_ORDER), hooks, statusLine };
}
