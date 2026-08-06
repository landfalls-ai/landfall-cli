// stop.mjs — the `stop` lifecycle hook (#225, story #190).
//
// An agent that has been investigating for ten minutes concludes with a summary
// it believes is current. Meanwhile another investigator published the finding
// that changes the answer. The war room saw it; this agent did not, because an
// MCP session only learns things in-band, on a tool result it chose to make.
//
// So: on Stop, ask every `landfall serve` session in this workspace whether
// room events newer than its cursor exist. If any do, spell them out and refuse
// the conclusion. If none do, exit silently and instantly.
//
// ORDER MATTERS — peek, block, THEN consume. `peek` is non-destructive, so if
// this process dies between reporting and consuming, the events stay queued and
// block again on the next attempt. That is the safe direction to fail: a
// duplicate nag costs a turn, a swallowed finding costs the incident.
//
// LOOP GUARD. Two independent things guarantee a session can always terminate:
//
//  1. `stop_hook_active` — the host sets it on the Stop that follows a block.
//     One acknowledgement is the whole ask; a second block on the same
//     conclusion would be the hook arguing with the agent.
//  2. Consume-after-block. Whatever was reported is removed from the queue, so
//     a further block requires genuinely NEW events. Without (1) an agent in a
//     very busy room could still be nagged repeatedly; without (2) it could
//     never stop at all. Both, so neither failure is reachable.
//
// #252 ADDS A SECOND REASON TO REFUSE, and it behaves differently: the room has
// QUARANTINED context this agent relied on, or a claim contradicting the
// admitted record is still awaiting its position. That is not discharged by
// reading — it stays true until the agent does something about it — so there is
// nothing to consume and guard (2) does not apply to it. Guard (1) does, and is
// what still guarantees termination: one refusal per conclusion, then the
// host's `stop_hook_active` lets the agent through. The alternative, refusing
// until the room's ruling is acted on, would be a hook that can strand a
// session on other people's votes.
//
// #228 ADDS A SECOND OUTPUT SHAPE and changes nothing above. The decision is
// still `buildStopDecision`; only its delivery is host-parameterized, because
// Cursor reads a hook's verdict as JSON on stdout and cannot see an exit code
// or a line of stderr at all. See protocol.mjs — including why Cursor's
// termination guarantee does not rest on `stop_hook_active`, which it never
// sends.
import { queryHookSockets, sendToSocket } from './socket.mjs';
import { EXIT2, renderStopVerdict } from './protocol.mjs';
import { BLOCKER_FOOT, BLOCKER_HEAD, describeStopBlockers, stopBlockers } from '../attention.mjs';

/**
 * Ceiling on what the hook writes back. Hook output is folded into the model's
 * context, and this is a nudge to go read the room — not a place to replay the
 * timeline. The pointer at the end is how the full detail stays reachable.
 */
export const HOOK_OUTPUT_MAX = 10_000;

/** Events spelled out in full before the block becomes a count. */
const DIGEST_MAX_LINES = 12;

/**
 * Turn `peek` answers into the decision: whether to block, what to say, and
 * which sockets to advance afterwards. Pure — no I/O, no clock.
 *
 * @param {{socketPath: string, response: object}[]} peeks
 */
