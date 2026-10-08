// Company second brain lookup (proposal item 15, FR-15): the Brain chip inside the console's More
// tab (spec 4.8, 4.11) shows what the brain holds that bears on the live room: each entry's title,
// its summary, how sure the brain is and how settled the entry is (two labels at most), the kind
// and the past incidents it came from on one dim line. A press quotes a match into the prompt as a
// draft. Reads run `landfall brain`; with no text the CLI asks for entries like the room's own
// incident, with text it searches the organization's memory. The CLI maps only fields an entry
// really carries (review #10): there is no "fix" or "why" on an entry, so none is drawn.
//
// It reads on open, on `r` and on a search (spec 2.5), never on a clock. More draws the chip row
// and calls `tab` for the body.

import { HOST, clip, currentRoom, quoteDraft } from '../core.js'
import { livePane, readLive } from '../live.js'
import { centerRow, footerOf, nowOf, resetLive, roomKeyOf } from './tabparts.js'

// The last `landfall brain` answer (live.js keeps it and its age) and the text it searched for.
const lp = livePane()
const view = { q: '', roomKey: '' }

// forRoom drops what was asked of another room.
function forRoom(r) {
  const key = roomKeyOf(r)
  if (key === view.roomKey) return
  view.roomKey = key
  view.q = ''
  resetLive(lp)
}

// No hooks of its own: the console owns the pane and the `/landfall` command.
export function install(on) {}

export async function band(io, e, k) {
  return null
}

export function onSnapshot(io, snap, prev) {}

export function start(io) {}

// tick runs every TICK_MS while the session lives; the brain reads only on open, `r` and a search.
export function tick(io, nowMs) {}

// warm reads the brain when the Brain chip is shown (and again when asked): the room's own
// incident, or the text last searched for.
export async function warm(io) {
  forRoom(currentRoom())
  await read(io, view.q)
}

export async function refresh(io) {
  forRoom(currentRoom())
  await read(io, view.q)
}

// search reads the brain for `q` (`/landfall brain <text>`, or the field's Enter).
export async function search(io, q) {
  forRoom(currentRoom())
  await read(io, String(q || '').trim())
}

// Brain has no badge.
export function badge() {
  return null
}

// The live read's state (live.js).
export function readState() {
  return lp
}

// Brain has no letters: Enter in the field searches, Enter on a match quotes it.
export function keys(k, io, nowMs, args) {
  return []
}

// tab is the Brain body (spec 4.8), under More's chip row.
export function tab(k, io, nowMs, args) {
  const { Box, Text, Button, Input } = k.els
  const r = currentRoom()
  forRoom(r)
  const answer = lp.answer
  const matches = answer && answer.ok ? answer.matches || [] : []
  const asked = view.q ? 'for "' + clip(view.q, 40) + '"' : r ? 'like ' + (r.displayId || 'this incident') : ''
  const rows = [k.row([Text({ key: 'br-h', bold: true, children: ['Company second brain'] }), asked ? Text({ key: 'br-d', dimColor: true, children: [asked] }) : null], 'br-hdr', 1)]
  if (!answer) rows.push(Text({ key: 'br-wait', dimColor: true, children: ['Asking the company second brain…'] }))
  else if (!answer.ok) rows.push(Text({ key: 'br-err', children: [String(answer.error || 'The company second brain did not answer.')] }))
  else if (matches.length === 0) rows.push(Text({ key: 'br-none', dimColor: true, children: [view.q ? 'The company second brain has nothing on "' + clip(view.q, 40) + '" yet.' : 'The company second brain holds nothing on this incident yet.'] }))

  matches.forEach((m, i) => {
    const id = 'm' + i
    const labels = centerRow(
      k,
      [m.confidence ? k.pill(confidenceWords(m.confidence), confidenceTone(m.confidence), 'bc-' + id) : null, m.status ? k.pill(String(m.status), statusTone(m.status), 'bs-' + id) : null],
      'bl-' + id,
      1,
    )
    const facts = []
    if (m.confidence || m.status) facts.push(labels)
    if (m.summary) facts.push(Text({ key: 'bf-' + id, children: [String(m.summary)] }))
    const from = sourceLine(m)
    if (from) facts.push(Text({ key: 'bi-' + id, dimColor: true, children: [from] }))
    const title = Text({ key: 'bt-' + id, bold: true, children: [String(m.title || 'past incident')] })
    const press = Button({ key: 'quote-' + i, label: k.terminal ? 'Enter: quote it' : 'Quote into my prompt', ...(k.terminal ? { plain: true, dimColor: true } : {}), onPress: () => quote(io, m) })
    if (k.terminal) {
      rows.push(Box({ key: 'bb-' + id, flexDirection: 'column', children: [title, ...facts, press] }))
      return
    }
    rows.push(Box({ key: 'card-' + id, flexDirection: 'column', rowGap: 1, borderStyle: 'round', borderColor: '#d9d9d6', paddingX: 1, children: [title, ...facts, press] }))
  })
  if (matches.length > 0) rows.push(Text({ key: 'br-hint', dimColor: true, children: ['Enter on a match quotes it into your prompt.'] }))

  if (hasFields(k)) {
    rows.push(
      Input({ key: 'brain-q', label: 'Search', placeholder: 'origin pool exhaustion', submitLabel: k.terminal ? 'search' : 'Search', value: view.q, onSubmit: (value) => search(io, value) }),
    )
  }
  return rows
}

