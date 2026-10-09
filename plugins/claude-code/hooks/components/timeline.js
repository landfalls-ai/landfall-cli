// The incident timeline and shared context ledger: the console's Timeline tab (spec 4.4; proposal
// item 08, FR-08).
//
// `landfall timeline` answers the room's events oldest first, filtered by kind on the CLI's side.
// The tab draws a chip row (a f s b p o, identical Buttons, the active one `▸ <kind>`), `m` reads
// further back, and one row per event (time, a glyph in its tone, what happened, who); a press on
// a row quotes it into the prompt as a draft. Nothing sends.
//
// It is warm from the moment the console opens (spec 2.5): a new room event (the watch stream's
// maxSeq moved on) reads the newest page again and merges it over what is shown, so rows read
// further back with `m` stay. A failed read keeps the rows and says they are stale; the footer
// says how old they are. The Timeline badge counts what is new since the tab was last drawn.

import { HOST, consoleState, currentOf, currentRoom, reading, roomName } from '../core.js'
import { closed, drawn, keep, livePane, opened as markOpen } from '../live.js'
import { textCells } from '../kit.js'
import { clipText, hhmm, toneColor } from '../views.js'
import { chipRow, footerOf, keyButton, nowOf, resetLive, roomKeyOf } from './tabparts.js'

export const LIMIT = 50

// The chips, in order, with their keys.
export const KINDS = [
  { kind: 'all', label: 'all', hotkey: 'a' },
  { kind: 'findings', label: 'findings', hotkey: 'f' },
  { kind: 'status', label: 'status', hotkey: 's' },
  { kind: 'beacon', label: 'Beacon', hotkey: 'b' },
  { kind: 'people', label: 'people', hotkey: 'p' },
  { kind: 'other', label: 'other', hotkey: 'o' },
]

const tl = {
  roomKey: '', // the room these rows are of
  kind: 'all',
  events: [],
  hasMore: false,
  oldestSeq: null, // the CLI's oldestSeq: where `m` reads back from
  error: '',
  loading: false,
  loaded: false,
  again: false, // a new event came while a read ran: read the newest page after it
  seenSeq: -1, // the room's maxSeq when the newest page was last read
  shownSeq: -1, // the room's maxSeq when the tab was last drawn: the badge counts past it
  argsApplied: '', // the chip a `/landfall timeline <chip>` already chose
  latest: [], // the newest unfiltered rows, for Home's Latest block
}

// The live state (live.js): open or not, when the last good read was, stale or not. The rows
// themselves stay in `tl`.
const lp = livePane()

// forRoom drops what was read of another room: a different incident starts the tab over.
function forRoom(r) {
  const key = roomKeyOf(r)
  if (key === tl.roomKey) return
  Object.assign(tl, { roomKey: key, kind: 'all', events: [], hasMore: false, oldestSeq: null, error: '', loading: false, loaded: false, again: false, seenSeq: -1, shownSeq: -1, argsApplied: '', latest: [] })
  resetLive(lp)
}

// No hooks of its own: the console owns the pane and the `/landfall` command.
export function install(on) {}

export async function band(io, e, k) {
  return null
}

// onSnapshot: a new room event reads the newest page into an open timeline.
export function onSnapshot(io, snap, prev) {
  const r = currentOf(snap)
  forRoom(r)
  if (!lp.open || !tl.loaded || !reading('timeline')) return
  if (r && typeof r.maxSeq === 'number' && r.maxSeq > tl.seenSeq) void loadTimeline(io, false, true)
}

export function start(io) {}

// tick stops the reads once the console is closed, and reads an event the snapshot could not (a
// read was running).
export async function tick(io, nowMs) {
  if (lp.open && !reading('timeline')) closed(lp)
  const r = currentRoom()
  const moved = roomKeyOf(r) !== tl.roomKey
  forRoom(r)
  if (!lp.open || !reading('timeline')) return
  if (moved) return loadTimeline(io, false)
  if (tl.loaded && !tl.loading && r && typeof r.maxSeq === 'number' && r.maxSeq > tl.seenSeq) void loadTimeline(io, false, true)
}

