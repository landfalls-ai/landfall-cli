// The vote: when the room is waiting on the person's position on a staged finding, the band says
// so with keys to corroborate (5), contest (6), read the evidence (7) or set it aside (8); a toast
// tells each new request once; the console's Vote tab (spec 4.1) draws the whole card. On the
// mobile surface the band is a compact card with two buttons. Proposal items 01 and 16.
//
// A press records the vote AS THE PERSON: it runs `landfall vote`, which calls the claims route
// with the person's own token and no agent instance (review.md #1), never $.mcp.call. The person's
// own claims (`mine`) are never offered: the server already counts them, and a second vote is
// refused.
//
// There is no pane of its own any more: the band keys and a new vote open the console on Vote
// (core.openConsole), and `tab`, `badge`, `warm`, `keys`, `refresh` and `text` are what console.js
// calls. The watch stream feeds this tab, so it reads nothing of its own.

import { clip, consoleState, currentOf, currentRoom, openConsole, roomName, severityTone } from '../core.js'
import { TONE } from '../kit.js'
import { centerRow, keyButton, nowOf, say } from './tabparts.js'

// A console opened unasked is placed only from this many columns (ui.open).
const DOCK_COLUMNS = 144
// How long the Vote tab keeps saying a vote was recorded before it shows the next one.
const RECORDED_MS = 10000

// Claims already told as a toast, "<roomKey>#<seq>": never told twice.
let told = new Set()
// Claims a vote was recorded on: hidden until the watch stream drops them.
let done = new Set()
// Claims with a vote in flight, "<roomKey>#<seq>" to the position being recorded: a second press
// does nothing.
let busy = new Map()
// Per room, the claims the person set aside with "later" (a new one shows).
let aside = {}
// What the tab shows: { roomKey, claimSeq, contesting }.
let view = null
// The vote just recorded: { roomKey, claimSeq, at }, said for RECORDED_MS.
let justVoted = null
// The band's last viewport, so a new vote can open the console where it docks.
let lastView = null

// No hooks of its own: the console owns the pane and the `/landfall` command.
export function install(on) {}

// band: the oldest or most urgent vote waiting on the person, with its keys;
// on mobile a compact card with two buttons.
export async function band(io, e, k) {
  if (e.viewport) lastView = { isFullscreen: e.viewport.isFullscreen, columns: e.viewport.columns, surface: e.surface }
  const r = currentRoom()
  if (!r) return null
  const list = waiting(r)
  const v = list[0]
  if (!v) return null
  if (io.surface === 'mobile' || e.surface === 'mobile') return [mobileCard(io, k, r, v)]
  const { Text } = k.els
  const left = timeLeft(v)
  const more = list.length > 1 ? ' · ' + (list.length - 1) + ' more waiting' : ''
  const head = k.rich
    ? k.header({ key: 'vote-h', title: roomName(r), pills: [{ text: 'vote waiting' + (left ? ' · ' + left : ''), tone: 'warning' }], dim: more ? more.slice(3) : undefined })
    : k.row(
        [
          Text({ key: 'vote-h-t', bold: true, children: ['Landfall · ' + clip(roomName(r), Math.max(16, k.width - 40))] }),
          Text({ key: 'vote-h-w', color: TONE.warning, children: ['vote waiting' + (left ? ' · ' + left : '')] }),
          more ? Text({ key: 'vote-h-m', dimColor: true, children: [more.slice(3)] }) : null,
        ],
        'vote-h',
        1,
      )
  const rows = [
    head,
    Text({ key: 'vote-s', children: [clip(author(v) + ': “' + v.statement + '”', k.width)] }),
    Text({ key: 'vote-p', dimColor: true, children: [positionsText(v)] }),
    k.row(
      [
        k.button({ key: 'vote-corroborate', label: 'corroborate', hotkey: '5', primary: true, onPress: () => castVote(io, r.roomKey, v, 'corroborate') }),
        canContest(k) ? k.button({ key: 'vote-contest', label: 'contest', hotkey: '6', onPress: () => showVote(io, r, v, true) }) : null,
        k.button({ key: 'vote-evidence', label: 'read the evidence', hotkey: '7', onPress: () => showVote(io, r, v, false) }),
        k.button({ key: 'vote-later', label: 'later', hotkey: '8', dim: true, onPress: () => setAside(io, r) }),
      ],
      'vote-keys',
    ),
  ]
  return rows
}

