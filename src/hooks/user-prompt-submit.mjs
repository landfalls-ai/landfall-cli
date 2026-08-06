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
// TWO SOURCES, AND THE ANSWER IS A UNION OF BOTH, NEVER A CHOICE BETWEEN THEM:
//   1. a live socket — authoritative, freshest, and the only one whose cursor
//      can actually be advanced;
//   2. the stage — for when `serve` exited between the wake and the prompt, so
//      the socket is gone but the context should still arrive.
//
// WHICH SOURCE SPEAKS IS DECIDED PER SESSION, ON ONE QUESTION: did that session
// answer the peek? A session that answers is telling us what it is still owed,
// and that answer is the truth even when it is "nothing". The stage cannot know
// what happened after it was written, and something usually did:
// `../tools.mjs`'s `flushPending` drains the very same queue in-band after
// EVERY MCP tool call, with no knowledge of this stage. So "the socket owes
// nothing" overwhelmingly means "the agent already read it" — and re-delivering
// the stage there would hand the agent context it has already seen, captioned
// "while you were idle", breaking the repo's nothing-arrives-twice guarantee.
// The stage is entitled to speak for one kind of session only: one we could not
// reach at all.
//
// THAT IS A FILTER, NOT A PRECEDENCE ORDER — and treating it as an order is how
// this went wrong twice. One stage can cover several sessions which by delivery
// time need not share a fate: one answering, one dead. Weighing the stage as a
// whole ("is ANY owner unreachable? → deliver all of it") re-delivers the
// answering session's already-read events; ranking the live socket above it
// ("does ANYONE owe something live? → deliver only that") silently DROPS the
// dead session's events, which is worse, because the caller then deletes the
// stage that held the only surviving copy. Both questions are the coarse one.
// So the two sources are sliced by session and unioned: every reachable session
// speaks for itself, every unreachable one is spoken for by the stage, and the
// digest is rendered once over the union.
//
// The cursor advances only AFTER the digest has been emitted, which is where
// `never consume without delivering` is finally paid off.
import { queryHookSockets, sendToSocket } from './socket.mjs';
import { buildInjection, owesUpdates } from './digest.mjs';
import { readStage, clearStage } from './stage.mjs';
import { clearDoorbell } from './doorbell.mjs';

/** Claude Code's structured output for this event. */
export function promptPayload(context) {
  return { hookSpecificOutput: { hookEventName: 'UserPromptSubmit', additionalContext: context } };
}

/**
 * Assemble what to deliver from both sources. Pure, so the rule is testable
 * without a socket or a filesystem.
 *
 * `peeks` is the RAW peek answers, not a digest built from them, because the
 * two facts this needs are per session and a rendered block has neither: WHICH
 * sessions replied (the one thing separating "serve is gone, the stage is the
 * only surviving copy" from "serve answered and owes nothing, so the stage is
 * spent") and which of them owe anything. Handed only the assembled text, the
 * best available question is "does anyone owe something?", and every wrong
 * answer this function has given came from asking it.
 *
 * So the sources are unioned per session rather than ranked:
 *   - a session that replied speaks for itself, and its reply — including
 *     "nothing owed" — retires its own staged entry;
 *   - a session that did not reply is spoken for by its staged entry, whether
 *     or not any OTHER session has live content this turn. That last clause is
 *     the fix for #10's third round: an early return on live content skipped
 *     the stage entirely, and `runUserPromptSubmitHook` then unstaged it, so a
 *     dead session's only surviving events were deleted undelivered.
 *
 * The union is rendered by the same `buildInjection` the live path uses, so one
 * digest goes out with one honest total, and staged text can never diverge from
 * live text.
 *
 * @param {{socketPath: string, response: object}[]} peeks every socket that
 *   answered the peek, owed or not
 * @param {{peeks: {socketPath: string, response: object}[]}|null} staged
 * @returns delivery, plus `staleStage` when the stage contributed nothing, so
 *   its caller should drop it rather than leave it to surface later. When the
 *   delivery DOES go out, every staged entry has either been folded into it or
 *   been retired by its own session's answer — which is what makes the caller's
 *   unconditional `unstage()` safe.
 */
export function chooseDelivery(peeks, staged) {
  const answered = Array.isArray(peeks) ? peeks : [];
  const reached = new Set(answered.map((p) => p?.socketPath));
  const stagedPeeks = staged?.peeks ?? [];

  const fromSocket = answered.filter(owesUpdates);
  const fromStage = stagedPeeks.filter((p) => !reached.has(p.socketPath)).filter(owesUpdates);

  const built = buildInjection([...fromSocket, ...fromStage]);
  if (!built.inject) {
    // Nothing owed anywhere. A stage that exists at this point speaks for
    // nobody — every one of its sessions either answered or turned out to owe
    // nothing — so say so, rather than leaving it to fire on a later prompt
    // once `serve` is gone and can no longer contradict it.
    return { inject: false, context: '', consumes: [], source: 'none', staleStage: stagedPeeks.length > 0 };
  }

  const source = fromSocket.length ? (fromStage.length ? 'socket+stage' : 'socket') : 'stage';
  return { ...built, source, staleStage: false };
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

  // The raw peeks, not a digest built from them: WHICH sessions replied is what
  // decides, session by session, whether the stage still speaks for any of them.
  let peeks = [];
  try {
    peeks = await query({ op: 'peek' }, opts);
  } catch {
    peeks = []; // asked and got nothing back — indistinguishable from serve being gone
  }

  const staged = await Promise.resolve(stage(opts)).catch(() => null);
  const delivery = chooseDelivery(peeks, staged);

  if (!delivery.inject) {
    // A stage none of whose sessions still owes anything has been overtaken —
    // most often by `flushPending` handing those events to the agent in-band on
    // an ordinary tool call. Drop it now: left on disk it would inject on some
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

  // Everything the stage still spoke for is inside the block just emitted —
  // `chooseDelivery` unions rather than ranks, so no staged entry can be left
  // undelivered behind a live one — and it went out whether or not a cursor
  // moved. So the stage always goes here: leaving it would re-inject the same
  // block on the next prompt.
  await Promise.resolve(unstage(opts)).catch(() => null);
  if (consumed.every(Boolean)) await Promise.resolve(clear(cwd)).catch(() => null);

  return { exitCode: 0, injected: true, source: delivery.source, context: delivery.context };
}
