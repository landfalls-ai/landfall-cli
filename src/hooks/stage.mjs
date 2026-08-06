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
//
// WHAT IS STORED, AND WHY IT IS NOT THE RENDERED TEXT. Per-socket `peek`
// answers, not the assembled block. A stage can cover several sessions, and by
// the time it is read they may no longer share a fate: one still answering (so
// its part is spent — `flushPending` handed those events to that agent in-band)
// while another has died (so its part is the only surviving copy). Delivering
// such a stage is therefore a PARTIAL question, and prose cannot answer it —
// `buildInjection` flattens every contributing session into one undifferentiated
// string with no per-session boundary to cut on, so storing the text left only
// "all of it or none of it", and all-of-it re-delivered the answering session's
// own already-read events (#10 review). Keeping the peeks means the digest is
// re-rendered at delivery from exactly the sessions that still need it, by the
// same renderer, so the text can still never diverge from the live path.
import os from 'node:os';
import path from 'node:path';
import { promises as fs } from 'node:fs';
import { workspaceKey } from './socket.mjs';

// Bumped to 2 when the stage stopped holding rendered text and started holding
// the per-socket peeks behind it. A v1 stage left by an older install is read as
// "no stage" rather than migrated: its text carries no session boundary, so the
// only honest thing to do with it is the safe direction — one missed nudge,
// never a duplicate delivery.
export const STAGE_VERSION = 2;

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
 * Park the owed peeks for the next prompt. Best-effort: a stage we cannot write
 * means the nudge is late, not that the room event is lost — the socket still
 * holds it and #225's Stop hook still refuses a conclusion over it.
 *
 * @param {{peeks: {socketPath: string, response: object}[]}} staged
 */
export async function writeStage({ peeks }, opts = {}) {
  const file = stagePath(opts);
  try {
    const dir = path.dirname(file);
    await fs.mkdir(dir, { recursive: true, mode: 0o700 });
    // `mode` on mkdir only applies to a directory it actually creates, so an
    // existing one keeps whatever mode it had. Same belt-and-braces chmod
    // `socket.mjs` does for this identical path — the directory mode is the
    // whole access-control story for everything that lives in here.
    await fs.chmod(dir, 0o700).catch(() => {});
    await fs.writeFile(file, `${JSON.stringify({ v: STAGE_VERSION, peeks })}\n`, { mode: 0o600 });
    // `mode` on writeFile only applies to a file it creates, the same way it
    // does on mkdir — an existing stage keeps whatever mode it had.
    await fs.chmod(file, 0o600).catch(() => {});
    return true;
  } catch {
    return false;
  }
}

/** The staged peeks, or null when there are none (or unreadable/foreign/older). */
export async function readStage(opts = {}) {
  try {
    const parsed = JSON.parse(await fs.readFile(stagePath(opts), 'utf8'));
    if (parsed?.v !== STAGE_VERSION || !Array.isArray(parsed.peeks)) return null;
    // An entry with no `socketPath` cannot be told apart from a session that
    // answered, so it could only ever be delivered blind. Dropped rather than
    // guessed at.
    const peeks = parsed.peeks.filter((p) => p && typeof p.socketPath === 'string' && p.response);
    return peeks.length ? { peeks } : null;
  } catch {
    return null;
  }
}

/** Drop the stage once it has been delivered. Idempotent. */
export async function clearStage(opts = {}) {
  await fs.unlink(stagePath(opts)).catch(() => {});
}
