// claude-code.mjs — Claude Code adapter (research.md R1).
//
// Claude Code ships its own `claude mcp add-json`/`claude mcp remove` CLI for
// user-scope (global) MCP registration, so WRITES go through that CLI rather
// than landfall hand-editing Claude Code's internal `~/.claude.json` format.
// Reading that same file to decide already-installed/conflict/not-installed
// is safe (it's just an inspection, not a write) and lets install/uninstall
// stay idempotent without depending on `claude mcp list`'s output format.
import path from 'node:path';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { planInstall, planUninstall } from '../json-merge.mjs';
import { isOnPath, homedir } from '../platform.mjs';

const execFileAsync = promisify(execFile);

export const id = 'claude-code';
export const displayName = 'Claude Code';

// Claude Code's schema requires "type": "stdio" (research.md R1) — a superset
// of the plain { command, args } value every other harness uses.
const ENTRY = { type: 'stdio', command: 'landfall', args: ['serve'] };
const KEY_PATH = 'mcpServers.landfall';

// Claude Code, uniquely among the supported harnesses, also has a native
// PLUGIN system (`claude plugin ...`) — a marketplace + plugin manifest,
// checked into this repo at .claude-plugin/, ships not just the MCP server
// but the landfall-investigation-dashboard agent (recognizes a war-room join
// prompt on sight, specializes in post_widget dashboard upkeep). Wiring that
// up too is what makes `landfall install` "seamless": no separate manual
// `claude plugin marketplace add` step for a Claude Code user.
const PLUGIN_MARKETPLACE_SOURCE = 'landfalls-ai/landfall-cli';
const PLUGIN_ID = 'landfall-edge-bridge@landfall';

function configPath() {
  return path.join(homedir(), '.claude.json');
}

export async function detect() {
  return isOnPath('claude');
}

/**
 * Best-effort: register the marketplace and install the plugin at user
 * scope. Never throws — this rides along with the MCP registration above,
 * which is the part `landfall install`'s contract actually promises; a
 * plugin-install failure (offline, an unreleased branch, an older `claude`
 * CLI without `claude plugin`) must not turn a successful MCP registration
 * into a reported failure. Both underlying commands are idempotent, so
 * re-running `landfall install` re-attempts a previously failed plugin step
 * for free.
 */
async function installPluginBestEffort() {
  try {
    await execFileAsync('claude', ['plugin', 'marketplace', 'add', PLUGIN_MARKETPLACE_SOURCE, '--scope', 'user']);
    await execFileAsync('claude', ['plugin', 'install', PLUGIN_ID, '--scope', 'user']);
    return 'installed';
  } catch {
    return 'skipped';
  }
}

export async function install() {
  let plan;
  try {
    plan = await planInstall(configPath(), KEY_PATH, ENTRY);
  } catch (err) {
    return { status: 'failed', detail: err.message, configPath: configPath() };
  }
  if (plan.action === 'already-installed') {
    return { status: 'already-installed', configPath: configPath(), pluginStatus: await installPluginBestEffort() };
  }
  if (plan.action === 'conflict') {
    return {
      status: 'conflict',
      detail: 'an existing "landfall" MCP server entry differs from what this installer would write',
      configPath: configPath(),
    };
  }
  try {
    await execFileAsync('claude', ['mcp', 'add-json', 'landfall', JSON.stringify(ENTRY), '--scope', 'user']);
  } catch (err) {
    return { status: 'failed', detail: err.message, configPath: configPath() };
  }
  return { status: 'configured', configPath: configPath(), pluginStatus: await installPluginBestEffort() };
}

/** Non-mutating peek used by `landfall uninstall` to build its candidate list. */
export async function hasEntry() {
  try {
    return (await planUninstall(configPath(), KEY_PATH, ENTRY)).action !== 'not-installed';
  } catch {
    return true; // unparseable/unreadable — surface it as a candidate rather than hide it
  }
}

export async function uninstall() {
  let plan;
  try {
    plan = await planUninstall(configPath(), KEY_PATH, ENTRY);
  } catch (err) {
    return { status: 'failed', detail: err.message, configPath: configPath() };
  }
  if (plan.action === 'not-installed') return { status: 'not-installed', configPath: configPath() };
  if (plan.action === 'left-in-place') return { status: 'left-in-place', configPath: configPath() };
  try {
    await execFileAsync('claude', ['mcp', 'remove', 'landfall', '--scope', 'user']);
  } catch (err) {
    return { status: 'failed', detail: err.message, configPath: configPath() };
  }
  // Best-effort, mirrors installPluginBestEffort(): leaving the plugin behind
  // after `landfall uninstall` would strand a dangling MCP-less agent.
  try {
    await execFileAsync('claude', ['plugin', 'uninstall', PLUGIN_ID, '--scope', 'user']);
  } catch {
    /* nothing to remove, or an older claude CLI without `claude plugin` — not fatal */
  }
  return { status: 'removed', configPath: configPath() };
}
