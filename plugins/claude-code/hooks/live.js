// Live panes (live.md FR-L2, FR-L3): the bookkeeping every pane that reads
// the room again on its own shares. Pure: nothing here holds `$`. A component
// keeps one `livePane()` per pane, marks it open when the pane is placed or
// drawn and closed on `ui.close`, and refreshes it from `tick(io, nowMs)` (the
// session clock, every 5 s) or from `onSnapshot` when the room moved on.
//
//   const lp = livePane()
//   await refresh(lp, () => io.run(args), nowMs)   one read; only one in flight
//   due(lp, nowMs, everyMs)                         the pane's cadence has come round
//   liveFooter(k, lp, nowMs, r, key)                "live · updated 12s ago", or why not
//
// A failed refresh keeps the last good answer and marks the pane stale, as
// the web canvas does; the next good read clears it.

import { TONE } from './kit.js'

// The cadences, from the web app where it has one (useLiveWidgets intervalMs).
export const WALL_MS = 15000
export const LB_MS = 60000
export const LIST_MS = 30000

export const RECONNECTING = 'Reconnecting to the room…'

export function livePane() {
  return {
    open: false, // placed or drawn, and not closed since
    closed: false, // closed since the person last opened it: a late draw does not reopen it
    answer: null, // the last good answer, or the first failure while none was good
    last: null, // the latest read's own answer, good or not: what a text answer says
    goodAt: 0, // clock time of the last good read
    triedAt: 0, // clock time the last read started
    stale: '', // the failed read's sentence while the answer shown is older
    inFlight: false,
    again: false, // a read was asked for while one ran: run once more after it
    seq: -1, // the room's seq the last read followed (widgetSeq or maxSeq)
  }
}

// opened marks the pane open: its command or key placed it.
export function opened(lp) {
  lp.open = true
  lp.closed = false
}

// closed marks the pane closed (`ui.close`): it stops reading.
export function closed(lp) {
  lp.open = false
  lp.closed = true
}

// drawn marks the pane open when the engine draws it, so after a hot reload
// (module state starts over, the pane stays up) it goes on reading. A draw
// that comes after a close does not reopen it.
export function drawn(lp) {
  if (!lp.closed) lp.open = true
}

// SLACK_MS lets a cadence land on the nearest 5 s tick rather than the next
// one after it, so a 15 s pane reads every 15 s, not every 20.
const SLACK_MS = 2000

// due says the pane's cadence has come round since the last read began.
export function due(lp, nowMs, everyMs) {
  return lp.open && !lp.inFlight && nowMs - lp.triedAt >= everyMs - SLACK_MS
}

// readLive runs one read, then once more for each ask that came while it ran,
// so a change seen mid-read is read too. `now` answers the clock time.
export async function readLive(lp, fetch, now) {
  let started = await refresh(lp, fetch, await now())
  while (started && lp.again) started = await refresh(lp, fetch, await now())
  return started
}

// refresh runs one read and keeps what it answers. A read asked for while one
// runs is not started twice: it is remembered (`again`) and the caller runs
// it after. Answers false when it did not start a read.
export async function refresh(lp, fetch, nowMs) {
  if (lp.inFlight) {
    lp.again = true
    return false
  }
  lp.inFlight = true
  lp.again = false
  lp.triedAt = nowMs
  let answer
  try {
    answer = await fetch()
  } catch (err) {
    answer = { ok: false, error: String(err).slice(0, 200) }
  }
  lp.inFlight = false
  keep(lp, answer, nowMs)
  return true
}

// keep records one read's answer: a good one replaces what the pane shows; a
// failed one leaves the last good answer up and marks it stale.
export function keep(lp, answer, nowMs) {
  lp.last = answer || { ok: false, error: 'no answer' }
  if (answer && answer.ok) {
    lp.answer = answer
    lp.goodAt = nowMs
    lp.stale = ''
  } else if (lp.answer && lp.answer.ok) {
    lp.stale = (answer && answer.error) || 'The last read failed.'
  } else {
    lp.answer = answer || { ok: false, error: 'no answer' }
  }
}

// notLive says the room's connection is down or still coming up. Absent
// (an older CLI) reads as live.
export function notLive(r) {
  return !!(r && r.connection && r.connection !== 'live')
}

// since is an age as the footer says it: "4s", "3m", "2h".
export function since(ms) {
  const s = Math.max(0, Math.round((Number(ms) || 0) / 1000))
  if (s < 60) return s + 's'
  const m = Math.floor(s / 60)
  if (m < 60) return m + 'm'
  return Math.floor(m / 60) + 'h'
}

// liveWords is the pane's last line: whether it is live and how old what it
// shows is. Null before the first good read.
export function liveWords(lp, nowMs, r) {
  if (notLive(r)) return { text: RECONNECTING, tone: 'warning' }
  if (!lp.answer || !lp.answer.ok) return null
  const age = since(Math.max(0, nowMs - lp.goodAt))
  if (lp.stale) return { text: 'stale · updated ' + age + ' ago · ' + lp.stale, tone: 'warning' }
  return { text: 'live · updated ' + age + ' ago', tone: 'neutral' }
}

// liveFooter draws liveWords: dim while live, in the warning tone otherwise.
export function liveFooter(k, lp, nowMs, r, key = 'live') {
  const w = liveWords(lp, nowMs, r)
  if (!w) return null
  if (w.tone === 'neutral') return k.text(w.text, { key, dimColor: true })
  return k.toned(w.text, w.tone, { key })
}

// widgetSeqOf is the room's newest widget-shaping seq, or null from a CLI
// that does not send it.
export function widgetSeqOf(r) {
  return r && typeof r.widgetSeq === 'number' ? r.widgetSeq : null
}
