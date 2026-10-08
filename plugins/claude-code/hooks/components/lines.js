// Lines of investigation (proposal item 13, FR-13): who is on which lead, so
// two people do not chase the same one. The lines belong to the people who
// hold them, so People draws them (roster.js: a person's line beside their
// name, the rest under `Lines`, `c: claim a line…`, `e: release`) and this
// file is its helpers: the room's lines from `landfall watch` (status.lines),
// the words a claim or a release answers with, and the two runs of the CLI
// as the person (`landfall lines claim|release`). The band shows one dim row
// while the room has claimed lines.
//
// For console.js: `/landfall lines <label>` calls claimLine(io, label) and
// answers its text, then redraws People.

import { HOST, ago, clip, currentRoom } from '../core.js'

// The tab has no command or pane of its own.
export function install(on) {}

// band: one dim row while the room has claimed lines.
export async function band(io, e, k) {
  const words = linesWords(currentRoom())
  if (!words) return null
  return [k.text(clip('Lines: ' + words, k.width), { key: 'lines', dimColor: true })]
}

export function onSnapshot(io, snap, prev) {}

export function start(io) {}

// tick runs every TICK_MS while the session lives: nothing to do here.
export function tick(io, nowMs) {}

// cli runs `landfall <args> --host claude-code` and answers its one JSON line.
async function cli(io, args) {
  return io.run([...args, '--host', HOST], { timeoutMs: 20000 })
}

// claimLine claims a line as the person. Answers the CLI's answer and the words
// that say what became of it (a toast, or a command's text).
export async function claimLine(io, label) {
  const r = currentRoom()
  label = String(label || '').trim()
  if (!r) return { ok: false, text: 'This folder is not in a war room. Open a share link from the room, or join one from Incidents.' }
  if (!label) return { ok: false, text: 'Say which line you are on: /landfall lines <label>.' }
  const got = await cli(io, ['lines', 'claim', '--room', r.roomKey, '--label', label])
  return { ok: !!got.ok, text: claimWords(got, label), answer: got }
}

// releaseLine releases a line the person holds.
export async function releaseLine(io, line) {
  const r = currentRoom()
  if (!r || !line || !line.claimId) return { ok: false, text: 'There is no line to release.' }
  const got = await cli(io, ['lines', 'release', '--room', r.roomKey, '--claim', line.claimId])
  if (got.ok) return { ok: true, text: got.note ? String(got.note) : 'You released the line ' + lineLabel(line) + '.', answer: got }
  return { ok: false, text: 'Line not released: ' + clip(got.error || 'no answer', 200), answer: got }
}

// claimWords is what a claim's answer says to the person. When someone else
// already holds the line, the CLI's sentence names them and is said as it is
// ("dave already holds this line. Help them, or claim another.").
export function claimWords(got, label) {
  if (got.ok) return 'You claimed the line ' + (got.label || label) + '. The room sees it.'
  if (got.heldBy) return String(got.error || got.heldBy + ' already holds this line. Help them, or claim another.')
  return 'Line not claimed: ' + (got.error || 'no answer')
}

// roomLines is the room's claimed lines from `landfall watch`.
export function roomLines(r) {
  const lines = r && r.status && Array.isArray(r.status.lines) ? r.status.lines : []
  return lines.filter((l) => l && lineLabel(l))
}

// lineLabel reads the label (review #5) or the first contract's `line`.
export function lineLabel(l) {
  return String(l.label || l.line || l.lineKey || '')
}

// linesWords is the band's row: "eu-west-1 5xx (dave) · origin pool (you)".
export function linesWords(r) {
  return roomLines(r)
    .map((l) => lineLabel(l) + ' (' + (l.you ? 'you' : l.owner || 'someone') + ')')
    .join(' · ')
}

// holds says a person holds a line: it is theirs by owner name, or yours.
export function holds(p, l) {
  return p.you ? !!l.you : !l.you && String(l.owner || '').trim().toLowerCase() === String(p.name || '').trim().toLowerCase()
}

// linesOf is the lines one person holds.
export function linesOf(r, p) {
  return roomLines(r).filter((l) => holds(p, l))
}

// loneLines are the room's lines nobody listed in `people` holds (an agent's, or a
// person who left), as `Lines` draws them.
export function loneLines(r, people) {
  return roomLines(r).filter((l) => !people.some((p) => holds(p, l)))
}

// lineWords is one such line: "origin pool · alice · 12m".
export function lineWords(l) {
  return [lineLabel(l), l.you ? 'you' : l.owner || 'someone', ago(l.ageMs)].filter(Boolean).join(' · ')
}

// linesText is the lines as text, where no pane can be drawn.
export function linesText(r) {
  const lines = roomLines(r)
  if (lines.length === 0) return 'Nobody has claimed a line yet. Type /landfall lines and what you are on to claim one.'
  const out = ['Lines of investigation']
  for (const l of lines) out.push('  ' + lineLabel(l) + ' · ' + [l.you ? 'you' : l.owner || 'someone', ago(l.ageMs)].filter(Boolean).join(' · '))
  return out.join('\n')
}
