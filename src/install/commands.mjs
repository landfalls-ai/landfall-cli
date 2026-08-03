// commands.mjs — the `landfall install` / `landfall uninstall` command bodies
// (contracts/cli.md), factored out of bin/landfall.mjs so they're directly
// unit-testable: every external effect (login, the harness registry, the
// prompt, PATH probing) is an injectable dependency defaulting to the real
// implementation. Neither function calls process.exit — they return
// `{ outcomes, exitCode }` or `{ usageError, exitCode }` and the CLI entry
// point is the only place that prints and exits.
import { HARNESSES as DEFAULT_HARNESSES } from './harnesses.mjs';
import { promptSelection as defaultPromptSelection } from './prompt.mjs';
import { exitCodeForOutcomes } from './report.mjs';
import { isOnPath as defaultIsOnPath } from './platform.mjs';
import { login as defaultLogin, getCachedAccessToken as defaultGetCachedAccessToken } from '../auth.mjs';

/**
 * Parse `install`/`uninstall`'s own flags out of the remaining argv, per
 * contracts/cli.md: `--yes`, `--only <ids>` (comma-separated), `--dry-run`
 * (install only; harmless if present for uninstall since nothing reads it).
 */
export function parseInstallFlags(rest) {
  const args = [...rest];
  const takeFlag = (name) => {
    const i = args.indexOf(name);
    if (i < 0) return false;
    args.splice(i, 1);
    return true;
  };
  const yes = takeFlag('--yes');
  const dryRun = takeFlag('--dry-run');
  let only = null;
  const oi = args.indexOf('--only');
  if (oi >= 0) {
    only = (args[oi + 1] ?? '')
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);
    args.splice(oi, 2);
  }
  return { yes, dryRun, only };
}

function resolveOnly(harnesses, only) {
  if (!only) return { candidates: harnesses, unknown: [] };
  const unknown = only.filter((id) => !harnesses.some((h) => h.id === id));
  return { candidates: harnesses.filter((h) => only.includes(h.id)), unknown };
}

/** `landfall install` (FR-001 through FR-013). See contracts/cli.md. */
export async function runInstall(rest, deps = {}) {
  const {
    log = () => {},
    harnesses = DEFAULT_HARNESSES,
    login = defaultLogin,
    getCachedAccessToken = defaultGetCachedAccessToken,
    isOnPathFn = defaultIsOnPath,
    promptSelectionFn = defaultPromptSelection,
  } = deps;

  const { yes, dryRun, only } = parseInstallFlags(rest);
  const { candidates, unknown } = resolveOnly(harnesses, only);
  if (unknown.length) {
    return { usageError: `unknown harness id(s): ${unknown.join(', ')}`, exitCode: 2, outcomes: [] };
  }

  // FR-001/FR-009: gate on sign-in before any harness is touched.
  let token = await getCachedAccessToken();
  if (!token) {
    log('signing in — landfall install registers your own machine, so it needs your session first.');
    await login(log).catch((e) => log(`sign-in failed: ${e.message}`));
    token = await getCachedAccessToken();
  }
  if (!token) {
    return { usageError: 'sign-in did not complete — aborting.', exitCode: 2, outcomes: [] };
  }

  const detected = [];
  for (const h of candidates) {
    if (await h.detect()) detected.push(h);
  }

  const selectedIds =
    detected.length === 0
      ? []
      : await promptSelectionFn(
          detected.map((h) => ({ id: h.id, label: h.displayName })),
          { skipPrompt: yes },
        );

  const outcomes = [];
  for (const h of candidates) {
    if (!detected.includes(h)) {
      outcomes.push({ displayName: h.displayName, status: 'not-detected' });
    } else if (!selectedIds.includes(h.id)) {
      outcomes.push({ displayName: h.displayName, status: 'skipped' });
    } else if (dryRun) {
      outcomes.push({ displayName: h.displayName, status: 'would-configure' });
    } else {
      outcomes.push({ displayName: h.displayName, ...(await h.install()) });
    }
  }

  // FR-002 / research.md R5: a harness registered with a `landfall` command
  // that never resolves is a silent dead end — warn as early as possible.
  if (!dryRun && outcomes.some((o) => o.status === 'configured') && !(await isOnPathFn('landfall'))) {
    log('warning: `landfall` is not resolvable on PATH from this shell — a harness launching it may fail until it is.');
  }

  return { outcomes, exitCode: exitCodeForOutcomes(outcomes) };
}

/** `landfall uninstall`. See contracts/cli.md. */
export async function runUninstall(rest, deps = {}) {
  const {
    harnesses = DEFAULT_HARNESSES,
    promptSelectionFn = defaultPromptSelection,
  } = deps;

  const { yes, only } = parseInstallFlags(rest);
  const { candidates, unknown } = resolveOnly(harnesses, only);
  if (unknown.length) {
    return { usageError: `unknown harness id(s): ${unknown.join(', ')}`, exitCode: 2, outcomes: [] };
  }

  // No sign-in required — removing a local file registration isn't an
  // authenticated action (contracts/cli.md).
  const withEntry = [];
  for (const h of candidates) {
    if (await h.hasEntry()) withEntry.push(h);
  }

  const selectedIds =
    withEntry.length === 0
      ? []
      : await promptSelectionFn(
          withEntry.map((h) => ({ id: h.id, label: h.displayName })),
          { skipPrompt: yes },
        );

  const outcomes = [];
  for (const h of candidates) {
    if (!withEntry.includes(h)) {
      outcomes.push({ displayName: h.displayName, status: 'not-installed' });
    } else if (!selectedIds.includes(h.id)) {
      outcomes.push({ displayName: h.displayName, status: 'skipped' });
    } else {
      outcomes.push({ displayName: h.displayName, ...(await h.uninstall()) });
    }
  }

  return { outcomes, exitCode: exitCodeForOutcomes(outcomes) };
}
