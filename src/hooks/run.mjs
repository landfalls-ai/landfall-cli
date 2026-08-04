// run.mjs — the hook handlers a registered entry actually invokes.
//
// SCOPE: this ticket (#222) is the installer. The behaviour behind each event
// is its own ticket — #225 for `stop` (block a conclusion while unconsumed
// room context exists) and #227 for `file-changed` (inject spooled events into
// an idle session). What is here is the contract those two fill in.
//
// The handlers exist NOW, rather than landing with their behaviour, because
// the installer registers a command the host will run on every matching event
// from the moment it is written. A command that does not resolve would turn a
// successful install into a per-turn error in the user's agent — so the
// default is the one that is always safe:
//
//   exit 0, print nothing = "nothing pending, carry on"
//
// which is precisely what both events should do when there is no room context
// waiting. Until #225/#227 wire in the bridge daemon, there never is.
import { HOOK_EVENTS } from './spec.mjs';

export const HOOK_EVENT_IDS = HOOK_EVENTS.map((e) => e.id);

/**
 * Run one hook event. Returns the process exit code the host should see:
 * 0 allows the turn to proceed. #225 introduces the non-zero path.
 */
export async function runHookEvent(eventId) {
  if (!HOOK_EVENT_IDS.includes(eventId)) return { exitCode: 2, error: `unknown hook event: ${eventId}` };
  return { exitCode: 0 };
}
