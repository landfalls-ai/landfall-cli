// vscode.mjs — VS Code (Copilot agent mode) adapter (research.md R1).
//
// VS Code uses a "servers" key (not "mcpServers" like every other harness
// here — a real asymmetry, not a typo) in its user-profile mcp.json. Install
// prefers `code --add-mcp` when the `code` CLI is on PATH; there's no
// documented CLI remove, so uninstall always edits the file directly via the
// same non-destructive json-merge core every other JSON harness uses.
import path from 'node:path';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { planInstall, planUninstall, applyInstall, applyUninstall } from '../json-merge.mjs';
import { isOnPath, pathExists, vscodeUserDir, appBundleCandidates } from '../platform.mjs';

const execFileAsync = promisify(execFile);

export const id = 'vscode';
export const displayName = 'VS Code';

// No "type" needed for a stdio entry under VS Code's schema (research.md R1).
const ENTRY = { command: 'landfall', args: ['serve'] };
const KEY_PATH = 'servers.landfall';

function configPath() {
  return path.join(vscodeUserDir(), 'mcp.json');
}

export async function detect() {
  if (await isOnPath('code')) return true;
  for (const candidate of appBundleCandidates('Visual Studio Code', 'Microsoft VS Code')) {
    if (await pathExists(candidate)) return true;
  }
  return false;
}

export async function install() {
  let plan;
  try {
    plan = await planInstall(configPath(), KEY_PATH, ENTRY);
  } catch (err) {
    return { status: 'failed', detail: err.message, configPath: configPath() };
  }
  if (plan.action === 'already-installed') return { status: 'already-installed', configPath: configPath() };
  if (plan.action === 'conflict') {
    return {
      status: 'conflict',
      detail: 'an existing "landfall" server entry differs from what this installer would write',
      configPath: configPath(),
    };
  }

  if (await isOnPath('code')) {
    try {
      await execFileAsync('code', ['--add-mcp', JSON.stringify({ name: 'landfall', ...ENTRY })]);
      return { status: 'configured', configPath: configPath() };
    } catch (err) {
      return { status: 'failed', detail: err.message, configPath: configPath() };
    }
  }

  try {
    await applyInstall(configPath(), KEY_PATH, ENTRY);
  } catch (err) {
    return { status: 'failed', detail: err.message, configPath: configPath() };
  }
  return { status: 'configured', configPath: configPath() };
}

/** Non-mutating peek used by `landfall uninstall` to build its candidate list. */
export async function hasEntry() {
  try {
    return (await planUninstall(configPath(), KEY_PATH, ENTRY)).action !== 'not-installed';
  } catch {
    return true;
  }
}

export async function uninstall() {
  try {
    const result = await applyUninstall(configPath(), KEY_PATH, ENTRY);
    return { status: statusFor(result.action), configPath: configPath() };
  } catch (err) {
    return { status: 'failed', detail: err.message, configPath: configPath() };
  }
}

function statusFor(action) {
  if (action === 'removed') return 'removed';
  if (action === 'not-installed') return 'not-installed';
  return 'left-in-place';
}
