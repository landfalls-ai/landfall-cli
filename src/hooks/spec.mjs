// spec.mjs — the canonical set of lifecycle hooks `landfall hooks install`
// registers, in ONE place, so the installer, the uninstaller and the hook
// handlers themselves can never disagree about what was written.
//
// Two events, both from story #190 (`EDGE_PUSH_ARCHITECTURE.md` §5/§10.1):
//
//   stop          — refuse a silent conclusion while room events newer than the
//                   session's last consumed seq exist (#225)
//   file-changed  — inject a digest of room events into an IDLE session (#227),
//                   woken by the doorbell marker. Claude Code only: it is the
//                   one host with a file-watch hook.
//
// The command every entry runs starts `landfall hooks <id>`. That prefix is
// also how uninstall recognizes a landfall-authored entry it must NOT delete
// because a human edited it since — see isLandfallCommand().
import { EXIT2, protocolForHost } from './protocol.mjs';
import { DOORBELL_DIR, DOORBELL_FILE } from './doorbell.mjs';

/** @typedef {{id: string, hosts: string[], purpose: string, matcher?: string}} HookEvent */

/** @type {HookEvent[]} */
export const HOOK_EVENTS = [
  {
    id: 'stop',
    hosts: ['claude-code', 'codex', 'cursor'],
    purpose: 'block a conclusion while unconsumed room context exists',
  },
  {
    id: 'file-changed',
    hosts: ['claude-code'],
    purpose: 'inject room events into an idle session',
    // #222 registered this matcher-less, which fires on ANY change in the
    // workspace. With a doorbell file (#227) that is both noisy and pointless:
    // the one change worth waking for is the marker `landfall serve` appends
    // when its queue goes from empty to non-empty. Narrowing the registration
    // is what turns "every keystroke saved" into "the room has something".
    matcher: `**/${DOORBELL_DIR}/${DOORBELL_FILE}`,
  },
];

/**
 * The exact shell command a registered hook entry runs.
 *
 * A host whose block semantics are the exit-2 convention gets the bare
 * command; a host that needs another output shape gets `--host <id>` appended
 * so the handler is TOLD which contract to answer on rather than inferring it
 * from a payload we did not write (#228).
 *
 * Passing the flag only where it changes behaviour is deliberate: the two
 * hosts already registered with the bare form keep byte-identical entries, so
 * an upgrade churns nobody's config into a conflict.
 */
export function hookCommand(eventId, hostId) {
  const base = `landfall hooks ${eventId}`;
  return protocolForHost(hostId) === EXIT2 ? base : `${base} --host ${hostId}`;
}

/** The matcher an event registers with, or null when it needs none. */
export function matcherFor(eventId) {
  return HOOK_EVENTS.find((e) => e.id === eventId)?.matcher ?? null;
}

/**
 * Exact commands a PREVIOUS version of this installer wrote for the same
 * (event, host) — entries that are ours and unmodified, just stale.
 *
 * This is what keeps an upgrade from stranding people. Without it, changing a
 * registered command turns every existing install into a `conflict`: never
 * overwritten (correct — we cannot tell "stale" from "hand-edited" without
 * this list) and never removable either, since `hooks uninstall` only deletes
 * a byte-exact match of the CURRENT command. Enumerating the old forms here
 * makes "stale" a fourth, distinguishable state that install replaces in place
 * and uninstall cleans up, while an entry matching neither the current nor any
 * past form stays a conflict — a human edited it, and it is not ours to touch.
 */
export function supersededHookCommands(eventId, hostId) {
  // Cursor's `stop` shipped bare in v0.2.0, before the host flag existed; the
  // handler answered it with exit 2 + stderr, which Cursor does not read.
  if (hostId === 'cursor' && eventId === 'stop') return ['landfall hooks stop'];
  return [];
}

/**
 * True for any command string this installer could plausibly have written —
 * including one a human has since edited. Used to distinguish "landfall's
 * entry, modified" (never overwritten, never deleted, never duplicated) from
 * "somebody else's hook" (invisible to us) and from a byte-exact match.
 */
export function isLandfallCommand(command) {
  return typeof command === 'string' && command.trim().startsWith('landfall hooks');
}

/** The event ids registered for one host id, in HOOK_EVENTS order. */
export function eventsForHost(hostId) {
  return HOOK_EVENTS.filter((e) => e.hosts.includes(hostId)).map((e) => e.id);
}
