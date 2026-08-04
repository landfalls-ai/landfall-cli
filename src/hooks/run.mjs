// run.mjs — the hook handlers a registered entry actually invokes.
//
// #222 shipped the installer and this contract; #225 filled in `stop` and #227
// `file-changed`. The two block on different things and speak on different
// channels, deliberately:
//
//   stop          exit 2 + stderr — a REFUSAL every host understands. The agent
//                 is concluding, and the point is that it may not yet.
//   file-changed  exit 0 + a structured JSON object on stdout — an INJECTION.
//                 The session is idle; there is nothing to refuse, only
//                 something to hand it.
//
// The shared default for an event with nothing to say is the one that is always
// safe, because the installer registers a command the host runs on every
// matching event from the moment it is written:
//
//   exit 0, print nothing = "nothing pending, carry on"
import { HOOK_EVENTS } from './spec.mjs';
import { runStopHook } from './stop.mjs';
import { protocolForHost, renderNoOp } from './protocol.mjs';
import { runFileChangedHook } from './file-changed.mjs';

export const HOOK_EVENT_IDS = HOOK_EVENTS.map((e) => e.id);

/**
 * Run one hook event. Returns the process exit code the host should see:
 * 0 allows the turn to proceed, 2 blocks it with stderr fed back to the model.
 *
 * `deps.input` is the hook payload the host writes to stdin (all three hosts
 * do) — it carries the loop guard. `deps.host` is the id the registered
 * command was written with (#228); it selects the output protocol and nothing
 * else. Everything else is injectable so the handlers are testable without a
 * serve process.
 */
export async function runHookEvent(eventId, deps = {}) {
  if (!HOOK_EVENT_IDS.includes(eventId)) return { exitCode: 2, error: `unknown hook event: ${eventId}` };
  const protocol = protocolForHost(deps.host);
  if (eventId === 'stop') {
    const { exitCode } = await runStopHook({ ...deps, protocol });
    return { exitCode };
  }
  if (eventId === 'file-changed') {
    const { exitCode } = await runFileChangedHook(deps);
    return { exitCode };
  }
  // An event with no behaviour yet still owes its host a well-formed answer:
  // silence under exit2, `{}` under a protocol that parses stdout every time.
  const noop = renderNoOp(protocol);
  return { exitCode: noop.exitCode, stdout: noop.channel === 'stdout' ? noop.text : '' };
}
