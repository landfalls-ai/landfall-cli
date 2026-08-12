// status.mjs — `landfall status`, feature 20260812-010632 (US5/T048).
//
// A short-lived process, invoked repeatedly (a Claude Code statusline runs on
// the host's own render cadence), that answers "what's happening in the war
// room this workspace is joined to?" by querying the hook-socket (#225) the
// SAME way a lifecycle hook already does — see ../hooks/socket.mjs's own
// header for why a socket and not a state file.
//
// Deliberately NOT a new transport: `queryHookSockets` (fan-out + best-effort
// across every socket in this workspace, unreachable sockets skipped) already
// exists for exactly this shape of question. This file adds only the display
// half — formatting one line — and a small last-known cache for the ONE case
// a query answers "the socket didn't answer in time" (SC-012): `landfall
// serve` not running at all is a DIFFERENT case (silent, no cache — see
// runStatus below) from a live session that was just briefly slow to answer.
import path from 'node:path';
import { promises as fs } from 'node:fs';
import { listHookSockets, queryHookSockets } from './hooks/socket.mjs';
import { stageDir } from './hooks/stage.mjs';

const CACHE_FILE = 'status-cache.json';

function cachePath(opts) {
  return path.join(stageDir(opts), CACHE_FILE);
}

/** Best-effort: a cache we cannot write means the next timeout gets nothing
 * to fall back to, never a crash. Same 0700/0600 discipline stage.mjs uses.
 * Exported for tests — driving the real fallback path in `queryStatus`
 * without needing a genuinely live socket, the same way `stage.mjs`'s
 * `writeStage`/`readStage` are exported for `file-changed.test.mjs`. */
export async function writeCache(status, opts = {}) {
  try {
    const file = cachePath(opts);
    await fs.mkdir(path.dirname(file), { recursive: true, mode: 0o700 });
    await fs.writeFile(file, `${JSON.stringify(status)}\n`, { mode: 0o600 });
    await fs.chmod(file, 0o600).catch(() => {});
  } catch {
    /* best-effort */
  }
}

/** The last successfully-cached status, or null when there is none (or it is
 * unreadable) — never a throw. */
export async function readCache(opts = {}) {
  try {
    return JSON.parse(await fs.readFile(cachePath(opts), 'utf8'));
  } catch {
    return null;
  }
}

/**
 * Query this workspace's status.
 *
 * TWO honest outcomes, not one: `null` means "landfall serve is not running
 * here" (no socket exists at all — a stale cache would be actively
 * misleading in this case, e.g. a closed terminal's old incident bleeding
 * into a statusline that should now be blank, so this path never reads the
 * cache). A socket existing but every query to it timing out falls back to
 * the last-known value instead — the session is presumably still there, just
 * momentarily slow, and showing nothing would be a worse answer than showing
 * slightly-stale truth for one render tick.
 */
export async function queryStatus(opts = {}) {
  const sockets = await listHookSockets(opts);
  if (!sockets.length) return null; // not running — see the docstring above

  const answers = await queryHookSockets({ op: 'status' }, opts);
  const first = answers.find((a) => a.response?.ok);
  if (first) {
    await writeCache(first.response, opts);
    return first.response;
  }
  // Sockets exist but none answered in time — SC-012's fallback case.
  return readCache(opts);
}

/**
 * One line, or '' when there is nothing to say (queryStatus returned null —
 * the caller should print nothing at all, per T051/SC-012's "silent absence,
 * never an error line").
 */
export function formatStatusLine(status) {
  if (!status) return '';
  const parts = [`🔴 landfall${status.incidentId ? ` #${status.incidentId}` : ''}`];
  if (status.pending) parts.push(`${status.pending} new`);
  if (status.votesAwaited) parts.push(`${status.votesAwaited} vote${status.votesAwaited === 1 ? '' : 's'} awaited`);
  // T050 (Phase 6 dependency): the divergence segment, once `status.divergence`
  // is populated by the socket verb — additive, absent until that lands.
  if (status.divergence?.diverging) {
    parts.push(`diverging from established root cause${status.divergence.establishedSubject ? ` (${status.divergence.establishedSubject})` : ''}`);
  }
  return parts.join(' · ');
}

/** The `landfall status` command itself: query, format, print (or print
 * nothing), always exit 0 — a statusline command's failure mode must never
 * be a visible error in someone's prompt. */
export async function runStatus(opts = {}) {
  let status = null;
  try {
    status = await queryStatus(opts);
  } catch {
    // A query that throws (not merely times out) is the same as "nothing to
    // say" from a statusline's point of view — never surface it as an error.
    status = null;
  }
  const line = formatStatusLine(status);
  if (line) process.stdout.write(line);
}
