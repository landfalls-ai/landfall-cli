// windsurf.mjs — Windsurf (Codeium) adapter (research.md R1). No CLI exists
// for MCP config, so both install and uninstall go through the shared
// non-destructive JSON merge core. The config file is not auto-created by
// Windsurf itself — applyInstall creates it (and its parent directories) the
// first time landfall is configured.
import path from 'node:path';
import { planInstall, planUninstall, applyInstall, applyUninstall } from '../json-merge.mjs';
import { pathExists, homedir, appBundleCandidates } from '../platform.mjs';

export const id = 'windsurf';
export const displayName = 'Windsurf';

const ENTRY = { command: 'landfall', args: ['serve'] };
const KEY_PATH = 'mcpServers.landfall';

function windsurfDir() {
  return path.join(homedir(), '.codeium', 'windsurf');
}

function configPath() {
  return path.join(windsurfDir(), 'mcp_config.json');
}

export async function detect() {
  for (const candidate of appBundleCandidates('Windsurf', 'Windsurf')) {
    if (await pathExists(candidate)) return true;
  }
  return pathExists(windsurfDir());
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
