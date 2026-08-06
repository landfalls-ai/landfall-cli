// file-changed.mjs — the doorbell wake (#227).
//
// This hook used to try to inject the digest itself. It cannot: Claude Code's
// `FileChanged` "does not support decision control. Exit code and JSON output
// are ignored." The event that can watch a file is not the event that can speak
// to the model.
//
// So the wake now does two things, neither of which needs the host to read our
// output:
//
//   1. STAGE the digest (stage.mjs) for `user-prompt-submit` to deliver.
//   2. NUDGE the human on stderr — the cheap side effect that reaches a person
//      watching the terminal even before the session resumes.
//
// And, critically, it consumes NOTHING. Advancing a cursor here would drop the
// events from the session's queue in exchange for a digest the host discards —
// silently swallowing room context, and taking it out of reach of #225's Stop
// hook too. `never consume without delivering` is the invariant; delivery
// happens in user-prompt-submit.mjs, and the cursor moves there.
import { queryHookSockets } from './socket.mjs';
import { buildInjection, INJECT_MAX } from './digest.mjs';
import { writeStage } from './stage.mjs';
import { clearDoorbell } from './doorbell.mjs';

export { INJECT_MAX };

/** One line, plus a terminal bell, for a human who happens to be looking. */
export function nudgeLine(total) {
  return `⚡ landfall: ${total} update(s) from your war room — they will be handed to this session on your next message.`;
}

/**
 * Run the doorbell wake. Always exits 0 and never writes to stdout: the host
 * ignores both, and a hook that logs onto a channel nobody parses is noise.
 */
export async function runFileChangedHook({
  cwd,
  env,
  platform,
  query = queryHookSockets,
  stage = writeStage,
  clear = clearDoorbell,
  notify = (text) => process.stderr.write(`${text}\n`),
} = {}) {
  let peeks = [];
  try {
    peeks = await query({ op: 'peek' }, { cwd, env, platform });
  } catch {
    return { exitCode: 0, staged: false, context: '' };
  }

  const injection = buildInjection(peeks);
  if (!injection.inject) {
    // Some other file changed, or another session already took this context.
    // Nothing to stage, and nothing we can prove is a stale bell.
    return { exitCode: 0, staged: false, context: '' };
  }

  // The peeks that are actually owed something, per session — not the assembled
  // text. Delivery may need to speak for some of these sessions and not others
  // (see `stage.mjs`), and that cut can only be made while they are still
  // separate. Sessions owing nothing are dropped here so they never widen the
  // stage beyond what it is entitled to deliver.
  const owed = peeks.filter((p) => (p?.response?.count ?? 0) > 0 || (p?.response?.dropped ?? 0) > 0);
  const staged = await stage({ peeks: owed }, { cwd, env, platform });

  const total = peeks.reduce((n, p) => n + (p.response?.count ?? 0) + (p.response?.dropped ?? 0), 0);
  notify(nudgeLine(total));

  // Clear the bell only once the digest is safely staged. If staging failed the
  // bell keeps ringing, which costs one extra wake and is the recoverable
  // direction — the events themselves are still queued on the socket either way.
  if (staged) await Promise.resolve(clear(cwd)).catch(() => null);

  return { exitCode: 0, staged, context: injection.context };
}
