// The incident timeline and shared context ledger: /timeline (proposal item 08, FR-08).
//
// `landfall timeline` answers the room's events oldest first, filtered by
// kind on the CLI's side. The pane draws one row per event (time, a glyph in
// its tone, what happened, who), chips filter by kind, `m` reads further back,
// and a press on a row quotes it into the prompt as a draft. Nothing sends.
//
// While open the timeline is live (live.md FR-L2): a new room event (the
// watch stream's maxSeq moved on) reads the newest page again and merges it
// over what is shown, so rows read further back with `m` stay. A failed read
// keeps the rows and says they are stale; the last line says how old they are.

import { HOST, addCommand, currentRoom, parseAnswer, room, roomName } from '../core.js'
import { kit } from '../kit.js'
import { closed, drawn, keep, liveFooter, livePane, notLive, opened as markOpen } from '../live.js'
import { clipText, hhmm, toneColor } from '../views.js'

export const PANE = 'landfall-timeline'
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
  kind: 'all',
  events: [],
  hasMore: false,
  oldestSeq: null, // the CLI's oldestSeq: where `m` reads back from
  error: '',
  loading: false,
  loaded: false,
  again: false, // a new event came while a read ran: read the newest page after it
  seenSeq: -1, // the room's maxSeq when the newest page was last read
}

// The live state (live.js): open or not, when the last good read was, stale
// or not. The rows themselves stay in `tl`.
const lp = livePane()

export function install(on) {
  addCommand({ name: 'timeline', description: "Show the war room's timeline", argumentHint: '[findings|status|beacon|people|other]' })

  on('command.run', { command: 'timeline' }, async ($, e) => {
    const asked = String(e.args || '').trim().toLowerCase()
    tl.kind = KINDS.some((x) => x.kind === asked) ? asked : 'all'
    const r = currentRoom()
    const opened = await $.ui.open({ id: PANE, title: r && r.displayId ? 'Timeline · ' + r.displayId : 'Timeline', focus: true, closeOnEscape: true })
    if (opened && opened.isPlaced) markOpen(lp)
    await loadTimeline(paneIo($), false)
    if (!opened || !opened.isPlaced) return { text: timelineText() }
    return {}
  })

  // The person's close (esc, the close mark) reaches this hook; the mod's own
  // closes go through closePane, since a plugin's own $.ui.close is not
  // raised to its own hooks.
  on('ui.close', { id: PANE }, async ($, e, next) => {
    closed(lp)
    return next(e)
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const k = kit($.ui.resolve(e), e)
    const { Text } = k.els
    drawn(lp)
    let nowMs = lp.goodAt
    try {
      nowMs = Number(await $.clock.now())
    } catch {
      // No clock: the age reads as of the last read.
    }
    const r = currentRoom()
    const rows = [k.header({ key: 'tl-h', title: 'Timeline' + (r && r.displayId ? ' · ' + r.displayId : ''), dim: [r && r.title ? r.title : '', tl.loading ? 'reading…' : ''].filter(Boolean).join(' · ') })]
    rows.push(
      k.row(
        KINDS.map((c) =>
          k.button({ key: 'kind-' + c.kind, label: (c.kind === tl.kind ? '● ' : '') + c.label, hotkey: c.hotkey, primary: c.kind === tl.kind, dim: c.kind !== tl.kind, onPress: () => setKind($, c.kind) }),
        ),
        'tl-kinds',
        2,
      ),
    )
    if (tl.error) rows.push(k.text(clipText(tl.error, k.width), { key: 'tl-err' }))
    else if (!tl.loaded) rows.push(k.text(tl.loading ? 'Reading the timeline…' : 'Press a to read the timeline.', { key: 'tl-empty', dimColor: true }))
    else if (tl.events.length === 0) rows.push(k.text(tl.kind === 'all' ? 'Nothing on the timeline yet.' : 'Nothing of this kind yet.', { key: 'tl-none', dimColor: true }))
    if (tl.hasMore && !tl.error) rows.push(k.button({ key: 'tl-more', label: tl.loading ? 'reading…' : 'earlier events', hotkey: 'm', dim: true, onPress: () => loadTimeline(paneIo($), true) }))
    const space = Math.max(20, k.width - 10)
    for (const ev of tl.events) {
      const glyph = Text({ key: 'g', color: toneColor(ev.tone || 'neutral'), children: [ev.glyph || '·'] })
      const label = clipText(eventWords(ev), space)
      const press = k.els.Button({ key: 'ev-' + ev.seq, label, plain: true, onPress: () => quote($, ev) })
      const line = k.row([Text({ key: 't', dimColor: true, children: [hhmm(ev.at)] }), glyph, press], 'row-' + ev.seq, 1)
      if (k.rich && ev.detail) rows.push(k.col([line, k.text(clipText(ev.detail, space), { key: 'd', dimColor: true })], 'evc-' + ev.seq))
      else rows.push(line)
    }
    if (tl.loaded || notLive(r)) {
      const foot = liveFooter(k, lp, nowMs, r, 'tl-live')
      if (foot) rows.push(foot)
    }
    rows.push(
      k.row(
        [
          k.text('press a row to quote it into your prompt', { key: 'tl-hint', dimColor: true }),
          k.button({ key: 'tl-close', label: 'close (esc)', dim: true, onPress: () => closePane($) }),
        ],
        'tl-keys',
      ),
    )
    return k.col(rows, 'tl')
  })
}

