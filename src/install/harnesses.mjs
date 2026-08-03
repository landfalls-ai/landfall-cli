// harnesses.mjs — the HarnessDescriptor registry (data-model.md), in the
// fixed order contracts/cli.md's output uses everywhere: Claude Code, Cursor,
// VS Code, Codex CLI, Claude Desktop, Windsurf.
import * as claudeCode from './harnesses/claude-code.mjs';
import * as cursor from './harnesses/cursor.mjs';
import * as vscode from './harnesses/vscode.mjs';
import * as codex from './harnesses/codex.mjs';
import * as claudeDesktop from './harnesses/claude-desktop.mjs';
import * as windsurf from './harnesses/windsurf.mjs';

/** @type {{id: string, displayName: string, detect: () => Promise<boolean>, install: () => Promise<object>, uninstall: () => Promise<object>}[]} */
export const HARNESSES = [claudeCode, cursor, vscode, codex, claudeDesktop, windsurf];

export function harnessById(id) {
  return HARNESSES.find((h) => h.id === id);
}
