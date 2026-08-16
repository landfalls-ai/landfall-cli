// context-render.mjs — pure rendering for the war-room context frame (feature
// 116, cross-repo-followup.md). Three render functions, one per new server
// read: `renderFrame` (GET .../edge/context/frame), `renderDelta`
// (GET .../edge/context/delta), `renderSearchHits` (GET .../edge/context/search).
// No I/O, no clock — same testable-without-a-network discipline as
// narrate.mjs/attention.mjs.

/** `active`/`away`/`unknown` — `active` is `undefined` on the wire when the
 * server's presence store could not be read (never fabricated as either
 * known state — see the monorepo's data-model.md validation rules). */
function presenceWord(active) {
  if (active === true) return 'active';
  if (active === false) return 'away';
  return 'unknown';
}

function participantLabel(p) {
  const name = p?.displayName || 'Participant';
  const label = p?.edgeAgentLabel ? ` · ${p.edgeAgentLabel}` : '';
  return `${name}${label} (${presenceWord(p?.active)})`;
}

function briefLines(items, heading) {
  if (!Array.isArray(items) || !items.length) return [];
  return [`${heading}:`, ...items.map((it) => `  #${it.seq} ${it.statement} — ${it.by}`)];
}

/**
 * Render a `ContextFrame` (data-model.md) as the text `join_war_room`/
 * `get_brief` return — a real, usable brief in one call (SC-002), replacing
 * the bare event-type histogram this tool used to render.
 */
export function renderFrame(frame) {
  if (!frame || typeof frame !== 'object') return 'No incident context available.';
  const inc = frame.incident ?? {};
  const head = [inc.title || '(untitled incident)', inc.severity, inc.status].filter(Boolean).join(' · ');
  const lines = [head];
  if (inc.alertSource) lines.push(`Alert source: ${inc.alertSource}`);

  const brief = frame.brief ?? {};
  const established = briefLines(brief.established, 'Established');
  const open = briefLines([...(brief.workingTheory ?? []), ...(brief.open ?? [])], 'Open');
  if (established.length || open.length) {
    lines.push('', ...established, ...(established.length && open.length ? [''] : []), ...open);
  } else {
    lines.push('', 'No findings or open items yet — this is a genuinely fresh incident.');
  }

  const participants = Array.isArray(frame.participants) ? frame.participants : [];
  if (participants.length) {
    lines.push('', `Participants: ${participants.map(participantLabel).join(', ')}`);
  }

  const freshness = typeof frame.freshnessMs === 'number' ? `${Math.max(0, Math.round(frame.freshnessMs / 1000))}s stale` : 'freshness unknown';
  lines.push('', `As of seq ${frame.asOfSeq ?? '?'} (${freshness}).`);
  return lines.join('\n');
}

/** The seq to resume delivery from — the frame's own cursor, so a caller does
 * not need to separately track "the highest seq in the brief". */
export function frameCursor(frame) {
  return typeof frame?.asOfSeq === 'number' ? frame.asOfSeq : undefined;
}

/**
 * Render a `FrameDelta` (data-model.md) — addressed/substantive items in
 * full, routine activity as a count only (FR-007/FR-008). Returns `''` when
 * there is nothing owed, so a caller can drop it from a result untouched.
 */
export function renderDelta(delta) {
  const items = Array.isArray(delta?.items) ? delta.items : [];
  const routineCount = delta?.routineCount ?? 0;
  if (!items.length && !routineCount) return '';

  const lines = [`⚠ ${items.length} update(s) from other investigators since your last check:`];
  for (const it of items) {
    const tag = it.class === 'addressed' ? '➤' : '•';
    const who = it.by ? ` [${it.by}]` : '';
    lines.push(`${tag} #${it.seq} ${it.type}${who}${it.summary ? ` — ${it.summary}` : ''}`);
  }
  if (routineCount) lines.push(`(+${routineCount} routine update(s) — counted, not shown)`);
  return lines.join('\n');
}

/** Render real search hits (FR-005/SC-006) — never a bare count. */
export function renderSearchHits(result, query) {
  const hits = Array.isArray(result?.hits) ? result.hits : [];
  if (!hits.length) return `0 matching event(s) for "${query}".`;
  const lines = [`${hits.length} matching event(s) for "${query}":`];
  for (const h of hits) lines.push(`#${h.seq} ${h.type} [${h.by}] — ${h.snippet}`);
  return lines.join('\n');
}
