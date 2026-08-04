// merge.mjs — the non-destructive merge core for hook config, and the one
// place the install/uninstall decision is made.
//
// Why this exists next to ../install/json-merge.mjs rather than inside it:
// MCP registration is ONE key holding ONE object, so "is it ours?" is a whole-
// value comparison. A hook config is a LIST per event that the user writes
// into too — a plain set/delete at a key path would take their hooks with it.
// So the unit here is one element of an array, and every operation preserves
// every element it did not put there.
//
// Three outcomes per registration, and no fourth:
//   present  — an element byte-identical to what we would write is already there
//   conflict — an element that IS ours by command prefix but differs (a human
//              edited it): never overwritten, never deleted, never duplicated
//   absent   — nothing of ours: append, leaving every other element untouched
import {
  readJsonOrEmpty,
  writeJsonPretty,
  getPath,
  setPath,
  deletePath,
} from '../install/json-merge.mjs';

function deepEqual(a, b) {
  return JSON.stringify(a) === JSON.stringify(b);
}

function listAt(data, keyPath) {
  const value = getPath(data, keyPath);
  return Array.isArray(value) ? value : [];
}

/**
 * Classify one registration against the config already on disk.
 * @param {object} data parsed config
 * @param {{keyPath: string, entry: object}} registration
 * @param {(element: object) => boolean} isOurs recognizes a landfall-authored
 *   element regardless of whether it still matches byte-for-byte
 */
export function classify(data, registration, isOurs) {
  const list = listAt(data, registration.keyPath);
  if (list.some((el) => deepEqual(el, registration.entry))) return 'present';
  if (list.some((el) => isOurs(el))) return 'conflict';
  return 'absent';
}

/**
 * What an install would do to `filePath`, without writing anything.
 * The file-level action is the worst case across its registrations: any
 * conflict wins, then any write, else already-installed.
 */
export async function planHookInstall(filePath, registrations, isOurs) {
  const data = await readJsonOrEmpty(filePath);
  const per = registrations.map((r) => ({ ...r, state: classify(data, r, isOurs) }));
  const action = per.some((r) => r.state === 'conflict')
    ? 'conflict'
    : per.some((r) => r.state === 'absent')
      ? 'write'
      : 'already-installed';
  return { action, data, registrations: per };
}

/**
 * Apply {@link planHookInstall}. Writes ONLY when at least one registration is
 * absent and none conflicts, and appends only the absent ones — a partially
 * installed file gains exactly what it was missing. A conflict writes nothing
 * at all, including for the registrations that would have been fine: a file
 * whose Stop entry a human rewrote is a file to leave alone and report on,
 * not one to half-update.
 */
export async function applyHookInstall(filePath, registrations, isOurs) {
  const plan = await planHookInstall(filePath, registrations, isOurs);
  if (plan.action !== 'write') return plan;
  let next = plan.data;
  for (const r of plan.registrations) {
    if (r.state !== 'absent') continue;
    next = setPath(next, r.keyPath, [...listAt(next, r.keyPath), r.entry]);
  }
  await writeJsonPretty(filePath, next);
  return { action: 'configured', data: next, registrations: plan.registrations };
}

/**
 * What an uninstall would do, without writing anything:
 *   remove        — at least one byte-exact entry of ours is present
 *   left-in-place — only edited-since entries of ours remain
 *   not-installed — nothing of ours anywhere
 */
export async function planHookUninstall(filePath, registrations, isOurs) {
  const data = await readJsonOrEmpty(filePath);
  const per = registrations.map((r) => ({ ...r, state: classify(data, r, isOurs) }));
  const action = per.some((r) => r.state === 'present')
    ? 'remove'
    : per.some((r) => r.state === 'conflict')
      ? 'left-in-place'
      : 'not-installed';
  return { action, data, registrations: per };
}

/**
 * Apply {@link planHookUninstall}: drops exactly the elements equal to what we
 * wrote and nothing else. An event list, or the container holding it, that we
 * have just emptied is pruned so an install→uninstall round trip leaves the
 * file as it was found rather than littered with `"Stop": []`. A list that
 * still holds someone else's hook is of course kept.
 */
export async function applyHookUninstall(filePath, registrations, isOurs) {
  const plan = await planHookUninstall(filePath, registrations, isOurs);
  if (plan.action !== 'remove') return plan;
  let next = plan.data;
  for (const r of plan.registrations) {
    if (r.state !== 'present') continue;
    const kept = listAt(next, r.keyPath).filter((el) => !deepEqual(el, r.entry));
    next = kept.length ? setPath(next, r.keyPath, kept) : deletePath(next, r.keyPath);
  }
  for (const parent of pruneCandidates(plan.registrations)) {
    const value = getPath(next, parent);
    if (value && typeof value === 'object' && Object.keys(value).length === 0) {
      next = deletePath(next, parent);
    }
  }
  await writeJsonPretty(filePath, next);
  return { action: 'removed', data: next, registrations: plan.registrations };
}

/** Parent key paths ("hooks" for "hooks.Stop") worth pruning if left empty. */
function pruneCandidates(registrations) {
  const parents = new Set();
  for (const r of registrations) {
    const keys = r.keyPath.split('.');
    if (keys.length > 1) parents.add(keys.slice(0, -1).join('.'));
  }
  return [...parents];
}
