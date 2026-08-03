// json-merge.mjs — the one non-destructive JSON merge/idempotency/conflict
// implementation shared by every direct-JSON-editing harness adapter
// (Cursor, VS Code's fallback, Claude Desktop, Windsurf). See
// specs/049-cli-mcp-harness-installer/research.md R2 for the design.
//
// There is exactly one canonical entry value landfall ever writes
// (data-model.md's LandfallRegistrationEntry); every adapter just picks the
// file path and the dotted key path within it (e.g. "mcpServers.landfall" or,
// for VS Code, "servers.landfall").
import { promises as fs } from 'node:fs';
import path from 'node:path';

/** The one value landfall ever writes into a harness's own config. */
export const LANDFALL_ENTRY = Object.freeze({ command: 'landfall', args: ['serve'] });

/** Thrown when an existing config file can't be parsed — never overwritten (FR-012). */
export class UnparseableConfigError extends Error {
  constructor(filePath, cause) {
    super(`${filePath} exists but could not be parsed as JSON: ${cause.message}`);
    this.name = 'UnparseableConfigError';
    this.filePath = filePath;
    this.cause = cause;
  }
}

async function readJsonOrEmpty(filePath) {
  let text;
  try {
    text = await fs.readFile(filePath, 'utf8');
  } catch (err) {
    if (err.code === 'ENOENT') return {};
    throw err;
  }
  if (text.trim() === '') return {};
  try {
    return JSON.parse(text);
  } catch (err) {
    throw new UnparseableConfigError(filePath, err);
  }
}

async function writeJsonPretty(filePath, data) {
  await fs.mkdir(path.dirname(filePath), { recursive: true });
  await fs.writeFile(filePath, JSON.stringify(data, null, 2) + '\n', 'utf8');
}

function getPath(obj, keyPath) {
  return keyPath.split('.').reduce((cur, key) => (cur == null ? undefined : cur[key]), obj);
}

/** Returns a NEW object with `value` set at `keyPath`; does not mutate `obj`. */
function setPath(obj, keyPath, value) {
  const keys = keyPath.split('.');
  const root = { ...obj };
  let cur = root;
  keys.forEach((key, i) => {
    if (i === keys.length - 1) {
      cur[key] = value;
    } else {
      cur[key] = { ...(cur[key] ?? {}) };
      cur = cur[key];
    }
  });
  return root;
}

/** Returns a NEW object with the key at `keyPath` removed; does not mutate `obj`. */
function deletePath(obj, keyPath) {
  const keys = keyPath.split('.');
  const root = { ...obj };
  let cur = root;
  keys.forEach((key, i) => {
    if (i === keys.length - 1) {
      delete cur[key];
    } else {
      cur[key] = { ...(cur[key] ?? {}) };
      cur = cur[key];
    }
  });
  return root;
}

function deepEqual(a, b) {
  return JSON.stringify(a) === JSON.stringify(b);
}

/**
 * Inspect `filePath` (a JSON config file, may not exist) without writing
 * anything, and report what an install would do:
 *  - `write`             — the key is absent, install() will create it
 *  - `already-installed` — the key holds exactly landfall's own entry
 *  - `conflict`          — the key holds something else; NEVER overwritten
 */
export async function planInstall(filePath, keyPath, entry = LANDFALL_ENTRY) {
  const data = await readJsonOrEmpty(filePath);
  const existing = getPath(data, keyPath);
  if (existing === undefined) return { action: 'write', data };
  if (deepEqual(existing, entry)) return { action: 'already-installed', data, existing };
  return { action: 'conflict', data, existing };
}

/**
 * Apply the plan from {@link planInstall}: writes the file ONLY when the key
 * was absent. Returns `{ action: 'configured', ... }` on write, or passes
 * through `already-installed` / `conflict` unchanged (no write in either case).
 */
export async function applyInstall(filePath, keyPath, entry = LANDFALL_ENTRY) {
  const plan = await planInstall(filePath, keyPath, entry);
  if (plan.action !== 'write') return plan;
  const next = setPath(plan.data, keyPath, entry);
  await writeJsonPretty(filePath, next);
  return { action: 'configured', data: next };
}

/**
 * Inspect `filePath` without writing anything, and report what an uninstall
 * would do:
 *  - `not-installed`  — the key is absent (nothing to remove)
 *  - `remove`         — the key holds exactly landfall's own entry
 *  - `left-in-place`  — the key holds something else (was hand-edited since
 *                        install); NEVER deleted
 */
export async function planUninstall(filePath, keyPath, entry = LANDFALL_ENTRY) {
  const data = await readJsonOrEmpty(filePath);
  const existing = getPath(data, keyPath);
  if (existing === undefined) return { action: 'not-installed', data };
  if (deepEqual(existing, entry)) return { action: 'remove', data, existing };
  return { action: 'left-in-place', data, existing };
}

/**
 * Apply the plan from {@link planUninstall}: writes the file ONLY when the
 * key matched landfall's own entry exactly. Returns `{ action: 'removed' }`
 * on write, or passes through `not-installed` / `left-in-place` unchanged.
 */
export async function applyUninstall(filePath, keyPath, entry = LANDFALL_ENTRY) {
  const plan = await planUninstall(filePath, keyPath, entry);
  if (plan.action !== 'remove') return plan;
  const next = deletePath(plan.data, keyPath);
  await writeJsonPretty(filePath, next);
  return { action: 'removed', data: next };
}