// warm starts the timeline's reads when the console opens (and reads again when asked).
export async function warm(io) {
  forRoom(currentRoom())
  markOpen(lp)
  await loadTimeline(io, false)
}

export async function refresh(io) {
  await warm(io)
}

// badge is the Timeline segment's count: news lines newer than the room seq the tab was last
// drawn at (the watch digest's `#seq` lines; `r.count` when every digest line is newer, since the
// digest is capped); none at zero. Drawing the tab clears it.
export function badge() {
  const r = currentRoom()
  if (!r) return null
  forRoom(r)
  const digest = r.digest || []
  const newer = digest.filter((line) => {
    const m = /^#(\d+) /.exec(line)
    return m && Number(m[1]) > tl.shownSeq
  }).length
  const n = digest.length > 0 && newer === digest.length ? Math.max(newer, r.count || 0) : newer
  return n > 0 ? n : null
}

// The newest rows of the unfiltered timeline, oldest first, for Home's Latest block. Latest is
// room news: rows the tab files under `other` (plumbing such as a refused signal read or a memory
// run starting) are skipped, and only when nothing else exists are they shown.
export function latestRows(n) {
  forRoom(currentRoom())
  const news = tl.latest.filter((e) => e && e.kind !== 'other')
  return (news.length > 0 ? news : tl.latest).slice(-n)
}

// How many unfiltered rows are kept for Latest: enough that dropping `other` still leaves three.
const LATEST_KEPT = 12

// The desktop's time slot: `00:00` and a cell, in cells (the time is proportional text off the terminal).
export const TIME_SLOT = Math.ceil(textCells('00:00')) + 1

// The live read's state (live.js), so Home can say how old its Latest block is.
export function readState() {
  return lp
}

// The Timeline tab has no letters beyond its chips, which are part of the body.
export function keys(k, io, nowMs, args) {
  return []
}

// tab is the Timeline tab's body (spec 4.4): the chips, `m: earlier events` while there is more,
// one row per event and the hint. The console draws the footer and keys. `args` is `/landfall timeline <chip>`.
export function tab(k, io, nowMs, args) {
  const { Box, Text } = k.els
  drawn(lp)
  const r = currentRoom()
  forRoom(r)
  applyArgs(io, args)
  if (r && typeof r.maxSeq === 'number') tl.shownSeq = r.maxSeq
  const rows = []
  rows.push(
    chipRow(
      k,
      // The docked desktop draws no key chip beside a kind (six chips with letters wrap to two rows,
      // round 2 review, issue 13); the terminal keeps its letters.
      KINDS.map((c) => ({ key: 'kind-' + c.kind, name: c.label, hotkey: k.terminal ? c.hotkey : undefined, active: c.kind === tl.kind, onPress: () => setKind(io, c.kind) })),
      'tl-kinds',
    ),
  )
  if (tl.hasMore && !tl.error) {
    rows.push(keyButton(k, { key: 'tl-more', label: tl.loading ? 'reading…' : k.terminal ? 'earlier events' : 'Earlier events', hotkey: 'm', dim: true, onPress: () => loadTimeline(io, true) }))
  }
  if (tl.error) rows.push(k.text(clipText(tl.error, k.width), { key: 'tl-err' }))
  else if (!tl.loaded) rows.push(k.text('Reading the timeline…', { key: 'tl-empty', dimColor: true }))
  else if (tl.events.length === 0) rows.push(k.text(tl.kind === 'all' ? 'Nothing on the timeline yet.' : 'Nothing of this kind yet.', { key: 'tl-none', dimColor: true }))
  const space = Math.max(20, k.width - 10)
  for (const ev of tl.events) {
    const mark = Text({ key: 'g', color: toneColor(ev.tone || 'neutral'), children: [ev.glyph || '·'] })
    // Off the terminal the glyphs differ in width, so each sits centered in one fixed slot and every
    // event's text starts at the same x (round 2 review, issue 13).
    const glyph = k.terminal ? mark : Box({ key: 'gs', width: 2, flexShrink: 0, justifyContent: 'center', children: [mark] })
    const label = clipText(eventWords(ev), space)
    const press = k.els.Button({ key: 'ev-' + ev.seq, label, plain: true, onPress: () => quote(io, ev) })
    // The time is proportional text, so its width moves with its digits and so did the text after it
    // (1447 to 1456 px, round 3 review, issue 2): off the terminal it sits in a box of one fixed width
    // (`00:00` and a cell), then the glyph slot, then the text in the box that takes the rest.
    const time = Text({ key: 't', dimColor: true, children: [hhmm(ev.at)] })
    const when = k.terminal ? time : Box({ key: 'tw', width: TIME_SLOT, flexShrink: 0, children: [time] })
    const words = k.terminal ? press : Box({ key: 'xw', flexGrow: 1, flexShrink: 1, minWidth: 0, children: [press] })
    const line = k.row([when, glyph, words], 'row-' + ev.seq, 1)
    if (k.rich && ev.detail) rows.push(k.col([line, k.text(clipText(ev.detail, space), { key: 'd', dimColor: true })], 'evc-' + ev.seq))
    else rows.push(line)
  }
  if (tl.events.length > 0) rows.push(k.text('Enter on a row quotes it into your prompt', { key: 'tl-hint', dimColor: true }))
  return rows
}