// onSnapshot: one toast per new request, and the console opened on Vote, unasked, only where the
// surface docks it beside the transcript and is wide enough, only while it is closed (an open
// console keeps its tab; its Vote badge and the toast tell) and never over a reason the person
// is typing (review.md #11, spec 2.4).
export function onSnapshot(io, snap) {
  for (const r of snap.rooms) {
    for (const v of r.votes ?? []) {
      if (v.mine) continue
      const id = idOf(r.roomKey, v)
      if (told.has(id)) continue
      told.add(id)
      say(io, author(v) + ' asked for your vote on #' + v.claimSeq, 8000)
      if (r === currentOf(snap) && docks() && !consoleState.open && !(view && view.contesting)) {
        view = { roomKey: r.roomKey, claimSeq: v.claimSeq, contesting: false }
        openConsole(io, 'vote')
      }
    }
  }
}

export function start(io) {}

// waiting is the votes the band may offer, most urgent first: the soonest to
// expire, then the oldest claim. Never the person's own; never one already
// voted on or set aside.
export function waiting(r) {
  const set = aside[r.roomKey]
  const list = (r.votes ?? []).filter((v) => v && !v.mine && !done.has(idOf(r.roomKey, v)) && !(set && set.has(v.claimSeq)))
  return list.sort((a, b) => {
    const ea = a.expiresInMs ?? Infinity
    const eb = b.expiresInMs ?? Infinity
    if (ea !== eb) return ea - eb
    return a.claimSeq - b.claimSeq
  })
}

// castVote runs `landfall vote` as the person and says what came of it.
// `io` is register.js's io.
export async function castVote(io, roomKey, v, position, reason) {
  const id = idOf(roomKey, v)
  if (busy.has(id)) return
  busy.set(id, position)
  io.invalidate()
  try {
    const args = ['vote', '--room', roomKey, '--claim', String(v.claimSeq), '--position', position]
    if (reason) args.push('--reason', reason)
    const answer = (await io.run(args)) || {}
    if (answer.ok) {
      done.add(id)
      const word = position === 'contest' ? 'Contested' : 'Corroborated'
      let said = word + ' #' + v.claimSeq + (answer.admitted ? ' · admitted' : '')
      if (answer.note) said += '. ' + answer.note
      say(io, said, 6000)
      if (view && view.roomKey === roomKey && view.claimSeq === v.claimSeq) view = null
      justVoted = { roomKey, claimSeq: v.claimSeq, at: await nowOf(io, 0) }
    } else {
      say(io, String(answer.error || 'The vote was not recorded.'), 8000)
    }
  } catch (err) {
    say(io, 'The vote was not recorded: ' + String(err), 8000)
  } finally {
    busy.delete(id)
    io.invalidate()
  }
}

// showVote brings one vote up in the console on Vote; `contesting` draws the reason field. The
// person asked for it (a band key), so the console takes the keyboard (`asked`).
function showVote(io, r, v, contesting) {
  view = { roomKey: r.roomKey, claimSeq: v.claimSeq, contesting: !!contesting }
  justVoted = null
  openConsole(io, 'vote', { asked: true })
}

function setAside(io, r) {
  aside[r.roomKey] = new Set((r.votes ?? []).map((v) => v.claimSeq))
  view = null
  io.invalidate()
}

// current is what the tab shows: the vote on view (the one brought up, else the most urgent) and
// whether a recorded vote is still being said.
function current(r, nowMs) {
  const list = waiting(r)
  const recorded = justVoted && justVoted.roomKey === r.roomKey && nowMs - justVoted.at < RECORDED_MS ? justVoted : null
  let v = null
  if (view && view.roomKey === r.roomKey) v = list.find((x) => x.claimSeq === view.claimSeq) || null
  if (!v && !recorded) v = list[0] || null
  return { list, v, recorded }
}

