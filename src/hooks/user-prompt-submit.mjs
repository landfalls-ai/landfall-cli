// user-prompt-submit.mjs — where the idle digest is actually delivered (#227,
// operator decision 2026-08-05).
//
// `FileChanged` can watch the doorbell but its output is discarded;
// `UserPromptSubmit` can speak to the model but does not know the room changed.
// This is the half that speaks. It fires on every prompt (the event takes no
// matcher), so the common case — nothing owed — must cost nothing.
//
// The honest limit, recorded rather than glossed: there is no way to wake an
// idle Claude Code session with zero human action, because nothing server-side
// can push into one. So the earliest moment an idle session can act on room
// context is the human's next message — which is what this delivers, before the
// model generates anything.
//
// TWO SOURCES, IN PRIORITY ORDER:
//   1. a live socket — authoritative, freshest, and the only one whose cursor
//      can actually be advanced;
//   2. the stage — for when `serve` exited between the wake and the prompt, so
//      the socket is gone but the context should still arrive.
//
// The cursor advances only AFTER the digest has been emitted, which is where
// `never consume without delivering` is finally paid off.
import { queryHookSockets, sendToSocket } from './socket.mjs';
import { buildInjection } from './digest.mjs';
import { readStage, clearStage } from './stage.mjs';
import { clearDoorbell } from './doorbell.mjs';

/** Claude Code's structured output for this event. */
export function promptPayload(context) {
  return { hookSpecificOutput: { hookEventName: 'UserPromptSubmit', additionalContext: context } };
}

/**
 * Decide what to deliver from the two sources. Pure, so the precedence rule is
 * testable without a socket or a filesystem.
 */
export function chooseDelivery(live, staged) {
  if (live?.inject) return { ...live, source: 'socket' };
  if (staged?.context) return { inject: true, context: staged.context, consumes: staged.consumes ?? [], source: 'stage' };
  return { inject: false, context: '', consumes: [], source: 'none' };
}

/**
 * Run the prompt-submit hook. Exit 0 always — this event CAN block a prompt,
 * and deliberately never does: the human is mid-sentence, and refusing their
 * message to show them a digest would be a worse interruption than the one this
 * feature exists to prevent.
 */
export async function runUserPromptSubmitHook({
  cwd,
  env,
  platform,
  query = queryHookSockets,
  send = sendToSocket,
  stage = readStage,
  unstage = clearStage,
  clear = clearDoorbell,
  emit = (text) => process.stdout.write(`${text}\n`),
} = {}) {
  const opts = { cwd, env, platform };

  let live = null;
  try {
    live = buildInjection(await query({ op: 'peek' }, opts));
  } catch {
    live = null; // fall through to the stage
  }

  const staged = live?.inject ? null : await stage(opts).catch(() => null);
  const delivery = chooseDelivery(live, staged);
  if (!delivery.inject) return { exitCode: 0, injected: false, source: 'none', context: '' };

  emit(JSON.stringify(promptPayload(delivery.context)));

  // Only now may cursors move. A consume that fails leaves the events queued —
  // the recoverable direction, and #225's Stop hook still covers them.
  const consumed = await Promise.all(
    (delivery.consumes ?? []).map(({ socketPath, upTo }) =>
      Promise.resolve(send(socketPath, { op: 'consume', upTo })).then(
        () => true,
        () => false,
      ),
    ),
  );

  // The stage has been delivered whether or not a cursor moved, so it always
  // goes: leaving it would re-inject the same block on the next prompt.
  await Promise.resolve(unstage(opts)).catch(() => null);
  if (consumed.every(Boolean)) await Promise.resolve(clear(cwd)).catch(() => null);

  return { exitCode: 0, injected: true, source: delivery.source, context: delivery.context };
}
