// The room at a glance: the band's head and news rows, the keys that catch up and open the
// console (spec §5.1), who is here under the prompt, the status line, and the toasts for an
// @-mention, a status change and Beacon ending a run. The room's full view is the console's
// Home tab (console.js); this file keeps its text answer (roomText) for where no pane draws.
//
// THE BAND (§5.1). register.js lays it out from these pieces, in this order: the head
// (bandHead: the room, `3 new`, `● vote waiting 4m 10s`, `● agent ✓`), the vote's statement and
// keys (vote.js), Beacon (beacon.js), the news or new-widget row (bandNews), the keys (bandKeys:
// `1: catch up  2: open the console  3: later  4: add chart <metric>`). Off the terminal the same
// rows sit in one card. The lines row left the band: lines live in People and on Home.
//
// LIVE (live.md). While the room's connection is not live the band says "Reconnecting to the
// room…" (a label off the terminal) and the status line's dot is hollow (FR-L3). For a minute
// after a new widget lands the band offers `w` to open the wall (FR-L4, wall.js). The head says
// whether the person's agent is in the room (FR-L5). Every field is optional: an older CLI that
// sends none draws as before.

import {
  ADDRESSED,
  CATCH_UP,
  clip,
  consoleState,
  currentRoom,
  newestLine,
  openConsole,
  orderRooms,
  pending,
  personLine,
  plainLine,
  room,
  roomIsOver,
  roomName,
  severityTone,
  statusWords,
  whoIsHere,
} from '../core.js'
import { TONE } from '../kit.js'
import { RECONNECTING, notLive } from '../live.js'
import { chart, pinChart } from './chart.js'
import { timeLeft, waiting } from './vote.js'
import { openWall, wallHint } from './wall.js'

export const AGENT_IN = 'agent ✓'
export const AGENT_OUT = 'agent not in the room'

// The seq of the newest addressed message per room already shown as a toast.
let toastedAt = {}
// The status and Beacon run each room had at the last snapshot, so a change
// is told once, and the first sight of a room is not told as a change.
let seenStatus = {}

export function install(on) {
  // Under the prompt: who else is in the room, while the person is not typing. Only the
  // terminal draws it; the desktop's empty prompt shows the catch-up suggestion instead and
  // the console's header carries the avatars (§5.3).
  on('ui.render', { component: 'PromptHint' }, async ($, e, next) => {
    if (e.props.isDraft || e.surface !== 'terminal') return next(e)
    const tail = whoIsHere()
    if (!tail) return next(e)
    return next({ ...e, props: { ...e.props, tail: (e.props.tail ? e.props.tail + '  ' : '') + tail } })
  })
}

// band: the band is laid out by register.js from bandHead, bandNews and bandKeys.
export async function band(io, e, k) {
  return null
}

// bandRoom is the room the band speaks for: the one with news first, else the current one.
export function bandRoom() {
  return pending()[0] || currentRoom()
}

// bandHead is row 1: `◆ Landfall 168 · cloudfront-5xx-high · 3 new  ● vote waiting 4m 10s
// ● agent ✓`, with `Reconnecting to the room…` above it on the terminal while the room's
// connection is down. Off the terminal: the mark, the name, labels, and who is here.
export function bandHead(io, e, k) {
  const r = bandRoom()
  if (!r) return []
  const { Box, Text } = k.els
  const total = pending().reduce((n, x) => n + x.count, 0)
  const votes = waiting(r)
  let voteLabel = null
  if (votes.length === 1) {
    const left = timeLeft(votes[0]).replace(/ left$/, '')
    voteLabel = k.pill('vote waiting' + (left ? ' ' + left : ''), 'warning', 'news-vote')
  } else if (votes.length > 1) voteLabel = k.pill(votes.length + ' votes waiting', 'warning', 'news-vote')
  if (k.terminal) {
    const rows = []
    if (notLive(r)) rows.push(k.toned(RECONNECTING, 'warning', { key: 'news-conn' }))
    const words = '◆ ' + roomName(r) + (total > 0 ? ' · ' + (total === 1 ? '1 new' : total + ' new') : '')
    rows.push(k.row([Text({ key: 'news-h', bold: true, children: [clip(words, Math.max(20, k.width - 40))] }), voteLabel, agentBadge(k, r)], 'news-hrow', 2))
    return rows
  }
  const st = r.status || {}
  const title = Box({
    key: 'news-title',
    flexDirection: 'row',
    columnGap: 1,
    flexWrap: 'wrap',
    alignItems: 'center',
    children: [
      k.mark('news-mark', 18),
      Text({ key: 'news-h', bold: true, children: [k.mobile ? clip(roomName(r), 28) : roomName(r)] }),
      st.severity ? k.pill(st.severity, severityTone(st.severity), 'news-sev') : null,
      voteLabel,
      total > 0 ? k.pill(total === 1 ? '1 new' : total + ' new', 'neutral', 'news-count') : null,
      notLive(r) ? k.pill('reconnecting', 'warning', 'news-agent') : agentBadge(k, r),
    ].filter(Boolean),
  })
  const here = (st.people || []).filter((p) => p.here)
  if (here.length === 0) return [title]
  if (k.mobile) return [k.col([title, k.dim(here.length + ' here', 'news-here')], 'news-head')]
  return [Box({ key: 'news-head', flexDirection: 'row', justifyContent: 'space-between', flexWrap: 'wrap', columnGap: 2, children: [title, k.avatars(here, { key: 'news-avs', max: 5 })] })]
}