// footer is the console's footer for this tab: `live · updated 4s ago`, or null before the first read.
export function footer(k, nowMs) {
  return footerOf(k, lp, nowMs, currentRoom(), 'br-live')
}

// quote drafts a match into the prompt (the console stays where it is).
async function quote(io, m) {
  await io.fill(quoteDraft(matchQuote(m)))
}

// read runs `landfall brain` for the room, with the text when there is some, one read at a time.
// A new text clears the old answer, so the tab says it is asking.
async function read(io, q) {
  if (q !== view.q) {
    view.q = q
    lp.answer = null
    lp.last = null
  }
  const fetch = () => {
    io.invalidate()
    const r = currentRoom()
    const args = ['brain']
    if (r) args.push('--room', r.roomKey)
    if (view.q) args.push('--q', view.q)
    args.push('--host', HOST)
    return io.run(args, { timeoutMs: 20000 })
  }
  await readLive(lp, fetch, () => nowOf(io, lp.triedAt))
  io.invalidate()
}

// text is the Brain answer where no pane can be placed (`claude -p`): it reads (searching for
// `args` when there is text) and says it.
export async function text(io, args) {
  forRoom(currentRoom())
  await read(io, String(args || '').trim())
  return brainText(lp.last, view.q)
}

// statusTone: how settled the brain holds an entry: `confirmed` is info, a contested one critical,
// anything else (`proposed`) neutral.
export function statusTone(status) {
  const s = String(status || '').toLowerCase()
  if (/conflict|contest/.test(s)) return 'critical'
  if (/confirm|accept|admit|establish/.test(s)) return 'info'
  return 'neutral'
}

// kindWords is the kind folded into the source line: "lesson · settled", "fact", or "".
export function kindWords(m) {
  return [m.kind, m.maturity].filter(Boolean).join(' · ')
}

// UUID-like ids are not names a person reads; display ids ("Acme 91") are.
const OPAQUE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

// fromWords names the incidents an entry came from: "From Acme 91, Acme 95",
// or "From 2 past incidents" when the CLI has only their ids.
export function fromWords(m) {
  const list = Array.isArray(m.incidents) && m.incidents.length > 0 ? m.incidents : m.incident ? [m.incident] : []
  if (list.length === 0) return ''
  const named = list.filter((x) => !OPAQUE.test(String(x)))
  if (named.length === list.length) return 'From ' + named.slice(0, 4).join(', ') + (named.length > 4 ? ' and ' + (named.length - 4) + ' more' : '')
  return 'From ' + list.length + (list.length === 1 ? ' past incident' : ' past incidents')
}

export function confidenceTone(confidence) {
  const c = String(confidence || '').toLowerCase()
  if (/establish|confirm|high/.test(c)) return 'good'
  if (/likely|probable|medium|moderate/.test(c)) return 'warning'
  if (/low|contest|disput|weak/.test(c)) return 'critical'
  return 'neutral'
}

// confidenceWords is the label's words: "high confidence".
export function confidenceWords(confidence) {
  const c = String(confidence || '').trim()
  return /confidence/i.test(c) ? c : c + ' confidence'
}

// sourceLine is the dim line under a match: the kind and where it came from, "pattern · from Acme
// 91, Acme 95".
export function sourceLine(m) {
  const from = fromWords(m)
  return [kindWords(m), from ? from.charAt(0).toLowerCase() + from.slice(1) : ''].filter(Boolean).join(' · ')
}

// matchQuote is a match as the person's draft quotes it.
export function matchQuote(m) {
  const out = ['From the company second brain: ' + (m.title || 'untitled')]
  if (m.summary) out.push(m.summary)
  const from = fromWords(m)
  if (from) out.push(from)
  const sure = [kindWords(m), m.confidence ? 'confidence ' + m.confidence : '', m.status || ''].filter(Boolean).join(' · ')
  if (sure) out.push(sure)
  return out.join('\n')
}

// brainText is the Brain answer where no pane can be drawn.
export function brainText(answer, q) {
  if (!answer) return 'The company second brain did not answer.'
  if (!answer.ok) return String(answer.error || 'The company second brain did not answer.')
  const matches = answer.matches || []
  if (matches.length === 0) return q ? 'The company second brain has nothing on "' + q + '" yet.' : 'The company second brain holds nothing on this incident yet.'
  const out = []
  for (const m of matches) {
    out.push([m.title || 'past incident', kindWords(m), m.confidence, m.status].filter(Boolean).join(' · '))
    if (m.summary) out.push('  ' + m.summary)
    const from = fromWords(m)
    if (from) out.push('  ' + from)
  }
  return out.join('\n')
}

// hasFields: the surface draws an Input (mobile draws no field yet).
function hasFields(k) {
  return k.surface !== 'mobile' && typeof k.els.Input === 'function'
}