export function buildStopDecision(peeks, { maxChars = HOOK_OUTPUT_MAX } = {}) {
  const answers = Array.isArray(peeks) ? peeks : [];
  const owed = answers.filter((p) => (p?.response?.count ?? 0) > 0 || (p?.response?.dropped ?? 0) > 0);
  const total = owed.reduce((n, p) => n + (p.response.count ?? 0) + (p.response.dropped ?? 0), 0);

  // #252: the second, independent reason to refuse — the room has QUARANTINED
  // context this agent relied on, or a claim contradicting the admitted record
  // is still awaiting its position. Unlike unconsumed events this is not
  // discharged by reading: it is discharged by the agent doing something about
  // it, so nothing is consumed for it. The `stop_hook_active` guard in
  // `runStopHook` is what still guarantees the session can terminate.
  const blockerLines = describeStopBlockers(mergeBlockers(answers));

  if (!total && !blockerLines.length) return { block: false, reason: '', consumes: [] };

  // Union across sessions (see queryHookSockets): over-reporting a sibling
  // window's context is recoverable, under-reporting is the bug this prevents.
  const lines = owed.flatMap((p) => p.response.digest ?? []);
  const resumeSeq = owed.length ? Math.min(...owed.map((p) => p.response.cursor ?? -1)) : -1;
  const shown = lines.slice(-DIGEST_MAX_LINES);
  let omitted = total - shown.length;

  const head = `⚠ Do not conclude yet — ${total} update(s) from other investigators reached this war room since you last read it:`;
  const tail = (n) =>
    n > 0
      ? `+${n} earlier update(s) not shown — call get_updates with sinceSeq=${resumeSeq} for the full detail.`
      : '';
  const foot =
    'Read these before you conclude. If they change your answer, say so and continue investigating; ' +
    'if they do not, restate your conclusion and you will be allowed to stop.';

  // The blocker section goes FIRST when present: "you cited something the room
  // has ruled wrong" outranks "there is unread news", and the two are separate
  // asks with separate exits.
  const blockerSection = blockerLines.length ? [BLOCKER_HEAD, ...blockerLines, BLOCKER_FOOT] : [];
  const eventSection = (body, n) => (total ? [head, ...body, tail(n), foot] : []);
  const assemble = (body, n) => [...blockerSection, ...eventSection(body, n)].filter(Boolean).join('\n');

  // Trim oldest-first until the whole message fits. The count of what was
  // dropped rises as lines leave, so the pointer stays truthful.
  let body = shown;
  let reason = assemble(body, omitted);
  while (reason.length > maxChars && body.length > 1) {
    body = body.slice(1);
    omitted = total - body.length;
    reason = assemble(body, omitted);
  }
  if (reason.length > maxChars) reason = `${reason.slice(0, maxChars - 1)}…`;

  return {
    block: true,
    reason,
    // Consume everything each session owed, not just what was spelled out —
    // the pointer above is what makes the omitted detail re-fetchable, exactly
    // as an in-band flush on a tool result does. Only EVENTS are consumed; a
    // quarantined citation is not made untrue by having been mentioned.
    consumes: owed.map((p) => ({ socketPath: p.socketPath, upTo: p.response.maxSeq })),
  };
}

/**
 * Blockers across every serve session in this workspace, de-duplicated.
 *
 * Two agent windows on one repo are two sessions with two cursors but often the
 * same incident, so the same quarantine can arrive twice. Dedupe on the
 * identity of the thing, not on the rendered line, so two sessions describing it
 * slightly differently still collapse to one.
 *
 * THE IDENTITY IS (incident, seq), NOT seq. Sockets are joined by WORKSPACE —
 * `workspaceKey(cwd)`, with no incident in it — so two `landfall serve`
 * processes in one checkout can be in two DIFFERENT incidents and both answer
 * one `peek`. Sequence numbers are small per-incident counters, so a collision
 * is ordinary rather than exotic, and a bare-seq dedupe silently dropped the
 * losing session's real blocker on first-answer-wins. That inverts this file's
 * own invariant: over-reporting is recoverable, under-reporting is the bug this
 * exists to prevent.
 *
 * An answer with no `incidentId` (an older serve process, before the field was
 * added to `peek`) falls back to its own socket path, so it never shares a
 * scope with anything but itself: not with another old process, and not with a
 * new one. During a partial upgrade — one old and one new serve process in the
 * same incident — the same quarantined item is therefore reported twice rather
 * than once. That is accepted, and deliberately not fixed by collapsing across
 * scopes when the content matches: an old answer carries no incident, so
 * matching it to a new one means guessing that equal sequence numbers mean the
 * same item, which is precisely the assumption that produced the cross-incident
 * drop above. A duplicate line is recoverable; a dropped blocker is the bug.
 * The duplicate disappears once every serve process in the workspace is new.
 */
function mergeBlockers(answers) {
  const quarantined = new Map();
  const contradictions = new Map();
  for (const a of answers) {
    const scope = a?.response?.incidentId ?? `socket:${a?.socketPath ?? '?'}`;
    const b = stopBlockers(a?.response?.attention);
    for (const q of b.quarantined) {
      const key = JSON.stringify([scope, q.targetSeq]);
      if (!quarantined.has(key)) quarantined.set(key, q);
    }
    for (const c of b.contradictions) {
      const key = JSON.stringify([scope, c.claimSeq]);
      if (!contradictions.has(key)) contradictions.set(key, c);
    }
  }
  return { quarantined: [...quarantined.values()], contradictions: [...contradictions.values()] };
}