// bandNews is row 5: for a minute after a new widget the wall's offer (`New on the wall: …`, or `New in bob's investigation: …`, and
// `w: open the wall`, the key whole and the text clipped), else the newest news line, dim. Only
// while there is news. Off the terminal the news line is left out while a vote waits (the vote
// is the news).
export async function bandNews(io, e, k) {
  const r = bandRoom()
  if (!r) return null
  const hint = wallHint(await nowOf(io))
  if (hint) {
    const key = k.button({ key: 'open-wall', label: k.terminal ? 'open the wall' : 'Open the wall', hotkey: 'w', onPress: async () => void (await openWall(io)) })
    const text = k.text(clip(hint.words || 'New on the wall: ' + hint.title, Math.max(16, k.width - 22)), { key: 'wall-new', dimColor: true })
    return [k.row([text, key], 'news-wall', 3)]
  }
  if (pending().length === 0) return null
  if (!k.terminal && waiting(r).length > 0) return null
  const newest = clip(plainLine(newestLine(r.digest ?? [])), k.terminal ? k.width : Math.max(40, k.width * 2))
  if (!newest) return null
  return [k.text(newest, { key: 'news-l', dimColor: true })]
}

// bandKeys is row 6: `1: catch up` (with news), `2: open the console` (`go to the console` while
// it is open: it gives the console the keys; the one key never dropped), `3: later` (with news),
// `4: add chart <metric>` while a chart is ready.
export function bandKeys(io, e, k) {
  const r = bandRoom()
  if (!r && !chart.ready) return null
  const rooms = pending()
  const news = rooms.length > 0
  const votes = !!r && waiting(r).length > 0
  const open = consoleState.open
  const keys = [
    news ? k.button({ key: 'catch-up', label: k.terminal ? 'catch up' : 'Catch up', hotkey: '1', primary: k.terminal || !votes, onPress: async () => void (await io.fill(CATCH_UP)) }) : null,
    k.button({
      key: 'console',
      label: k.terminal ? (open ? 'go to the console' : 'open the console') : open ? 'Go to the console' : 'Open the console',
      hotkey: '2',
      onPress: async () => void (await openConsole(io, open ? null : 'home', open ? consoleState.args : null, { focus: true })),
    }),
    news
      ? k.button({
          key: 'later',
          label: k.terminal ? 'later' : 'Later',
          hotkey: '3',
          dim: true,
          onPress: () => {
            for (const x of rooms) room.laterAt[x.roomKey] = x.maxSeq
            io.invalidate()
          },
        })
      : null,
    chart.ready ? k.button({ key: 'pin', label: clip((k.terminal ? 'add chart ' : 'Add chart ') + chart.ready.label, k.mobile ? 36 : 60), hotkey: '4', onPress: () => pinChart(io) }) : null,
  ]
  return [k.row(keys.filter(Boolean), 'news-keys', k.terminal ? 2 : 1)]
}

// bandHasNews: the room rows have something to say (news, the wall's offer, the reconnecting
// line). The band draws nothing when this, the vote, Beacon and the chart all have nothing.
export async function bandHasNews(io) {
  const r = bandRoom()
  if (!r) return false
  return pending().length > 0 || notLive(r) || !!wallHint(await nowOf(io))
}

// onSnapshot: a toast for a mention (once per message), a status change and
// Beacon ending a run; and the status line.
export function onSnapshot(io, snap) {
  // One toast per message: keyed by the message's own seq.
  for (const r of snap.rooms) {
    if (r.addressed === 0) continue
    let said = ''
    let saidSeq = -1
    for (const line of r.digest ?? []) {
      if (!line.endsWith(ADDRESSED)) continue
      const m = /^#(\d+) /.exec(line)
      const seq = m ? Number(m[1]) : r.maxSeq
      if (seq > saidSeq) {
        said = line
        saidSeq = seq
      }
    }
    if (said && saidSeq > (toastedAt[r.roomKey] ?? -1)) {
      toastedAt[r.roomKey] = saidSeq
      io.toast(roomName(r) + ': ' + plainLine(said), 8000)
    }
  }
  for (const r of snap.rooms) {
    const st = r.status
    if (!st) continue
    const was = seenStatus[r.roomKey]
    seenStatus[r.roomKey] = { status: st.status || '', beacon: st.beacon || '' }
    if (!was) continue
    if (st.status && was.status && st.status !== was.status) {
      io.toast(roomName(r) + ' is now ' + st.status, 8000)
    }
    if (st.beacon && st.beacon !== was.beacon && st.beacon !== 'investigating') {
      io.toast(roomName(r) + ': Beacon ' + st.beacon, 6000)
    }
  }
  io.status(statusLine(snap) || undefined)
}