// tab is the Vote tab's body (spec 4.1): the whole card, then `Also waiting`, then the footer-less
// keys row. Console.js draws the header, the switcher and `r: refresh`, `close (esc)`.
export function tab(k, io, nowMs, args) {
  const { Text } = k.els
  const r = currentRoom()
  if (!r) return [k.dim('This folder is not in a war room.', 'vt-none')]
  const { list, v, recorded } = current(r, nowMs)
  const rows = []
  if (!v) {
    rows.push(k.dim(recorded ? 'Your vote on #' + recorded.claimSeq + ' is recorded.' : 'No vote is waiting on you.', 'vt-none'))
  } else {
    const contesting = !!(view && view.roomKey === r.roomKey && view.claimSeq === v.claimSeq && view.contesting) && canContest(k)
    const left = timeLeft(v)
    rows.push(centerRow(k, [Text({ key: 'vt-h', bold: true, children: ['Your vote is waiting'] }), left ? k.pill(left, 'warning', 'vt-left') : null], 'vt-head', 1))
    rows.push(Text({ key: 'vt-who', bold: true, children: [clip(author(v) + ' · staged #' + v.claimSeq, k.width)] }))
    rows.push(centerRow(k, [positionsMark(k, v), Text({ key: 'vt-pos-t', dimColor: k.terminal, children: [positionsText(v)] })], 'vt-pos', 1))
    rows.push(k.terminal ? Text({ key: 'vt-quote', children: ['“' + v.statement + '”'] }) : k.quote('“' + v.statement + '”', 'vt-quote'))
    rows.push(Text({ key: 'vt-ev', dimColor: !v.evidence, children: [v.evidence ? 'Evidence: ' + v.evidence : 'No evidence is attached to this claim.'] }))
    rows.push(Text({ key: 'vt-exp', dimColor: true, children: [expiryText(v)] }))
    rows.push(
      Text({
        key: 'vt-note',
        dimColor: true,
        children: [
          v.shortfall === 1
            ? 'Your position admits it to the shared context. Every agent in the room reads it after that.'
            : 'Your position counts toward admitting it to the shared context.',
        ],
      }),
    )
    if (contesting) {
      rows.push(
        k.els.Input({
          key: 'vt-reason',
          label: 'Why do you contest it? ',
          placeholder: 'what the evidence does not show',
          submitLabel: k.terminal ? 'contest' : 'Contest',
          autoFocus: true,
          onSubmit: (value) => submitContest(io, r.roomKey, v, value),
        }),
      )
    }
  }
  const others = list.filter((x) => !v || x.claimSeq !== v.claimSeq)
  if (others.length > 0) {
    rows.push(Text({ key: 'vt-also', bold: true, children: ['Also waiting'] }))
    for (const o of others) {
      const left = timeLeft(o)
      const words = '#' + o.claimSeq + ' ' + author(o) + (left ? ' · ' + left : '') + ' · “' + o.statement + '”'
      rows.push(
        k.els.Button({
          key: 'vt-also-' + o.claimSeq,
          label: clip(words, k.width),
          plain: true,
          onPress: () => {
            view = { roomKey: r.roomKey, claimSeq: o.claimSeq, contesting: false }
            justVoted = null
            io.invalidate()
          },
        }),
      )
    }
  }
  const keyRow = keys(k, io, nowMs, args)
  if (keyRow.length > 0) rows.push(k.row(keyRow, 'vt-keys', 2))
  return rows
}