// footer is the console's footer for this tab: `live · updated 4s ago`, or null before the first read.
export function footer(k, nowMs) {
  return footerOf(k, lp, nowMs, currentRoom(), 'tl-live')
}

// applyArgs takes `/landfall timeline <chip>` once: the chip is chosen and read, then not chosen
// again on every draw (the person's own chip presses win after that).
function applyArgs(io, args) {
  if (args == null || args === '') {
    tl.argsApplied = ''
    return
  }
  const asked = String(typeof args === 'object' ? args.chip || '' : args).trim().toLowerCase()
  if (consoleState.args === args) consoleState.args = null
  if (!asked || asked === tl.argsApplied) return
  tl.argsApplied = asked
  if (KINDS.some((x) => x.kind === asked) && asked !== tl.kind) {
    chooseKind(asked)
    void loadTimeline(io, false)
  }
}

function chooseKind(kind) {
  tl.kind = kind
  tl.events = []
  tl.hasMore = false
  tl.oldestSeq = null
  tl.loaded = false
  tl.error = ''
}

async function setKind(io, kind) {
  if (tl.kind === kind && tl.loaded) return
  chooseKind(kind)
  await loadTimeline(io, false)
}

// loadTimeline reads the newest page for the chosen kind, or with `more` the
// page before the oldest row shown, and keeps the rows oldest first. `live`
// is a refresh on a new event: the newest page is merged over the rows shown
// (the rows it covers replaced, so a Beacon run folded anew is one row), and
// a failure keeps them, marked stale.
async function loadTimeline(io, more, live) {
  if (tl.loading) {
    if (live) tl.again = true
    return
  }
  tl.loading = true
  tl.again = false
  io.invalidate()
  const kind = tl.kind
  const r = currentRoom()
  const roomKey = tl.roomKey
  const seenSeq = r && typeof r.maxSeq === 'number' ? r.maxSeq : -1
  const before = more ? (tl.oldestSeq ?? (tl.events.length > 0 ? tl.events[0].seq : undefined)) : undefined
  const startedAt = await nowOf(io, lp.triedAt)
  if (!more) lp.triedAt = startedAt
  const answer = await io.run(timelineArgs(r, kind, before), { timeoutMs: 30000 })
  tl.loading = false
  if (roomKey !== tl.roomKey) {
    // The room changed while this read ran: its rows are another room's.
    io.invalidate()
    return
  }
  if (kind !== tl.kind) {
    // The person chose another chip while this read ran: read that one now.
    io.invalidate()
    await loadTimeline(io, false)
    return
  }
  if (!answer.ok) {
    if (live && tl.loaded) keep(lp, answer, startedAt)
    else tl.error = answer.error || 'The timeline could not be read.'
  } else {
    tl.error = ''
    const page = (answer.events || []).slice().sort((x, y) => x.seq - y.seq)
    if (more) {
      const have = new Set(tl.events.map((x) => x.seq))
      tl.events = [...page.filter((x) => !have.has(x.seq)), ...tl.events]
    } else if (live && tl.loaded && page.length > 0) {
      tl.events = mergeNewest(tl.events, page)
    } else tl.events = page
    if (!more) {
      tl.seenSeq = seenSeq
      keep(lp, answer, startedAt)
      if (kind === 'all') tl.latest = tl.events.slice(-LATEST_KEPT)
    }
    if (!(live && tl.loaded)) {
      tl.hasMore = !!answer.hasMore
      if (typeof answer.oldestSeq === 'number' && (!more || tl.oldestSeq == null || answer.oldestSeq < tl.oldestSeq)) tl.oldestSeq = answer.oldestSeq
      else if (!more) tl.oldestSeq = page.length > 0 ? page[0].seq : null
    }
    tl.loaded = true
    if (!more && kind !== 'all') void loadLatest(io, r)
  }
  io.invalidate()
  if (tl.again) await loadTimeline(io, false, true)
}

