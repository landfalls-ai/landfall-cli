// The vote card: when the room is waiting on the person's position on a
// staged finding, the band says so with keys to corroborate (5), contest (6),
// read the evidence (7) or set it aside (8); a toast tells each new request
// once; the 'landfall-vote' pane (and /vote) draws the whole card. On the
// mobile surface the band is a compact card with two buttons. Proposal items
// 01 and 16.
//
// A press records the vote AS THE PERSON: it runs `landfall vote`, which
// calls the claims route with the person's own token and no agent instance
// (review.md #1), never $.mcp.call. The person's own claims (`mine`) are never
// offered: the server already counts them, and a second vote is refused.

import { addCommand, clip, currentOf, currentRoom, parseAnswer, room, roomName, severityTone } from '../core.js'
import { TONE, kit } from '../kit.js'

export const PANE = 'landfall-vote'
const PANE_TITLE = 'Your vote'
// A pane opened unasked is placed only from this many columns (ui.open).
const DOCK_COLUMNS = 144

// Claims already told as a toast, "<roomKey>#<seq>": never told twice.
let told = new Set()
// Claims a vote was recorded on: hidden until the watch stream drops them.
let done = new Set()
// Claims with a vote in flight: a second press does nothing.
let busy = new Set()
// Per room, the claims the person set aside with "later" (a new one shows).
let aside = {}
// What the pane shows: { roomKey, claimSeq, contesting }.
let pane = null
// The band's last viewport, so a new vote can open the pane where it docks.
let lastView = null

export function install(on) {
  addCommand({ name: 'vote', description: 'Show the finding the war room is waiting on your vote for' })

  on('command.run', { command: 'vote' }, async ($) => {
    const r = currentRoom()
    if (!r) return { text: 'This folder is not in a war room. Open a share link from the room to join it.' }
    const v = waiting(r)[0]
    if (!v) return { text: 'No vote is waiting on you.' }
    pane = { roomKey: r.roomKey, claimSeq: v.claimSeq, contesting: false }
    const opened = await $.ui.open({ id: PANE, title: PANE_TITLE, focus: true, closeOnEscape: true })
    if (!opened.isPlaced) return { text: votesText(r) }
    return {}
  })

  // The whole card: statement, author, positions, evidence, expiry, buttons;
  // the reason field while contesting.
  on('ui.render', { component: 'Pane' }, async ($, e, next) => {
    if (e.requestId !== PANE) return next(e)
    const k = kit($.ui.resolve(e), e)
    const io = ioOf($, e.surface)
    return drawPane(io, k, e.surface)
  })
}

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
        canContest(k)
          ? k.button({ key: 'vote-contest', label: 'contest', hotkey: '6', onPress: () => openPane(io, r, v, true) })
          : null,
        k.button({ key: 'vote-evidence', label: 'read the evidence', hotkey: '7', onPress: () => openPane(io, r, v, false) }),
        k.button({ key: 'vote-later', label: 'later', hotkey: '8', dim: true, onPress: () => setAside(io, r) }),
      ],
      'vote-keys',
    ),
  ]
  return rows
}