// keys are the Vote tab's letters, in the order of spec 2.3: `c: corroborate` (primary),
// `x: contest…` (`x: cancel` while the reason field is drawn), `l: later`. None while no vote is
// on view. Console.js adds `r: refresh` and `close (esc)`.
export function keys(k, io, nowMs, args) {
  const r = currentRoom()
  if (!r) return []
  const { v } = current(r, nowMs)
  if (!v) return []
  const id = idOf(r.roomKey, v)
  const inFlight = busy.get(id)
  const contesting = !!(view && view.roomKey === r.roomKey && view.claimSeq === v.claimSeq && view.contesting)
  const words = (terminal, desktop) => (k.terminal ? terminal : desktop)
  const out = [
    keyButton(k, {
      key: 'vt-corroborate',
      label: inFlight === 'corroborate' ? words('corroborating…', 'Corroborating…') : words('corroborate', 'Corroborate'),
      hotkey: 'c',
      primary: true,
      onPress: () => castVote(io, r.roomKey, v, 'corroborate'),
    }),
  ]
  if (canContest(k)) {
    out.push(
      keyButton(k, {
        key: 'vt-contest',
        label: inFlight === 'contest' ? words('contesting…', 'Contesting…') : contesting ? words('cancel', 'Cancel') : words('contest…', 'Contest…'),
        hotkey: 'x',
        onPress: () => {
          if (busy.has(id)) return
          view = { roomKey: r.roomKey, claimSeq: v.claimSeq, contesting: !contesting }
          io.invalidate()
        },
      }),
    )
  }
  out.push(keyButton(k, { key: 'vt-later', label: words('later', 'Later'), hotkey: 'l', dim: true, onPress: () => setAside(io, r) }))
  return out
}

// badge is the Vote segment's count: votes waiting on the person; none at zero.
export function badge() {
  const r = currentRoom()
  if (!r) return null
  const n = waiting(r).length
  return n > 0 ? n : null
}

// warm: the watch stream feeds this tab, so there is nothing to start.
export function warm(io) {}

// refresh: nothing to read again; the stream is the source.
export function refresh(io) {}

// text is the tab's answer where no pane can be placed (`claude -p`).
export async function text(io, args) {
  const r = currentRoom()
  if (!r) return 'This folder is not in a war room. Open a share link from the room to join it.'
  if (waiting(r).length === 0) return 'No vote is waiting on you.'
  return votesText(r)
}

// tick: the tab stops saying a vote was recorded after RECORDED_MS.
export function tick(io, nowMs) {
  if (justVoted && nowMs - justVoted.at >= RECORDED_MS) {
    justVoted = null
    io.invalidate()
  }
}

async function submitContest(io, roomKey, v, value) {
  const reason = String(value || '').trim()
  if (!reason) {
    say(io, 'Say why you contest it, then press Enter.', 6000)
    return
  }
  await castVote(io, roomKey, v, 'contest', reason)
}

// mobileCard is the band on the mobile surface: room, severity, the quote,
// "your vote · 1 of 2 · 4m 10s left", Corroborate and Later. Mobile draws no
// fields, so contesting with a reason waits for a larger screen.
function mobileCard(io, k, r, v) {
  const { Text } = k.els
  const st = r.status || {}
  const pills = st.severity ? [{ text: st.severity, tone: severityTone(st.severity) }] : []
  const parts = ['your vote', countText(v)]
  const left = timeLeft(v)
  if (left) parts.push(left)
  return k.col(
    [
      k.header({ key: 'vote-m-h', title: roomName(r), pills }),
      Text({ key: 'vote-m-q', children: [clip(author(v) + ': “' + v.statement + '”', 240)] }),
      Text({ key: 'vote-m-p', color: TONE.warning, children: [parts.join(' · ')] }),
      k.row(
        [
          k.button({ key: 'vote-corroborate', label: 'Corroborate', hotkey: '5', primary: true, onPress: () => castVote(io, r.roomKey, v, 'corroborate') }),
          k.button({ key: 'vote-later', label: 'Later', hotkey: '8', onPress: () => setAside(io, r) }),
        ],
        'vote-m-keys',
      ),
    ],
    'vote-m',
    1,
  )
}

function docks() {
  return !!lastView && lastView.isFullscreen === true && (lastView.columns ?? 0) >= DOCK_COLUMNS && lastView.surface !== 'mobile'
}

function canContest(k) {
  return typeof k.els.Input === 'function'
}

function idOf(roomKey, v) {
  return roomKey + '#' + v.claimSeq
}

// author names who staged it: a person, or a person's agent.
export function author(v) {
  if (v.authorIsAgent && v.authorHuman) return v.authorHuman + "'s agent"
  return v.authoredBy || v.authorHuman || 'someone'
}

