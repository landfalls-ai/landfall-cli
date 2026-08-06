// stage.mjs — where a digest waits between the doorbell wake and the next
// prompt (#227, operator decision 2026-08-05).
//
// WHY A STAGE EXISTS AT ALL. Claude Code's `FileChanged` "does not support
// decision control. Exit code and JSON output are ignored." — so the event that
// can WATCH the doorbell cannot DELIVER anything, and the event that can
// deliver (`UserPromptSubmit`) does not know the room changed. The stage is the
// join between them.
//
// WHY NOT IN THE WORKSPACE. `.landfall/room_events` is deliberately a
// contentless doorbell: a timestamp, a pid and a count, because a file in
// someone's repository gets grepped, backed up and occasionally committed.
// A staged digest IS room content, so it goes where the sockets already live —
// a `0700` directory outside the repo, at `0600` — and the doorbell stays
// contentless. Same staging behaviour the decision asked for, one directory to
// the left.
//
// WHAT IT BUYS over having `UserPromptSubmit` simply re-query the sockets: if
// the `serve` process exits between the wake and the next prompt, the socket is
// gone but the digest survives. That is the case the stage is for; a live
// socket is always preferred when one answers.
import os from 'node:os';
import path from 'node:path';
import { promises as fs } from 'node:fs';
import { workspaceKey } from './socket.mjs';

export const STAGE_VERSION = 1;

/** A real filesystem directory on every platform (unlike the pipe namespace). */
export function stageDir({ cwd = process.cwd(), env = process.env, platform = process.platform } = {}) {
  const key = workspaceKey(cwd);
  const base =
    platform === 'win32'
      ? path.join(env.LOCALAPPDATA ?? env.APPDATA ?? os.homedir(), 'landfall', 'state')
      : env.XDG_RUNTIME_DIR
        ? path.join(env.XDG_RUNTIME_DIR, 'landfall')
        : path.join(env.HOME ?? os.homedir(), '.local', 'state', 'landfall', 'run');
  return path.join(base, key);
}

export function stagePath(opts) {
  return path.join(stageDir(opts), 'pending-digest.json');
}

/**
 * Park a digest for the next prompt. Best-effort: a stage we cannot write means
 * the nudge is late, not that the room event is lost — the socket still holds
 * it and #225's Stop hook still refuses a conclusion over it.
 */
export async function writeStage({ context, consumes }, opts = {}) {
  const file = stagePath(opts);
  try {
    const dir = path.dirname(file);
    await fs.mkdir(dir, { recursive: true, mode: 0o700 });
    // `mode` on mkdir only applies to a directory it actually creates, so an
    // existing one keeps whatever mode it had. Same belt-and-braces chmod
    // `socket.mjs` does for this identical path — the directory mode is the
    // whole access-control story for everything that lives in here.
    await fs.chmod(dir, 0o700).catch(() => {});
    await fs.writeFile(file, `${JSON.stringify({ v: STAGE_VERSION, context, consumes })}\n`, { mode: 0o600 });
    return true;
  } catch {
    return false;
  }
}

/** The staged digest, or null when there is none (or it is unreadable/foreign). */
export async function readStage(opts = {}) {
  try {
    const parsed = JSON.parse(await fs.readFile(stagePath(opts), 'utf8'));
    if (parsed?.v !== STAGE_VERSION || typeof parsed.context !== 'string' || !parsed.context) return null;
    return { context: parsed.context, consumes: Array.isArray(parsed.consumes) ? parsed.consumes : [] };
  } catch {
    return null;
  }
}

/** Drop the stage once it has been delivered. Idempotent. */
export async function clearStage(opts = {}) {
  await fs.unlink(stagePath(opts)).catch(() => {});
}