/**
 * Whether the host is telling us this Stop already follows a block of ours.
 * Claude Code and Codex both pass the hook a JSON object on stdin;
 * `stop_hook_active` is the documented field. Absent or unparseable input is
 * treated as a first attempt — the conservative reading, since the cost of
 * being wrong is one extra nag rather than a silent conclusion.
 */
export function isStopHookActive(input) {
  const parsed = parseHookInput(input);
  if (!parsed) return false;
  if (parsed.stop_hook_active === true || parsed.stopHookActive === true) return true;
  // Cursor sends no `stop_hook_active`. Where it reports how many auto-followups
  // this turn has already had, that count is the same guard under another name:
  // anything above zero means this Stop follows one of ours. Where it does not,
  // this is simply never true and termination rests on consume-after-block plus
  // Cursor's own cap of 5 followups (protocol.mjs).
  return Number.isFinite(parsed.loop_count) && parsed.loop_count >= 1;
}

/**
 * Whether this Stop is a turn that actually CONCLUDED.
 *
 * Cursor's stop payload carries `status: 'completed' | 'aborted' | 'error'`.
 * A turn the user interrupted, or one that died, is not an agent walking away
 * from the room holding stale context — it is an agent that did not get to
 * finish. Nagging it adds a followup message to a turn nobody is reading, and
 * would consume the very events the next real conclusion needs to be told
 * about. Hosts that send no status (Claude Code, Codex) are unaffected: absent
 * means "concluded", the reading those hosts' Stop already implies.
 */
export function isConcludedTurn(input) {
  const status = parseHookInput(input)?.status;
  return status !== 'aborted' && status !== 'error';
}

function parseHookInput(input) {
  if (!input) return null;
  try {
    const parsed = typeof input === 'string' ? JSON.parse(input) : input;
    return parsed && typeof parsed === 'object' ? parsed : null;
  } catch {
    return null;
  }
}

/**
 * Run the stop hook. Returns `{ exitCode, blocked, reason }`.
 *
 * WHAT gets said is `buildStopDecision`'s; HOW is `protocol`'s (protocol.mjs) —
 * exit 2 with the digest on stderr for Claude Code and Codex, one JSON object
 * on stdout for Cursor. Under exit2 nothing is ever written to stdout, because
 * the host parses that channel; under cursor-json stdout is exactly where the
 * verdict goes and silence there is a parse error rather than an allow, so
 * EVERY exit path below emits — including the ones that decline to block.
 *
 * `emit` is called BEFORE the cursors move, so a crash mid-way leaves the
 * events queued rather than consumed-but-never-delivered. It is passed
 * `(text, {channel})` and is never called with empty text.
 */
export async function runStopHook({
  input,
  cwd,
  env,
  platform,
  protocol = EXIT2,
  query = queryHookSockets,
  send = sendToSocket,
  emit = (text, { channel } = {}) =>
    (channel === 'stdout' ? process.stdout : process.stderr).write(text),
} = {}) {
  const say = (decision) => {
    const verdict = renderStopVerdict(protocol, decision);
    if (verdict.text) emit(verdict.text, { channel: verdict.channel });
    return verdict;
  };
  const allow = () => {
    const { exitCode } = say({ block: false, reason: '' });
    return { exitCode, blocked: false, reason: '' };
  };

  if (isStopHookActive(input)) return allow();
  if (!isConcludedTurn(input)) return allow();

  let peeks = [];
  try {
    peeks = await query({ op: 'peek' }, { cwd, env, platform });
  } catch {
    // A hook that fails must not become a hook that blocks. No serve session,
    // no socket, a permissions problem — all mean "nothing known to be owed".
    return allow();
  }

  const decision = buildStopDecision(peeks);
  if (!decision.block) return allow();

  const { exitCode } = say(decision);

  // A failed consume must never suppress the block that was already emitted —
  // the events staying queued is the recoverable outcome.
  await Promise.all(
    decision.consumes.map(({ socketPath, upTo }) =>
      Promise.resolve(send(socketPath, { op: 'consume', upTo })).catch(() => null),
    ),
  );

  return { exitCode, blocked: true, reason: decision.reason };
}
