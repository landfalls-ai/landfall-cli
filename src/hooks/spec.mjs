// spec.mjs — the canonical set of lifecycle hooks `landfall hooks install`
// registers, in ONE place, so the installer, the uninstaller and the hook
// handlers themselves can never disagree about what was written.
//
// Two events, both from story #190 (`EDGE_PUSH_ARCHITECTURE.md` §5/§10.1):
//
//   stop          — refuse a silent conclusion while room events newer than the
//                   session's last consumed seq exist (#225)
//   file-changed  — inject a digest of spooled room events into an idle
//                   session (#227). Claude Code only: it is the one host with
//                   a file-watch hook.
//
// The command every entry runs is `landfall hooks <id>`. That prefix is also
// how uninstall recognizes a landfall-authored entry it must NOT delete
// because a human edited it since — see isLandfallCommand().

/** @typedef {{id: string, hosts: string[], purpose: string}} HookEvent */

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
    purpose: 'inject spooled room events into an idle session',
  },
];

/** The exact shell command a registered hook entry runs. */
export function hookCommand(eventId) {
  return `landfall hooks ${eventId}`;
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
