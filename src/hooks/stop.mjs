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
import { queryHookSockets, sendToSocket } from './socket.mjs';

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
  const owed = (Array.isArray(peeks) ? peeks : []).filter(
    (p) => (p?.response?.count ?? 0) > 0 || (p?.response?.dropped ?? 0) > 0,
  );
  const total = owed.reduce((n, p) => n + (p.response.count ?? 0) + (p.response.dropped ?? 0), 0);
  if (!total) return { block: false, reason: '', consumes: [] };

  // Union across sessions (see queryHookSockets): over-reporting a sibling
  // window's context is recoverable, under-reporting is the bug this prevents.
  const lines = owed.flatMap((p) => p.response.digest ?? []);
  const resumeSeq = Math.min(...owed.map((p) => p.response.cursor ?? -1));
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

  const assemble = (body, n) => [head, ...body, tail(n), foot].filter(Boolean).join('\n');

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
    // as an in-band flush on a tool result does.
    consumes: owed.map((p) => ({ socketPath: p.socketPath, upTo: p.response.maxSeq })),
  };
}

/**
 * Whether the host is telling us this Stop already follows a block of ours.
 * Claude Code and Codex both pass the hook a JSON object on stdin;
 * `stop_hook_active` is the documented field. Absent or unparseable input is
 * treated as a first attempt — the conservative reading, since the cost of
 * being wrong is one extra nag rather than a silent conclusion.
 */
export function isStopHookActive(input) {
  if (!input) return false;
  try {
    const parsed = typeof input === 'string' ? JSON.parse(input) : input;
    return parsed?.stop_hook_active === true || parsed?.stopHookActive === true;
  } catch {
    return false;
  }
}

/**
 * Run the stop hook. Returns `{ exitCode, blocked }` — exit 2 with the digest on
 * stderr is the one blocking shape every host in HOOK_EVENTS understands
 * (Claude Code and Codex both feed a Stop hook's stderr back to the model on
 * exit 2). Nothing is ever written to stdout: the host parses that channel.
 *
 * `emit` is called BEFORE the cursors move, so a crash mid-way leaves the
 * events queued rather than consumed-but-never-delivered.
 */
export async function runStopHook({
  input,
  cwd,
  env,
  platform,
  query = queryHookSockets,
  send = sendToSocket,
  emit = (text) => process.stderr.write(`${text}\n`),
} = {}) {
  if (isStopHookActive(input)) return { exitCode: 0, blocked: false, reason: '' };

  let peeks = [];
  try {
    peeks = await query({ op: 'peek' }, { cwd, env, platform });
  } catch {
    // A hook that fails must not become a hook that blocks. No serve session,
    // no socket, a permissions problem — all mean "nothing known to be owed".
    return { exitCode: 0, blocked: false, reason: '' };
  }

  const decision = buildStopDecision(peeks);
  if (!decision.block) return { exitCode: 0, blocked: false, reason: '' };

  emit(decision.reason);

  // A failed consume must never suppress the block that was already emitted —
  // the events staying queued is the recoverable outcome.
  await Promise.all(
    decision.consumes.map(({ socketPath, upTo }) =>
      Promise.resolve(send(socketPath, { op: 'consume', upTo })).catch(() => null),
    ),
  );

  return { exitCode: 2, blocked: true, reason: decision.reason };
}
