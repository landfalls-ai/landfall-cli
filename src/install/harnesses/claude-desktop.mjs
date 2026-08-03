// claude-desktop.mjs — Claude Desktop adapter (research.md R1). macOS/Windows
// only — there is no official Linux build (research.md R3). No CLI exists for
// MCP config, so both install and uninstall go through the shared
// non-destructive JSON merge core.
import path from 'node:path';
import { planInstall, planUninstall, applyInstall, applyUninstall } from '../json-merge.mjs';
import { pathExists, homedir, appDataDir, appBundleCandidates } from '../platform.mjs';

export const id = 'claude-desktop';
export const displayName = 'Claude Desktop';

const ENTRY = { command: 'landfall', args: ['serve'] };
const KEY_PATH = 'mcpServers.landfall';

function configPath() {
  if (process.platform === 'darwin') {
    return path.join(homedir(), 'Library', 'Application Support', 'Claude', 'claude_desktop_config.json');
  }
  // Windows; also used as an inert fallback path on other platforms, where
  // detect() below always returns false (research.md R3 — no official Linux build).
  return path.join(appDataDir(), 'Claude', 'claude_desktop_config.json');
}

export async function detect() {
  if (process.platform !== 'darwin' && process.platform !== 'win32') return false;
  for (const candidate of appBundleCandidates('Claude', 'Claude')) {
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
      detail: 'an existing "landfall" MCP server entry differs from what this installer would write',
      configPath: configPath(),
    };
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
    if (result.action === 'removed') return { status: 'removed', configPath: configPath() };
    if (result.action === 'not-installed') return { status: 'not-installed', configPath: configPath() };
    return { status: 'left-in-place', configPath: configPath() };
  } catch (err) {
    return { status: 'failed', detail: err.message, configPath: configPath() };
  }
}
