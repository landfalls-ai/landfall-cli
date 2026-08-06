// attention.mjs — what the room is waiting on from THIS agent, and how that
// reaches it (landfalls-ai/landfall#252, story #203).
//
// The projection itself lives on the server (`GET …/vetting/attention`), which
// is the whole point: quorum, admission and decay are the platform's rules,
// and a bridge that re-derived "does this still need my vote?" locally would be
// a second implementation of them — drifting, and drifting in the direction of
// telling an investigator their vote is not needed when it is. Everything here
// is presentation and a decision about when to interrupt.
//
// TWO CHANNELS, ONE SOURCE.
//
//   tier 0 (piggyback) — a "vote requested" line prepended to a tool result.
//                        Cheap, unmissable, and it costs the agent nothing:
//                        it is already reading that result.
//   tier 1 (Stop hook) — a refusal to conclude while something the agent
//                        RELIED ON has been quarantined, or a claim that
//                        contradicts the admitted record is still unanswered.
//
// The split is deliberate. A vote request is an invitation and must never stop
// an agent from finishing its work — most claims do not need this particular
// participant. A conclusion drawn on quarantined evidence is a different thing:
// the room has already ruled that content wrong, and letting the agent state
// its answer anyway is the failure the whole vetting loop exists to prevent.
//
// EVERY FUNCTION HERE IS PURE. No clock, no I/O, no module state — so the
// interrupt policy is testable without a war room, which is what keeps a change
// to it honest.

/** Event type prefixes whose arrival can change what awaits this agent. */
const ATTENTION_PREFIXES = ['claim.', 'context.'];

/**
 * Does this room event potentially change the attention projection? Used to
 * refresh on arrival rather than on a timer — the socket already pushes these
 * in milliseconds, so a poll would be both slower and noisier.
 */
export function touchesAttention(evt) {
  const type = String(evt?.type ?? '');
  return ATTENTION_PREFIXES.some((p) => type.startsWith(p));
}

/** Vote-request lines spelled out in full on one tool result. */
const VOTE_LINES_MAX = 3;

/** Trim an untrusted statement to one bounded line. */
function oneLine(s, max = 120) {
  const flat = String(s ?? '').replace(/\s+/g, ' ').trim();
  return flat.length <= max ? flat : `${flat.slice(0, max - 1)}…`;
}

/**
 * The key a vote request is remembered by, so the same claim is not re-announced
 * on every single tool call. Staleness is part of the key on purpose: a claim
 * about to lapse is genuinely new information, and it is the last moment a
 * position can still count.
 */
export function voteKey(v) {
  return `${v?.claimSeq}:${v?.stale ? 'stale' : 'fresh'}`;
}

/**
 * Build the piggyback block for a tool result.
 *
 * `notified` is the set of keys already announced to this session; it is READ
 * here and returned as `keys`, never mutated, so a caller that fails to deliver
 * the block does not lose the announcement.
 *
 * Returns `{ text, keys }` — `text` is '' when there is nothing new to say, so
 * an ordinary tool result is left untouched.
 */
export function voteRequestBlock(attention, { notified = new Set(), max = VOTE_LINES_MAX } = {}) {
  const awaited = Array.isArray(attention?.votesAwaited) ? attention.votesAwaited : [];
  // Already sorted by urgency server-side; keep that order rather than imposing
  // a second one that could disagree with it.
  const fresh = awaited.filter((v) => typeof v?.claimSeq === 'number' && !notified.has(voteKey(v)));
  if (!fresh.length) return { text: '', keys: [] };

  const shown = fresh.slice(0, max);
  const omitted = fresh.length - shown.length;
  const lines = shown.map((v) => {
    const who = v.authoredBy ? ` from ${v.authoredBy}${v.authorIsAgent ? ' (agent)' : ''}` : '';
    const need = v.shortfall?.text ? ` — ${oneLine(v.shortfall.text, 100)}` : '';
    const when = v.stale
      ? ' — PAST its freshness window'
      : typeof v.expiresInMs === 'number' && v.expiresInMs > 0
        ? ` — ${formatDuration(v.expiresInMs)} left`
        : '';
    return `⚠ vote requested: claim #${v.claimSeq} "${oneLine(v.statement)}"${who}${need}${when}`;
  });
  if (omitted) lines.push(`+${omitted} more claim(s) awaiting your position — see the war room.`);
  lines.push(
    'Take a position with corroborate_claim or contest_claim when you have evidence either way; ' +
      'your vote is a position, never a decision, so vote and carry on.',
  );

  return { text: lines.join('\n'), keys: shown.map(voteKey) };
}

