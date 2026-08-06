// commands.mjs — the `landfall hooks install` / `landfall hooks uninstall`
// command bodies (#222), factored out of bin/landfall.mjs the same way
// ../install/commands.mjs is: every external effect is an injectable
// dependency, neither function calls process.exit, and both return
// `{ outcomes, exitCode }` or `{ usageError, exitCode }` for the CLI entry
// point to print.
//
// Two deliberate differences from `landfall install`:
//
//  - No sign-in gate. `landfall install` gates on a session because MCP
//    registration is about reaching a war room. A hook talks to the local
//    bridge daemon over the machine's own socket; requiring a browser round
//    trip to edit a local config file would be friction with nothing behind
//    it, and would make the command unusable from a fleet provisioning script
//    — which story #190 names as the point of shipping it as config.
//
//  - No interactive selection prompt. `hooks install` configures every host it
//    detects; `--only` narrows it. The multi-select checklist exists for MCP
//    registration because registering an agent it into a war room is a choice
//    per agent. Hooks are the enhancement tier for hosts already registered.
import { HOOK_HOSTS as DEFAULT_HOSTS } from './hosts.mjs';
import { exitCodeForOutcomes } from '../install/report.mjs';
import { isOnPath as defaultIsOnPath } from '../install/platform.mjs';

/**
 * Parse `landfall hooks` flags: `--only <ids>`, `--dry-run`, `--uninstall`, and
 * `--host <id>` — the last of which belongs to a hook INVOCATION rather than to
 * install/uninstall, and names the host whose output contract the handler must
 * answer on (#228). Parsed here with the rest so `rest[0]` stays the
 * subcommand wherever the flag appears.
 */
export function parseHookFlags(rest) {
  const args = [...rest];
  const takeValue = (name) => {
    const i = args.indexOf(name);
    if (i < 0) return null;
    const value = args[i + 1] ?? null;
    args.splice(i, value === null ? 1 : 2);
    return value;
  };
  const takeFlag = (name) => {
    const i = args.indexOf(name);
    if (i < 0) return false;
    args.splice(i, 1);
    return true;
  };
  const dryRun = takeFlag('--dry-run');
  const uninstall = takeFlag('--uninstall');
  const host = takeValue('--host');
  let only = null;
  const oi = args.indexOf('--only');
  if (oi >= 0) {
    only = (args[oi + 1] ?? '')
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);
    args.splice(oi, 2);
  }
  return { dryRun, uninstall, only, host, rest: args };
}

function resolveOnly(hosts, only) {
  if (!only) return { candidates: hosts, unknown: [] };
  const unknown = only.filter((id) => !hosts.some((h) => h.id === id));
  return { candidates: hosts.filter((h) => only.includes(h.id)), unknown };
}

/** Map a merge-core action onto the report vocabulary shared with `landfall install`. */
function installStatus(action) {
  return action === 'configured' ? 'configured' : action === 'conflict' ? 'conflict' : 'already-installed';
}

const CONFLICT_DETAIL =
  'an existing landfall hook entry differs from what this installer would write — left untouched';

/** `landfall hooks install`. */
export async function runHooksInstall(rest, deps = {}) {
  const { log = () => {}, hosts = DEFAULT_HOSTS, isOnPathFn = defaultIsOnPath } = deps;
  const { dryRun, only } = parseHookFlags(rest);
  const { candidates, unknown } = resolveOnly(hosts, only);
  if (unknown.length) {
    return { usageError: `unknown hook host id(s): ${unknown.join(', ')}`, exitCode: 2, outcomes: [] };
  }

  const outcomes = [];
  for (const h of candidates) {
    if (!(await h.detect())) {
      outcomes.push({ displayName: h.displayName, status: 'not-detected' });
      continue;
    }
    try {
      const result = dryRun ? await h.plan() : await h.install();
      const status = dryRun
        ? result.action === 'write'
          ? 'would-configure'
          : installStatus(result.action)
        : installStatus(result.action);
      outcomes.push({
        displayName: h.displayName,
        status,
        detail: result.action === 'conflict' ? CONFLICT_DETAIL : result.detail,
        configPath: h.configPath(),
      });
    } catch (err) {
      outcomes.push({
        displayName: h.displayName,
        status: 'failed',
        detail: err.message,
        configPath: h.configPath(),
      });
    }
  }

  // Same warning `landfall install` raises, for the same reason: a hook whose
  // command does not resolve fires on every turn and fails on every turn.
  if (!dryRun && outcomes.some((o) => o.status === 'configured') && !(await isOnPathFn('landfall'))) {
    log('warning: `landfall` is not resolvable on PATH from this shell — the hooks just registered will fail until it is.');
  }

  return { outcomes, exitCode: exitCodeForOutcomes(outcomes) };
}

/** `landfall hooks uninstall` (also reachable as `landfall hooks install --uninstall`). */
export async function runHooksUninstall(rest, deps = {}) {
  const { hosts = DEFAULT_HOSTS } = deps;
  const { only } = parseHookFlags(rest);
  const { candidates, unknown } = resolveOnly(hosts, only);
  if (unknown.length) {
    return { usageError: `unknown hook host id(s): ${unknown.join(', ')}`, exitCode: 2, outcomes: [] };
  }

  const outcomes = [];
  for (const h of candidates) {
    try {
      if (!(await h.hasEntry())) {
        outcomes.push({ displayName: h.displayName, status: 'not-installed', configPath: h.configPath() });
        continue;
      }
      const result = await h.uninstall();
      outcomes.push({
        displayName: h.displayName,
        status: result.action === 'removed' ? 'removed' : result.action === 'not-installed' ? 'not-installed' : 'left-in-place',
        configPath: h.configPath(),
      });
    } catch (err) {
      outcomes.push({
        displayName: h.displayName,
        status: 'failed',
        detail: err.message,
        configPath: h.configPath(),
      });
    }
  }

  return { outcomes, exitCode: exitCodeForOutcomes(outcomes) };
}
