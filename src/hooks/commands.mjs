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
import { promises as fs } from 'node:fs';
import path from 'node:path';
import { HOOK_HOSTS as DEFAULT_HOSTS } from './hosts.mjs';
import { exitCodeForOutcomes } from '../install/report.mjs';
import { isOnPath as defaultIsOnPath } from '../install/platform.mjs';
import { loadPolicy, policyPath, starterPolicy, intentFor } from './policy.mjs';

/** Parse `hooks install`/`hooks uninstall` flags: `--only <ids>`, `--dry-run`, `--uninstall`. */
export function parseHookFlags(rest) {
  const args = [...rest];
  const takeFlag = (name) => {
    const i = args.indexOf(name);
    if (i < 0) return false;
    args.splice(i, 1);
    return true;
  };
  const dryRun = takeFlag('--dry-run');
  const uninstall = takeFlag('--uninstall');
  let only = null;
  const oi = args.indexOf('--only');
  if (oi >= 0) {
    only = (args[oi + 1] ?? '')
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);
    args.splice(oi, 2);
  }
  return { dryRun, uninstall, only, rest: args };
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

/**
 * `landfall hooks policy [--init]` — print the production allow-list and,
 * for every rule, the EXACT request body it would send.
 *
 * This command is not a convenience. #233's acceptance criterion is that "the
 * engineer can read exactly what leaves the machine", and a policy file plus a
 * promise about how it is interpreted does not satisfy that — the interpreter
 * has to show its work. Because a rule's payload is built only from literals in
 * the rule (see policy.mjs), what this prints IS the complete set of values
 * this machine can ever send, with no caveats about what a matched command
 * might add.
 *
 * `--init` writes a documented starter file, and refuses to touch an existing
 * one: overwriting a policy someone tuned would be the worst possible outcome
 * of a command that reads like an explanation.
 */
export async function runHooksPolicy(rest, deps = {}) {
  const { load = loadPolicy, filePath = policyPath(), now = new Date(0) } = deps;
  const lines = [];
  const init = rest.includes('--init');

  if (init) {
    if (await fs.access(filePath).then(() => true, () => false)) {
      lines.push(`policy already exists: ${filePath} (not overwritten)`);
    } else {
      await fs.mkdir(path.dirname(filePath), { recursive: true });
      await fs.writeFile(filePath, JSON.stringify(starterPolicy(), null, 2) + '\n', 'utf8');
      lines.push(`wrote starter policy: ${filePath}`);
      lines.push('edit it, then re-run `landfall hooks policy` to see what each rule would send.');
      return { lines, exitCode: 0 };
    }
  }

  const { exists, rules, errors } = await load(filePath);
  lines.push(`policy: ${filePath}`);
  if (!exists) {
    lines.push('  (no policy file — no command on this machine is reported; `--init` writes a starter)');
    return { lines, exitCode: 0 };
  }
  for (const error of errors) lines.push(`  ! ${error}`);
  if (!rules.length) {
    lines.push('  (no usable rules — nothing is reported)');
    return { lines, exitCode: errors.length ? 1 : 0 };
  }

  for (const rule of rules) {
    lines.push('');
    lines.push(`  ${rule.id}${rule.description ? ` — ${rule.description}` : ''}`);
    lines.push(`    matches: ${rule.command}${rule.allOf.length ? ` containing all of ${rule.allOf.map((s) => JSON.stringify(s)).join(', ')}` : ' (any invocation)'}`);
    if (rule.noneOf.length) {
      lines.push(`    unless it contains any of ${rule.noneOf.map((s) => JSON.stringify(s)).join(', ')}`);
    }
    lines.push(`    sends: ${JSON.stringify({ ...intentFor(rule, now), startedAt: '<when you confirm>' })}`);
  }
  lines.push('');
  lines.push('Nothing else is sent. The command line itself never leaves this machine.');
  return { lines, exitCode: errors.length ? 1 : 0 };
}