// loadLatest keeps Home's Latest block current while a chip filters the tab: the newest
// unfiltered rows, read on their own.
async function loadLatest(io, r) {
  const answer = await io.run(timelineArgs(r, 'all', undefined, LATEST_KEPT), { timeoutMs: 30000 })
  if (answer.ok) tl.latest = (answer.events || []).slice().sort((x, y) => x.seq - y.seq)
}

// mergeNewest lays the newest page over the rows shown: the rows the page
// covers (its oldest seq and after) are replaced by it, the older ones stay.
export function mergeNewest(rows, page) {
  const from = page[0].seq
  return [...rows.filter((x) => x.seq < from), ...page]
}

// quote drafts the event into the prompt, at the cursor: "> 15:52 <text>".
async function quote(io, ev) {
  await io.fill(quoteText(ev), 'insert')
}

export function quoteText(ev) {
  return '> ' + hhmm(ev.at) + ' ' + String(ev.text || '') + '\n\n'
}

export function timelineArgs(r, kind, before, limit) {
  const args = ['timeline', '--host', HOST]
  if (r && r.roomKey) args.push('--room', r.roomKey)
  args.push('--limit', String(limit || LIMIT))
  if (kind && kind !== 'all') args.push('--kind', kind)
  if (before != null) args.push('--before', String(before))
  return args
}

// eventWords is an event's text and who did it: "status investigating · carol".
function eventWords(ev) {
  const text = String(ev.text || '')
  return ev.who && !text.includes(ev.who) ? text + ' · ' + ev.who : text
}

// timelineText is /timeline's answer where no pane can be drawn.
export function timelineText() {
  if (tl.error) return tl.error
  const r = currentRoom()
  const head = 'Timeline' + (r ? ' · ' + roomName(r) : '') + (tl.kind !== 'all' ? ' · ' + tl.kind : '')
  if (tl.events.length === 0) return head + '\n  Nothing on the timeline yet.'
  const lines = tl.events.map((ev) => '  ' + hhmm(ev.at) + ' ' + (ev.glyph || '·') + ' ' + eventWords(ev))
  if (tl.hasMore) lines.unshift('  (earlier events not shown)')
  return [head, ...lines].join('\n')
}

// text is the Timeline tab's answer where no pane can be placed (`claude -p`): it reads the
// timeline (with the chip asked for) and says it.
export async function text(io, args) {
  forRoom(currentRoom())
  const asked = String(args || '').trim().toLowerCase()
  if (KINDS.some((x) => x.kind === asked) && asked !== tl.kind) chooseKind(asked)
  await loadTimeline(io, false)
  return timelineText()
}
