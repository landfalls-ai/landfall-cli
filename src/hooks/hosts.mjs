// hosts.mjs — the hook-host registry, in the order every `landfall hooks`
// report prints: Claude Code, Codex CLI, Cursor.
//
// Deliberately a different, shorter list than ../install/harnesses.mjs: MCP
// registration works for any harness that speaks MCP, while a hook needs the
// host to actually have a lifecycle-hook surface. Claude Desktop, VS Code and
// Windsurf have none, so they are not silently reported as "not-detected"
// here — they are simply not hosts.
import * as claudeCode from './hosts/claude-code.mjs';
import * as codex from './hosts/codex.mjs';
import * as cursor from './hosts/cursor.mjs';

export const HOOK_HOSTS = [claudeCode, codex, cursor];

export function hostById(id) {
  return HOOK_HOSTS.find((h) => h.id === id);
}