/** `2h 15m`, `12m` — no locale, no dependency. */
export function formatDuration(ms) {
  if (!Number.isFinite(ms) || ms <= 0) return '0m';
  const minutes = Math.floor(ms / 60_000);
  const hours = Math.floor(minutes / 60);
  if (hours > 0) return `${hours}h ${minutes % 60}m`;
  return `${minutes}m`;
}

/**
 * The two things that must stop a conclusion.
 *
 * (a) QUARANTINED CONTEXT THIS AGENT IS ATTACHED TO. The ticket says "has cited
 *     a now-quarantined seq"; this also covers an item the agent AUTHORED and
 *     had quarantined, because the failure is identical — an answer resting on
 *     content the room has ruled wrong — and the narrower reading would let the
 *     more common case through. A merely `flagged` item never blocks: a flag is
 *     an open question, and refusing every conclusion while one is open would
 *     make any participant able to freeze an investigation.
 *
 * (b) AN UNANSWERED CONTRADICTION. A staged claim awaiting this agent's
 *     position whose admission is blocked on contradicting the admitted record.
 *     Someone is asserting something incompatible with what the room already
 *     established, and this agent has not said which it believes — concluding
 *     without answering that is the definition of leaving it unaddressed.
 *     Ordinary vote requests do NOT block; most claims do not need this
 *     participant, and a bridge that stopped every conclusion until the queue
 *     was empty would be uninstalled within a day.
 */
export function stopBlockers(attention) {
  const flagged = Array.isArray(attention?.flaggedOwnContext) ? attention.flaggedOwnContext : [];
  const awaited = Array.isArray(attention?.votesAwaited) ? attention.votesAwaited : [];

  const quarantined = flagged.filter((f) => f?.state === 'quarantined');
  const contradictions = awaited.filter((v) => {
    const seqs = v?.shortfall?.missing?.contradiction;
    return Array.isArray(seqs) && seqs.length > 0;
  });

  return { quarantined, contradictions };
}

/** True when anything in `blockers` should stop a conclusion. */
export function hasStopBlockers(blockers) {
  return Boolean(blockers?.quarantined?.length || blockers?.contradictions?.length);
}

/**
 * The lines a Stop hook writes for `blockers`. Says what is wrong and what
 * would resolve it — a refusal that does not name its own exit is a wall.
 */
export function describeStopBlockers(blockers, { max = 6 } = {}) {
  const lines = [];
  for (const f of blockers?.quarantined ?? []) {
    const cited = Array.isArray(f.citedByClaimSeqs) && f.citedByClaimSeqs.length
      ? ` (cited by your claim${f.citedByClaimSeqs.length > 1 ? 's' : ''} #${f.citedByClaimSeqs.join(', #')})`
      : '';
    const why = f.reason ? `: "${oneLine(f.reason, 100)}"` : '';
    lines.push(
      `• seq ${f.targetSeq} — the room QUARANTINED this ${f.targetKind || 'item'} you ${f.relation ?? 'used'}${cited}${why}`,
    );
  }
  for (const v of blockers?.contradictions ?? []) {
    const against = (v.shortfall.missing.contradiction ?? []).map((s) => `#${s}`).join(', ');
    lines.push(
      `• claim #${v.claimSeq} "${oneLine(v.statement)}" contradicts admitted claim(s) ${against} and is still awaiting your position`,
    );
  }
  const shown = lines.slice(0, max);
  if (lines.length > shown.length) shown.push(`+${lines.length - shown.length} more — call get_updates for the rest.`);
  return shown;
}

/** The header and footer wrapping {@link describeStopBlockers}. */
export const BLOCKER_HEAD = '⚠ Do not conclude yet — the room has ruled on context your answer may rest on:';
export const BLOCKER_FOOT =
  'Address these before concluding: drop or correct anything that rested on quarantined context, and ' +
  'take a position on the contradiction with corroborate_claim or contest_claim. If your answer genuinely ' +
  'does not depend on them, say so explicitly and you will be allowed to stop.';
