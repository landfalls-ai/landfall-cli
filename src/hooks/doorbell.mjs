// doorbell.mjs — the file a `FileChanged` hook watches (#227, story #190).
//
// The ticket originally called this a SPOOL: the bridge would write incoming
// room events to a per-incident file and the hook would read them out of it.
// The #225 socket decision changed that, and improved it — the file stops being
// the transport and becomes a doorbell.
//
//   serve enqueues an event ─► pending goes 0 → non-empty ─► append a marker
//                                                                    │
//   Claude Code's file watcher fires ◄─────────────────────────────────┘
//                    │
//                    └─► landfall hooks file-changed ──socket──► peek/consume
//
// Two problems dissolve in that change:
//
//  - "Rotate/truncate the spool after consumption" with more than one local
//    session. There is nothing to rotate. Two agents on one incident have two
//    serve processes, two sockets and two cursors, so whoever wakes first no
//    longer consumes the other's context. The marker is idempotent and can be
//    truncated by anyone at any time without losing anything, because it never
//    held the events.
//
//  - Room content in the user's repository. A marker line carries a timestamp,
//    a pid and a count — never an event, never a finding, never a name. The
//    incident stays on the socket, which is readable by one user account, and
//    out of a directory that gets grepped, backed up and occasionally committed.
//
// The 0 → non-empty EDGE is what rings, not every enqueue: a busy room would
// otherwise rewrite the file on every event and wake the hook on each one, for
// context the session is already about to be shown.
import path from 'node:path';
import { promises as fs } from 'node:fs';

/** Directory name reserved in the workspace. Self-ignoring — see ensureDir(). */
export const DOORBELL_DIR = '.landfall';

/**
 * The marker's filename, and it is NOT free choice — Claude Code's
 * `FileChanged` matcher is a list of literal filenames whose exact-match set is
 * "letters, digits, `_`, and `|`". A hyphen drops the whole matcher onto the
 * regular-expression path instead, so `room-events` would only work by way of
 * a regex that happens to match. `room_events` stays on the documented
 * exact-match path, which is why the underscore is load-bearing rather than
 * stylistic. See spec.mjs.
 */
export const DOORBELL_FILE = 'room_events';

/** Past this, the marker file is rewritten rather than appended to. */
const MAX_MARKER_BYTES = 8 * 1024;

export function doorbellPath(cwd = process.cwd()) {
  return path.join(cwd, DOORBELL_DIR, DOORBELL_FILE);
}

/**
 * Create the directory, and make it ignore itself.
 *
 * A `.gitignore` containing `*` ignores everything in its own directory,
 * including itself — so the doorbell never shows up in `git status` and we
 * never have to edit a `.gitignore` the user owns. Silently rewriting a
 * tracked file in someone's repository to make our own artifact tidy is not
 * ours to do.
 */
async function ensureDir(dir) {
  await fs.mkdir(dir, { recursive: true });
  const ignore = path.join(dir, '.gitignore');
  try {
    await fs.writeFile(ignore, '*\n', { flag: 'wx' });
  } catch {
    /* already there (or unwritable) — either way, not worth failing over */
  }
}

/**
 * A doorbell bound to one workspace. `ring()` is best-effort and never throws:
 * a read-only checkout must not take down a serve process, it just means an
 * idle session finds out at its next tool call instead.
 */
export function createDoorbell({ cwd = process.cwd(), pid = process.pid, now = () => new Date().toISOString(), log } = {}) {
  const file = doorbellPath(cwd);
  let warned = false;

  return {
    path: file,
    async ring(pending = 0) {
      try {
        await ensureDir(path.dirname(file));
        const line = `${now()} pid=${pid} pending=${pending}\n`;
        const size = await fs.stat(file).then((s) => s.size, () => 0);
        // Truncating rather than appending past the cap keeps an unattended
        // week-long session from growing a log nobody reads. Nothing is lost:
        // the events are on the socket, not here.
        await fs.writeFile(file, line, { flag: size >= MAX_MARKER_BYTES ? 'w' : 'a' });
      } catch (err) {
        if (!warned) {
          warned = true; // once per process — a nudge, not a per-event complaint
          log?.(`doorbell unavailable (${err.message}) — an idle session will see room context at its next tool call.`);
        }
      }
    },
  };
}

/**
 * Truncate the marker after a hook has consumed what it announced. Idempotent
 * and safe from any process: see the note above about why nothing is lost.
 */
export async function clearDoorbell(cwd = process.cwd()) {
  await fs.truncate(doorbellPath(cwd), 0).catch(() => {});
}
