// platform.mjs — small, shared OS-probing helpers used by every harness
// adapter's detect()/config-path resolution (research.md R1). Deliberately
// re-reads env/os state on every call rather than caching at import time, so
// tests can redirect HOME/PATH per-sandbox (test/install/helpers.mjs).
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const execFileAsync = promisify(execFile);

/** True if `bin` resolves on the current PATH. */
export async function isOnPath(bin) {
  const cmd = process.platform === 'win32' ? 'where' : 'which';
  try {
    await execFileAsync(cmd, [bin]);
    return true;
  } catch {
    return false;
  }
}

/** True if a filesystem path exists (file OR directory). */
export async function pathExists(candidate) {
  try {
    await fs.access(candidate);
    return true;
  } catch {
    return false;
  }
}

/** The current user's home directory (re-read per call — see file header). */
export function homedir() {
  return os.homedir();
}

/** Windows roaming AppData root, honoring $APPDATA when set (tests). */
export function appDataDir() {
  return process.env.APPDATA || path.join(homedir(), 'AppData', 'Roaming');
}

/** XDG config home, honoring $XDG_CONFIG_HOME when set. */
export function xdgConfigHome() {
  return process.env.XDG_CONFIG_HOME || path.join(homedir(), '.config');
}

/** The user-level VS Code config directory, per OS (VS Code's own convention). */
export function vscodeUserDir() {
  if (process.platform === 'darwin') {
    return path.join(homedir(), 'Library', 'Application Support', 'Code', 'User');
  }
  if (process.platform === 'win32') {
    return path.join(appDataDir(), 'Code', 'User');
  }
  return path.join(xdgConfigHome(), 'Code', 'User');
}

/**
 * App-bundle / install-marker paths to probe for a GUI app, per OS.
 *
 * `LANDFALL_TEST_APP_ROOT`, when set, replaces the hardcoded `/Applications`
 * (or `Program Files`) root — the ONLY way tests can sandbox this check,
 * since these are fixed OS install locations with no per-user env var of
 * their own (unlike HOME-relative paths, which the test env already
 * redirects). Never set outside a test process.
 */
export function appBundleCandidates(macApp, winDirName) {
  const testRoot = process.env.LANDFALL_TEST_APP_ROOT;
  if (process.platform === 'darwin') {
    return [path.join(testRoot ?? '/Applications', `${macApp}.app`)];
  }
  if (process.platform === 'win32') {
    return [
      path.join(testRoot ?? path.join(appDataDir(), '..', 'Local', 'Programs'), winDirName),
      path.join(testRoot ?? path.join('C:', 'Program Files'), winDirName),
    ];
  }
  return [];
}
