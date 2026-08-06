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
// "PRIORITY" HERE MEANS ANSWERED, NOT OWED. A session that answers is telling
// us what it is still owed, and that answer is the truth even when it is
// "nothing". The stage cannot know what happened after it was written, and
// something usually did: `../tools.mjs`'s `flushPending` drains the very same
// queue in-band after EVERY MCP tool call, with no knowledge of this stage. So
// "the socket owes nothing" overwhelmingly means "the agent already read it" —
// and re-delivering the stage there would hand the agent context it has
// already seen, captioned "while you were idle", breaking the repo's
// nothing-arrives-twice guarantee. The stage is entitled to answer for one
// session only: one we could not reach at all.
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
 *
 * `answered` is the socket paths that actually replied to the peek — the one
 * fact that separates "serve is gone, the stage is the only surviving copy"
 * from "serve answered and owes nothing, so the stage is spent". Without it
 * both look identical (no injection), and the second re-delivers.
 *
 * A stage is judged per session, against the sessions it was built from: it is
 * trusted only while some session named in its `consumes` is unreachable. An
 * empty `consumes` carries no owner to check, so it falls back to the coarse
 * question — did anything answer at all?
 *
 * @returns delivery, plus `staleStage` when a stage was proven spent and its
 *   caller should drop it rather than leave it to surface later.
 */
export function chooseDelivery(live, staged, { answered = [] } = {}) {
  if (live?.inject) return { ...live, source: 'socket', staleStage: false };

  const nothing = { inject: false, context: '', consumes: [], source: 'none', staleStage: false };
  if (!staged?.context) return nothing;

  const reached = new Set(answered);
  const owners = (staged.consumes ?? []).map((c) => c.socketPath);
  const unreachable = owners.length ? owners.some((p) => !reached.has(p)) : reached.size === 0;
  if (!unreachable) return { ...nothing, staleStage: true };

  return { inject: true, context: staged.context, consumes: staged.consumes ?? [], source: 'stage', staleStage: false };
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

  // The raw peeks, not just the digest built from them: WHICH sessions replied
  // is what decides whether the stage still speaks for anyone.
  let peeks = [];
  let live = null;
  try {
    peeks = await query({ op: 'peek' }, opts);
    live = buildInjection(peeks);
  } catch {
    peeks = []; // asked and got nothing back — indistinguishable from serve being gone
    live = null;
  }

  const staged = await Promise.resolve(stage(opts)).catch(() => null);
  const delivery = chooseDelivery(live, staged, { answered: peeks.map((p) => p.socketPath) });

  if (!delivery.inject) {
    // A stage every one of whose sessions answered has been overtaken — most
    // often by `flushPending` handing those events to the agent in-band on an
    // ordinary tool call. Drop it now: left on disk it would inject on some
    // later prompt, once `serve` is gone and can no longer contradict it.
    if (delivery.staleStage) await Promise.resolve(unstage(opts)).catch(() => null);
    return { exitCode: 0, injected: false, source: 'none', context: '' };
  }

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
