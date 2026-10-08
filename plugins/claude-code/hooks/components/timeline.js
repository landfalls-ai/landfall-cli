// The incident timeline and shared context ledger: /timeline (proposal item 08, FR-08).
//
// `landfall timeline` answers the room's events oldest first, filtered by
// kind on the CLI's side. The pane draws one row per event (time, a glyph in
// its tone, what happened, who), chips filter by kind, `m` reads further back,
// and a press on a row quotes it into the prompt as a draft. Nothing sends.

import { HOST, addCommand, currentRoom, parseAnswer, room, roomName } from '../core.js'
import { kit } from '../kit.js'
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
}

export function install(on) {
  addCommand({ name: 'timeline', description: "Show the war room's timeline", argumentHint: '[findings|status|beacon|people|other]' })

  on('command.run', { command: 'timeline' }, async ($, e) => {
    const asked = String(e.args || '').trim().toLowerCase()
    tl.kind = KINDS.some((x) => x.kind === asked) ? asked : 'all'
    const r = currentRoom()
    const opened = await $.ui.open({ id: PANE, title: r && r.displayId ? 'Timeline · ' + r.displayId : 'Timeline', focus: true, closeOnEscape: true })
    await loadTimeline($, false)
    if (!opened || !opened.isPlaced) return { text: timelineText() }
    return {}
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const k = kit($.ui.resolve(e), e)
    const { Text } = k.els
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
    if (tl.hasMore && !tl.error) rows.push(k.button({ key: 'tl-more', label: tl.loading ? 'reading…' : 'earlier events', hotkey: 'm', dim: true, onPress: () => loadTimeline($, true) }))
    const space = Math.max(20, k.width - 10)
    for (const ev of tl.events) {
      const glyph = Text({ key: 'g', color: toneColor(ev.tone || 'neutral'), children: [ev.glyph || '·'] })
      const label = clipText(eventWords(ev), space)
      const press = k.els.Button({ key: 'ev-' + ev.seq, label, plain: true, onPress: () => quote($, ev) })
      const line = k.row([Text({ key: 't', dimColor: true, children: [hhmm(ev.at)] }), glyph, press], 'row-' + ev.seq, 1)
      if (k.rich && ev.detail) rows.push(k.col([line, k.text(clipText(ev.detail, space), { key: 'd', dimColor: true })], 'evc-' + ev.seq))
      else rows.push(line)
    }
    rows.push(
      k.row(
        [
          k.text('press a row to quote it into your prompt', { key: 'tl-hint', dimColor: true }),
          k.button({ key: 'tl-close', label: 'close (esc)', dim: true, onPress: () => $.ui.close({ id: PANE }) }),
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

export function onSnapshot(io, snap, prev) {}

export function start(io) {}

async function setKind($, kind) {
  if (tl.kind === kind && tl.loaded) return
  tl.kind = kind
  await loadTimeline($, false)
}

// loadTimeline reads the newest page for the chosen kind, or with `more` the
// page before the oldest row shown, and keeps the rows oldest first.
async function loadTimeline($, more) {
  if (tl.loading) return
  tl.loading = true
  $.ui.invalidate('ui.render')
  const kind = tl.kind
  const before = more ? (tl.oldestSeq ?? (tl.events.length > 0 ? tl.events[0].seq : undefined)) : undefined
  let answer
  try {
    answer = parseAnswer(await $.process.run([room.bin, ...timelineArgs(currentRoom(), kind, before)], { timeoutMs: 30000 }))
  } catch (err) {
    answer = { ok: false, error: String(err).slice(0, 200) }
  }
  tl.loading = false
  if (kind !== tl.kind) {
    $.ui.invalidate('ui.render')
    return
  }
  if (!answer.ok) {
    tl.error = answer.error || 'The timeline could not be read.'
  } else {
    tl.error = ''
    tl.loaded = true
    const page = (answer.events || []).slice().sort((x, y) => x.seq - y.seq)
    if (more) {
      const have = new Set(tl.events.map((x) => x.seq))
      tl.events = [...page.filter((x) => !have.has(x.seq)), ...tl.events]
    } else tl.events = page
    tl.hasMore = !!answer.hasMore
    if (typeof answer.oldestSeq === 'number' && (!more || tl.oldestSeq == null || answer.oldestSeq < tl.oldestSeq)) tl.oldestSeq = answer.oldestSeq
    else if (!more) tl.oldestSeq = page.length > 0 ? page[0].seq : null
  }
  $.ui.invalidate('ui.render')
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

// tick runs every TICK_MS while the session lives: a component with an open
// pane refreshes it here on its own cadence (nothing to do by default).
export function tick(io, nowMs) {}