// onSnapshot: one toast per new request, and the pane opened unasked only
// where the surface docks it beside the transcript and is wide enough
// (review.md #11).
export function onSnapshot(io, snap) {
  for (const r of snap.rooms) {
    for (const v of r.votes ?? []) {
      if (v.mine) continue
      const id = idOf(r.roomKey, v)
      if (told.has(id)) continue
      told.add(id)
      io.toast(author(v) + ' asked for your vote on #' + v.claimSeq, 8000)
      // Never over a reason the person is typing.
      if (r === currentOf(snap) && docks() && !(pane && pane.contesting)) void openPane(io, r, v, false, true)
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
// `io` is register.js's io, or the pane's own (ioOf).
export async function castVote(io, roomKey, v, position, reason) {
  const id = idOf(roomKey, v)
  if (busy.has(id)) return
  busy.add(id)
  try {
    const args = ['vote', '--room', roomKey, '--claim', String(v.claimSeq), '--position', position]
    if (reason) args.push('--reason', reason)
    const answer = (await io.run(args)) || {}
    if (answer.ok) {
      done.add(id)
      const word = position === 'contest' ? 'Contested' : 'Corroborated'
      let said = word + ' #' + v.claimSeq + (answer.admitted ? ' · admitted' : '')
      if (answer.note) said += '. ' + answer.note
      io.toast(said, 6000)
      if (pane && pane.roomKey === roomKey && pane.claimSeq === v.claimSeq) {
        pane = null
        await io.close(PANE)
      }
    } else {
      io.toast(String(answer.error || 'The vote was not recorded.'), 8000)
    }
  } catch (err) {
    io.toast('The vote was not recorded: ' + clip(String(err), 160), 8000)
  } finally {
    busy.delete(id)
    io.invalidate()
  }
}

// openPane shows one vote's card; `contesting` draws the reason field.
// Unasked (a new vote on a docking surface) it opens without the keyboard.
async function openPane(io, r, v, contesting, unasked) {
  pane = { roomKey: r.roomKey, claimSeq: v.claimSeq, contesting: !!contesting }
  try {
    const opened = await io.open(PANE, PANE_TITLE, unasked ? {} : { focus: true, closeOnEscape: true })
    if (opened && opened.isPlaced === false && !unasked) {
      io.toast('Widen the window to see the vote, or run /vote.', 6000)
    }
  } catch {
    // The band row stays; the pane is a convenience.
  }
  io.invalidate()
}

function setAside(io, r) {
  aside[r.roomKey] = new Set((r.votes ?? []).map((v) => v.claimSeq))
  if (pane && pane.roomKey === r.roomKey) {
    pane = null
    void io.close(PANE)
  }
  io.invalidate()
}

// drawPane is the full card in the 'landfall-vote' pane.
function drawPane(io, k, surface) {
  const { Text, Box } = k.els
  const r = pane ? room.snapshot.rooms.find((x) => x.roomKey === pane.roomKey) : currentRoom()
  const v = r && pane ? (r.votes ?? []).find((x) => x.claimSeq === pane.claimSeq && !x.mine) : r ? waiting(r)[0] : null
  const close = k.button({ key: 'pane-close', label: 'close (esc)', dim: true, onPress: () => closePane(io) })
  if (!r || !v) {
    const said = pane && r && done.has(idOf(r.roomKey, { claimSeq: pane.claimSeq })) ? 'Your vote on #' + pane.claimSeq + ' is recorded.' : 'No vote is waiting on you.'
    return k.col([k.header({ key: 'vp-h', title: PANE_TITLE }), Text({ key: 'vp-none', dimColor: true, children: [said] }), close], 'vp')
  }
  if (!pane) pane = { roomKey: r.roomKey, claimSeq: v.claimSeq, contesting: false }
  const left = timeLeft(v)
  const rows = []
  rows.push(k.header({ key: 'vp-h', title: 'Your vote is waiting', pills: left ? [{ text: left, tone: 'warning' }] : [] }))
  rows.push(Text({ key: 'vp-room', dimColor: true, children: [clip(roomName(r), k.width)] }))
  rows.push(Text({ key: 'vp-who', bold: true, children: [clip(author(v) + ' · staged #' + v.claimSeq, k.width)] }))
  rows.push(k.row([positionsMark(k, v), Text({ key: 'vp-pos-t', children: [positionsText(v)] })], 'vp-pos', 1))
  rows.push(Text({ key: 'vp-quote', children: ['“' + v.statement + '”'] }))
  rows.push(Text({ key: 'vp-ev', dimColor: !v.evidence, children: [v.evidence ? 'Evidence: ' + v.evidence : 'No evidence is attached to this claim.'] }))
  rows.push(Text({ key: 'vp-exp', dimColor: true, children: [expiryText(v)] }))
  rows.push(
    Text({
      key: 'vp-note',
      dimColor: true,
      children: [
        v.shortfall === 1
          ? 'Your position admits it to the shared context. Every agent in the room reads it after that.'
          : 'Your position counts toward admitting it to the shared context.',
      ],
    }),
  )
  const contest = canContest(k)
  if (pane.contesting && contest) {
    rows.push(
      k.els.Input({
        key: 'pane-reason',
        label: 'Why do you contest it? ',
        placeholder: 'what the evidence does not show',
        submitLabel: 'contest',
        autoFocus: true,
        onSubmit: (value) => submitContest(io, r.roomKey, v, value),
      }),
    )
  }
  rows.push(
    k.row(
      [
        k.button({ key: 'pane-corroborate', label: 'corroborate', hotkey: '5', primary: true, onPress: () => castVote(io, r.roomKey, v, 'corroborate') }),
        contest && !pane.contesting
          ? k.button({
              key: 'pane-contest',
              label: 'contest…',
              hotkey: '6',
              onPress: () => {
                pane = { ...pane, contesting: true }
                io.invalidate()
              },
            })
          : null,
        k.button({ key: 'pane-later', label: 'later', hotkey: '8', dim: true, onPress: () => setAside(io, r) }),
        close,
      ],
      'vp-keys',
    ),
  )
  return Box({ flexDirection: 'column', rowGap: surface === 'terminal' ? 0 : 1, children: rows.filter(Boolean) })
}

async function submitContest(io, roomKey, v, value) {
  const reason = String(value || '').trim()
  if (!reason) {
    io.toast('Say why you contest it, then press Enter.', 6000)
    return
  }
  await castVote(io, roomKey, v, 'contest', reason)
}

function closePane(io) {
  pane = null
  void io.close(PANE)
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

// ioOf is the pane's own io, the same closures register.js builds, spelled
// here so `$` stays in this file.
function ioOf($, surface) {
  return {
    surface: surface || 'terminal',
    run: async (args) => {
      try {
        return parseAnswer(await $.process.run([room.bin, ...args], { timeoutMs: 20000 }))
      } catch (err) {
        return { ok: false, error: String(err).slice(0, 200) }
      }
    },
    toast: (text, ms) => $.ui.toast(text, ms ? { timeoutMs: ms } : undefined),
    open: (id, title, opts) => $.ui.open({ id, title, ...(opts || {}) }),
    close: (id) => $.ui.close({ id }),
    invalidate: () => $.ui.invalidate('ui.render'),
  }
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

// positionsMark: a ring on a surface that draws vectors, dots on the terminal.
function positionsMark(k, v) {
  const n = Math.max(0, v.positionsSoFar ?? 0)
  const total = Math.max(1, v.needed ?? n + (v.shortfall ?? 1))
  if (k.rich) return k.svg(ringSvg(n, total), { key: 'vp-ring', alt: n + ' of ' + total + ' positions', width: 36, height: 36 })
  const shown = Math.min(total, 8)
  const filled = Math.min(shown, n)
  return k.els.Text({ key: 'vp-dots', color: TONE.good, children: ['●'.repeat(filled) + '○'.repeat(shown - filled)] })
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

// votesText is /vote's answer where no pane can be placed (`claude -p`).
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

// tick runs every TICK_MS while the session lives: a component with an open
// pane refreshes it here on its own cadence (nothing to do by default).
export function tick(io, nowMs) {}
