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
// Four outcomes per registration, and no fifth:
//   present  — an element byte-identical to what we would write is already there
//   outdated — an element byte-identical to what an OLDER version of this
//              installer wrote (registration.superseded): still ours, still
//              unmodified, just stale — upgraded in place, position kept
//   conflict — an element that IS ours by command prefix but matches neither
//              the current nor any past form (a human edited it): never
//              overwritten, never deleted, never duplicated
//   absent   — nothing of ours: append, leaving every other element untouched
//
// `outdated` is what makes changing a registered command survivable (#228).
// Without it the only honest classification for a stale entry is `conflict`,
// and a conflict is by design immovable: install refuses to write and
// uninstall refuses to delete, so an upgrade would leave a dead entry that our
// own tooling cannot clear. Enumerating past forms explicitly — rather than
// treating "starts with landfall hooks but differs" as replaceable — is what
// keeps a hand-edited entry safe, since it matches no enumerated form.
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

/** Elements of `list` written by an older version of this installer. */
function isSuperseded(registration, element) {
  return (registration.superseded ?? []).some((old) => deepEqual(element, old));
}

/**
 * Classify one registration against the config already on disk.
 *
 * `outdated` is tested BEFORE `present`, so a file holding both the current
 * entry and a stale one is still reported as work to do — otherwise the stale
 * entry would survive every future install, firing a command written for a
 * contract we no longer answer.
 *
 * @param {object} data parsed config
 * @param {{keyPath: string, entry: object, superseded?: object[]}} registration
 * @param {(element: object) => boolean} isOurs recognizes a landfall-authored
 *   element regardless of whether it still matches byte-for-byte
 */
export function classify(data, registration, isOurs) {
  const list = listAt(data, registration.keyPath);
  if (list.some((el) => isSuperseded(registration, el))) return 'outdated';
  if (list.some((el) => deepEqual(el, registration.entry))) return 'present';
  if (list.some((el) => isOurs(el))) return 'conflict';
  return 'absent';
}

/**
 * The list with every stale form of `registration` replaced by the current
 * entry, in place — position preserved (a user reading their own config should
 * not find our hook has jumped to the end), duplicates collapsed to one.
 */
function upgradeInPlace(list, registration) {
  const kept = [];
  let placed = false;
  for (const el of list) {
    const stale = isSuperseded(registration, el);
    const current = deepEqual(el, registration.entry);
    if (!stale && !current) {
      kept.push(el);
      continue;
    }
    if (placed) continue; // a second copy of ours, stale or not, is not kept
    kept.push(registration.entry);
    placed = true;
  }
  if (!placed) kept.push(registration.entry);
  return kept;
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
    : per.some((r) => r.state === 'absent' || r.state === 'outdated')
      ? 'write'
      : 'already-installed';
  return { action, data, registrations: per };
}

/**
 * Apply {@link planHookInstall}. Writes ONLY when at least one registration is
 * absent or outdated and none conflicts, and touches only those — a partially
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
    if (r.state === 'absent') {
      next = setPath(next, r.keyPath, [...listAt(next, r.keyPath), r.entry]);
    } else if (r.state === 'outdated') {
      next = setPath(next, r.keyPath, upgradeInPlace(listAt(next, r.keyPath), r));
    }
  }
  await writeJsonPretty(filePath, next);
  return { action: 'configured', data: next, registrations: plan.registrations };
}

/**
 * What an uninstall would do, without writing anything:
 *   remove        — at least one entry of ours is present byte-exact, in its
 *                   current form or in one an older version wrote
 *   left-in-place — only edited-since entries of ours remain
 *   not-installed — nothing of ours anywhere
 */
export async function planHookUninstall(filePath, registrations, isOurs) {
  const data = await readJsonOrEmpty(filePath);
  const per = registrations.map((r) => ({ ...r, state: classify(data, r, isOurs) }));
  const action = per.some((r) => r.state === 'present' || r.state === 'outdated')
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
    if (r.state !== 'present' && r.state !== 'outdated') continue;
    // An entry an older version wrote is still ours to remove — leaving it
    // behind would make "uninstall removed everything landfall added" false.
    const kept = listAt(next, r.keyPath).filter(
      (el) => !deepEqual(el, r.entry) && !isSuperseded(r, el),
    );
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