export async function band(io, e, k) {
  return null
}

// onSnapshot: a new room event reads the newest page into an open timeline.
export function onSnapshot(io, snap, prev) {
  if (!lp.open || !tl.loaded) return
  const r = (snap.rooms || [])[0]
  if (r && typeof r.maxSeq === 'number' && r.maxSeq > tl.seenSeq) void loadTimeline(io, false, true)
}

export function start(io) {}

// tick moves the age under an open timeline, and reads an event the
// snapshot could not (a read was running).
export async function tick(io, nowMs) {
  if (!lp.open) return
  io.invalidate()
  const r = currentRoom()
  if (tl.loaded && !tl.loading && r && typeof r.maxSeq === 'number' && r.maxSeq > tl.seenSeq) void loadTimeline(io, false, true)
}

async function setKind($, kind) {
  if (tl.kind === kind && tl.loaded) return
  tl.kind = kind
  await loadTimeline(paneIo($), false)
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
  const seenSeq = r && typeof r.maxSeq === 'number' ? r.maxSeq : -1
  const before = more ? (tl.oldestSeq ?? (tl.events.length > 0 ? tl.events[0].seq : undefined)) : undefined
  const startedAt = await ioNow(io)
  if (!more) lp.triedAt = startedAt
  const answer = await io.run(timelineArgs(r, kind, before), { timeoutMs: 30000 })
  tl.loading = false
  if (kind !== tl.kind) {
    io.invalidate()
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
    }
    if (!(live && tl.loaded)) {
      tl.hasMore = !!answer.hasMore
      if (typeof answer.oldestSeq === 'number' && (!more || tl.oldestSeq == null || answer.oldestSeq < tl.oldestSeq)) tl.oldestSeq = answer.oldestSeq
      else if (!more) tl.oldestSeq = page.length > 0 ? page[0].seq : null
    }
    tl.loaded = true
  }
  io.invalidate()
  if (tl.again) await loadTimeline(io, false, true)
}

// mergeNewest lays the newest page over the rows shown: the rows the page
// covers (its oldest seq and after) are replaced by it, the older ones stay.
export function mergeNewest(rows, page) {
  const from = page[0].seq
  return [...rows.filter((x) => x.seq < from), ...page]
}

// closePane closes the pane and stops its reads.
async function closePane($) {
  closed(lp)
  await $.ui.close({ id: PANE })
}

// paneIo is what this file's reads take, from the hook's own `$`, shaped as
// register.js shapes `io`.
function paneIo($) {
  return {
    run: async (args, opts) => {
      try {
        return parseAnswer(await $.process.run([room.bin, ...args], { timeoutMs: 30000, ...(opts || {}) }))
      } catch (err) {
        return { ok: false, error: String(err).slice(0, 200) }
      }
    },
    invalidate: () => $.ui.invalidate('ui.render'),
    now: () => $.clock.now(),
  }
}

async function ioNow(io) {
  try {
    return Number(await io.now())
  } catch {
    return lp.triedAt
  }
}

// quote drafts the event into the prompt, at the cursor: "> 15:52 <text>".
async function quote($, ev) {
  await $.prompt.fill({ text: quoteText(ev), mode: 'insert' })
}

export function quoteText(ev) {
  return '> ' + hhmm(ev.at) + ' ' + String(ev.text || '') + '\n\n'
}

export function timelineArgs(r, kind, before) {
  const args = ['timeline', '--host', HOST]
  if (r && r.roomKey) args.push('--room', r.roomKey)
  args.push('--limit', String(LIMIT))
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