// positionsText: "positions 1 of 2 · your vote would admit it".
export function positionsText(v) {
  const n = v.positionsSoFar ?? 0
  let s = v.needed != null ? 'positions ' + n + ' of ' + v.needed : 'positions ' + n
  if (v.shortfall === 1) s += ' · your vote would admit it'
  else if (v.shortfall > 1) s += ' · needs ' + v.shortfall + ' more'
  return s
}

// countText is the mobile card's short form: "1 of 2", or "needs 1 more".
function countText(v) {
  const n = v.positionsSoFar ?? 0
  if (v.needed != null) return n + ' of ' + v.needed
  if (v.shortfall > 0) return 'needs ' + v.shortfall + ' more'
  return n + (n === 1 ? ' position' : ' positions')
}

// timeLeft: "4m 10s left", "50s left", "time is up"; '' with no expiry.
export function timeLeft(v) {
  if (v.stale) return 'time is up'
  const ms = v.expiresInMs
  if (ms == null || !isFinite(ms)) return ''
  const s = Math.round(ms / 1000)
  if (s <= 0) return 'time is up'
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const r = s % 60
  if (h > 0) return h + 'h ' + m + 'm left'
  if (m === 0) return r + 's left'
  return r === 0 ? m + 'm left' : m + 'm ' + r + 's left'
}

function expiryText(v) {
  const left = timeLeft(v)
  if (!left) return 'This claim does not expire.'
  if (left === 'time is up') return 'Its time is up; the room may still take your position.'
  return 'Expires in ' + left.replace(/ left$/, '') + '.'
}

// positionsMark: a ring on a surface that draws vectors; on the terminal meter dots in ink, a
// `●` (bold) for each position held and a dim `○` for each still needed.
function positionsMark(k, v) {
  const n = Math.max(0, v.positionsSoFar ?? 0)
  const total = Math.max(1, v.needed ?? n + (v.shortfall ?? 1))
  if (k.rich) return k.svg(ringSvg(n, total), { key: 'vt-ring', alt: n + ' of ' + total + ' positions', width: 36, height: 36 })
  const shown = Math.min(total, 8)
  const filled = Math.min(shown, n)
  const { Text } = k.els
  return k.row(
    [filled > 0 ? Text({ key: 'vt-dots-on', bold: true, children: ['●'.repeat(filled)] }) : null, shown > filled ? Text({ key: 'vt-dots-off', dimColor: true, children: ['○'.repeat(shown - filled)] }) : null],
    'vt-dots',
    0,
  )
}

export function ringSvg(n, total) {
  const c = 2 * Math.PI * 15
  const on = Math.min(1, n / total) * c
  return (
    '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 36 36" width="36" height="36">' +
    '<circle cx="18" cy="18" r="15" fill="none" stroke="#8a8a8a" stroke-opacity=".3" stroke-width="4"/>' +
    '<circle cx="18" cy="18" r="15" fill="none" stroke="' + TONE.good + '" stroke-width="4" stroke-dasharray="' + on.toFixed(1) + ' ' + c.toFixed(1) + '" transform="rotate(-90 18 18)"/>' +
    '<text x="18" y="22" text-anchor="middle" font-size="11" font-weight="700" fill="#898781">' + n + '/' + total + '</text></svg>'
  )
}

// votesText is the Vote tab's answer where no pane can be placed (`claude -p`).
function votesText(r) {
  const out = [roomName(r) + ' · ' + (waiting(r).length === 1 ? '1 vote waiting on you' : waiting(r).length + ' votes waiting on you')]
  for (const v of waiting(r)) {
    const left = timeLeft(v)
    out.push('#' + v.claimSeq + ' ' + author(v) + ': “' + v.statement + '”')
    out.push('  ' + positionsText(v) + (left ? ' · ' + left : ''))
    if (v.evidence) out.push('  Evidence: ' + v.evidence)
  }
  out.push('Vote as yourself: landfall vote --claim <seq> --position corroborate|contest [--reason "<why>"]')
  return out.join('\n')
}