export function start(io) {}

// agentBadge is the band head's word on the person's agent (FR-L5): "agent ✓"
// while their `landfall serve` session is in the room, dim "agent not in the
// room" when it is not; nothing from a CLI that does not say.
function agentBadge(k, r) {
  const a = r && r.agent
  if (!a || typeof a.inRoom !== 'boolean') return null
  if (a.inRoom) return k.terminal ? k.text(AGENT_IN, { key: 'news-agent', color: TONE.good }) : k.pill(AGENT_IN, 'good', 'news-agent')
  return k.text(AGENT_OUT, { key: 'news-agent', dimColor: true })
}

// youWords is /room's line for both sides of the person's connection:
// "You: this session (Claude Code) · your agent: in the room".
export function youWords(r) {
  const a = r && r.agent
  if (!a || typeof a.inRoom !== 'boolean') return ''
  return 'You: this session (Claude Code) · your agent: ' + (a.inRoom ? 'in the room' : 'not in the room yet')
}

// nowOf reads the session clock (io.now). Where there is none it answers the
// far future, which shows no hint rather than one that never goes.
async function nowOf(io) {
  try {
    if (typeof io.now === 'function') return Number(await io.now())
  } catch {
    // no clock
  }
  return Number.MAX_SAFE_INTEGER
}

// statusLine is the daemon's line with the room at a glance after the room's name, the ask first
// (round 2 review, issue 18: a surface cuts a long line from the right, so the quietest words go
// last, and an emoji cannot be a state color): "1 vote waiting on you · Acme 82 · SEV2 ·
// investigating · 4 here · Beacon investigating · 3 new". While a room's connection is not live
// the line leads with a hollow dot: "○ Acme 82 · …".
export function statusLine(snap) {
  const line = glance(snap)
  if (!line || !(snap.rooms || []).some((r) => notLive(r))) return line
  return '○ ' + line
}

const LEAD_DOT = /^(🔴|🟡|🟢)\s*/u

function glance(snap) {
  const line = (snap.line || '').replace(/^(🔴|🟡|🟢) landfall: /u, '$1 ').replace(LEAD_DOT, '')
  const r = snap.rooms.length === 1 ? snap.rooms[0] : null
  if (!line || !r || !r.status) return line
  const name = r.displayId || r.title || 'landfall'
  if (!line.startsWith(name)) return line
  const st = r.status
  const parts = [st.severity, st.status].filter(Boolean)
  const here = (st.people ?? []).filter((p) => p.here).length
  if (here > 0) parts.push(here + ' here')
  if (st.beacon) parts.push('Beacon ' + st.beacon)
  const votes = waiting(r).length
  const ask = votes > 0 ? (votes === 1 ? '1 vote waiting on you' : votes + ' votes waiting on you') + ' · ' : ''
  // The daemon's own tail says `1 vote awaited`: the ask leads the line now, so it is not said twice.
  const tail = line.slice(name.length).replace(/ · \d+ votes? awaited/g, '')
  if (parts.length === 0) return ask + name + tail
  return ask + name + ' · ' + parts.join(' · ') + tail
}

// roomText is /room's answer where no pane can be drawn (`claude -p`).
export function roomText() {
  const out = []
  const { shown, earlier } = roomsToList(room.snapshot.rooms)
  for (const r of shown) {
    out.push(roomName(r) + ' · ' + r.count + ' new')
    if (notLive(r)) out.push('  ' + RECONNECTING)
    const you = youWords(r)
    if (you) out.push('  ' + you)
    const st = r.status
    if (st) {
      const state = statusWords(st)
      if (state.length > 0) out.push('  ' + state.join(' · '))
      if (st.theory) out.push('  Leading theory: ' + st.theory)
      for (const p of st.people ?? []) out.push('  ' + personLine(p))
    }
    for (const line of r.digest ?? []) out.push('  ' + plainLine(line))
  }
  if (earlier.length > 0) out.push(earlierLine(earlier))
  return out.join('\n')
}

// roomsToList is /room's rooms: the one this session is in now first, in
// full, then any other open room in full; rooms after the first whose
// incident is over are only named, on one dim line (earlierLine). A daemon
// keeps a resolved room for a while, and it read as the current one.
export function roomsToList(rooms) {
  const ordered = orderRooms(rooms)
  const shown = []
  const earlier = []
  ordered.forEach((r, i) => (i > 0 && roomIsOver(r) ? earlier : shown).push(r))
  return { shown, earlier }
}

// earlierLine is the dim line for rooms that are over:
// "Earlier: Acme 166 (resolved), Acme 160 (closed)".
export function earlierLine(rooms) {
  return 'Earlier: ' + rooms.map((r) => (r.displayId || roomName(r)) + (r.status && r.status.status ? ' (' + r.status.status + ')' : '')).join(', ')
}

// tick runs every TICK_MS while the session lives: a component with an open
// pane refreshes it here on its own cadence (nothing to do by default).
export function tick(io, nowMs) {}
