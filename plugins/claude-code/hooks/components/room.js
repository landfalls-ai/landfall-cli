// The room at a glance: the band's news rows (keys 1 to 3), the /room pane,
// who is here under the prompt, the status line, and the toasts for an
// @-mention, a status change and Beacon ending a run.
//
// Every site draws for its surface (kit.js): the terminal as text and keys,
// exactly as it always has; the desktop Code tab and VS Code as a branded card
// with the Beacon mark, status and severity pills, avatars with presence and
// real buttons; mobile as the same card, compact.
//
// LIVE (live.md). While the room's connection is not live the band and the
// pane say "Reconnecting to the room…" and the status line's dot is hollow
// (FR-L3). For a minute after a new widget lands the band offers `w` to open
// the wall (FR-L4, wall.js). Both sides of the person's connection show: the
// band's head says whether their agent is in the room, and /room says who
// "you" are here and where the agent is (FR-L5). Every new field is optional:
// an older CLI that sends none draws as before.

import {
  ADDRESSED,
  addCommand,
  CATCH_UP,
  clip,
  currentRoom,
  dot,
  newestLine,
  orderRooms,
  pending,
  personLine,
  plainLine,
  room,
  roomIsOver,
  roomName,
  severityTone,
  statusTone,
  statusWords,
  toolsOf,
  whereIs,
  whoIsHere,
} from '../core.js'
import { kit, TONE } from '../kit.js'
import { RECONNECTING, notLive } from '../live.js'
import { openWall, wallHint } from './wall.js'

export const PANE = 'landfall-room'
export const AGENT_IN = 'agent ✓'
export const AGENT_OUT = 'agent not in the room'

// The seq of the newest addressed message per room already shown as a toast.
let toastedAt = {}
// The status and Beacon run each room had at the last snapshot, so a change
// is told once, and the first sight of a room is not told as a change.
let seenStatus = {}

export function install(on) {
  addCommand({ name: 'room', description: 'Show your Landfall war room' })

  on('command.run', { command: 'room' }, async ($) => {
    if (room.snapshot.rooms.length === 0) {
      return { text: 'This folder is not in a war room. Open a share link from the room to join it.' }
    }
    const opened = await $.ui.open({ id: PANE, title: 'Landfall', focus: true, closeOnEscape: true })
    if (!opened.isPlaced) return { text: roomText() }
    return {}
  })


  // Under the prompt: who else is in the room, while the person is not typing.
  on('ui.render', { component: 'PromptHint' }, async ($, e, next) => {
    if (e.props.isDraft) return next(e)
    const tail = whoIsHere()
    if (!tail) return next(e)
    if (e.surface === 'terminal') {
      return next({ ...e, props: { ...e.props, tail: (e.props.tail ? e.props.tail + '  ' : '') + tail } })
    }
    // Only the terminal draws `tail`: elsewhere the line is drawn here, the
    // engine's own hint first, then who is here as avatars.
    return hintRich(kit($.ui.resolve(e), e), e.props.hint)
  })

  // The room, in a pane: the room at a glance, then every untold line.
  on('ui.render', { component: 'Pane' }, async ($, e, next) => {
    if (e.requestId !== PANE) return next(e)
    if (e.surface !== 'terminal') {
      return paneRich(kit($.ui.resolve(e), e), {
        catchUp: async () => {
          await $.ui.close({ id: PANE })
          await $.prompt.fill({ text: CATCH_UP })
        },
        close: () => $.ui.close({ id: PANE }),
      })
    }
    const { Box, Text, Button } = $.ui.resolve(e)
    const width = Math.max(20, (e.props.bodyColumns ?? 80) - 2)
    const rows = []
    const { shown, earlier } = roomsToList(room.snapshot.rooms)
    for (const r of shown) {
      rows.push(Text({ key: 'h-' + r.roomKey, bold: true, children: [clip(roomName(r), width)] }))
      if (notLive(r)) rows.push(Text({ key: 'conn-' + r.roomKey, color: TONE.warning, children: [RECONNECTING] }))
      const you = youWords(r)
      if (you) rows.push(Text({ key: 'you-' + r.roomKey, children: [clip(you, width)] }))
      const st = r.status
      if (st) {
        const state = statusWords(st)
        if (state.length > 0) rows.push(Text({ key: 'st-' + r.roomKey, children: [clip(state.join(' · '), width)] }))
        if (st.theory) rows.push(Text({ key: 'th-' + r.roomKey, dimColor: true, children: [clip('Leading theory: ' + st.theory, width)] }))
        const people = st.people ?? []
        if (people.length > 0) {
          rows.push(Text({ key: 'ps-' + r.roomKey, children: [' '] }))
          rows.push(Text({ key: 'ph-' + r.roomKey, bold: true, children: ['In the room (' + people.filter((p) => p.here).length + ' here)'] }))
          people.forEach((p, i) => {
            rows.push(Text({ key: r.roomKey + '-p' + i, dimColor: !p.here, children: [clip(personLine(p), width)] }))
          })
        }
        rows.push(Text({ key: 'pe-' + r.roomKey, children: [' '] }))
      }
      const news = [r.count === 1 ? '1 new' : r.count + ' new']
      rows.push(Text({ key: 'nh-' + r.roomKey, bold: true, children: ['News · ' + news.join(' · ')] }))
      const lines = r.digest ?? []
      if (lines.length === 0) rows.push(Text({ key: 'n-' + r.roomKey, dimColor: true, children: ['Nothing new since you last spoke.'] }))
      lines.forEach((line, i) => {
        const addressed = line.endsWith(ADDRESSED)
        rows.push(Text({ key: r.roomKey + '-' + i, bold: addressed, children: [clip((addressed ? '@ ' : '  ') + plainLine(line), width)] }))
      })
      rows.push(Text({ key: 's-' + r.roomKey, children: [' '] }))
    }
    if (earlier.length > 0) rows.push(Text({ key: 'earlier', dimColor: true, children: [clip(earlierLine(earlier), width)] }))
    const line = statusLine(room.snapshot)
    if (line) rows.push(Text({ key: 'line', dimColor: true, children: [clip(line, width)] }))
    rows.push(
      Box({
        key: 'actions',
        flexDirection: 'row',
        columnGap: 2,
        children: [
          Button({
            key: 'pane-catch-up',
            label: 'catch up',
            hotkey: 'c',
            plain: true,
            onPress: async () => {
              await $.ui.close({ id: PANE })
              await $.prompt.fill({ text: CATCH_UP })
            },
          }),
          Button({ key: 'pane-close', label: 'close (esc)', plain: true, dimColor: true, onPress: () => $.ui.close({ id: PANE }) }),
        ],
      }),
    )
    return Box({ flexDirection: 'column', children: rows })
  })

}

// band: the room's news, the untold count, the newest line, three keys.
// With no news it still says when the room is reconnecting, and offers the
// wall for a minute after a new widget.
export async function band(io, e, k) {
  const rooms = pending()
  const cur = rooms[0] || currentRoom()
  const hint = cur ? wallHint(await nowOf(io)) : null
  if (rooms.length === 0 && !notLive(cur) && !hint) return null
  if (!k.terminal) return bandRich(io, k, rooms, cur, hint)
  const { Text } = k.els
  const r = cur
  const rows = []
  if (notLive(r)) rows.push(Text({ key: 'news-conn', color: TONE.warning, children: [RECONNECTING] }))
  const total = rooms.reduce((n, x) => n + x.count, 0)
  const head = [roomName(r)]
  if (total > 0) head.push(total === 1 ? '1 new' : total + ' new')
  if (r.votesAwaited === 1) head.push('1 vote awaited')
  if (r.votesAwaited > 1) head.push(r.votesAwaited + ' votes awaited')
  const headText = Text({ key: 'news-h', bold: true, children: ['Landfall · ' + head.join(' · ')] })
  const badge = agentBadge(k, r)
  rows.push(badge ? k.row([headText, badge], 'news-hrow', 2) : headText)
  if (hint) rows.push(hintRow(io, k, hint))
  if (rooms.length === 0) return rows
  const newest = clip(plainLine(newestLine(r.digest ?? [])), k.width)
  if (newest) rows.push(Text({ key: 'news-l', dimColor: true, children: [newest] }))
  rows.push(
    k.row(
      [
        k.button({ key: 'catch-up', label: 'catch up', hotkey: '1', primary: true, onPress: async () => void (await io.fill(CATCH_UP)) }),
        k.button({ key: 'show', label: 'show the room', hotkey: '2', onPress: async () => void (await io.open(PANE, 'Landfall', { focus: true, closeOnEscape: true })) }),
        k.button({
          key: 'later',
          label: 'later',
          hotkey: '3',
          dim: true,
          onPress: () => {
            for (const x of rooms) room.laterAt[x.roomKey] = x.maxSeq
            io.invalidate()
          },
        }),
      ],
      'news-keys',
    ),
  )
  return rows
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

// hintRow is the band's offer after a new widget: `w` opens the wall.
function hintRow(io, k, hint) {
  return k.row(
    [
      k.button({ key: 'open-wall', label: 'open the wall', hotkey: 'w', onPress: async () => void (await openWall(io)) }),
      k.text('New: ' + clip(hint.title, 60), { key: 'wall-new', dimColor: true }),
    ],
    'news-wall',
  )
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

// --- off the terminal: the desktop Code tab, VS Code, mobile ---------------

// roomPills is a room's severity and status as pills. A connection that is
// not live is said in words above them (RECONNECTING), not as a pill.
function roomPills(k, r, key) {
  const st = r.status || {}
  const out = []
  if (st.severity) out.push(k.pill(st.severity, severityTone(st.severity), key + '-sev'))
  if (st.status) out.push(k.pill(st.status, statusTone(st.status), key + '-st'))
  return out
}

// bandRich is the band as one card: the room with its pills and who is here,
// the newest line with Beacon's state, then Catch up, Show the room, Later.
// With no news it is the reconnecting line and the wall offer alone.
function bandRich(io, k, rooms, r, hint) {
  const { Box, Text } = k.els
  const st = r.status || {}
  const total = rooms.reduce((n, x) => n + x.count, 0)
  const name = roomName(r)
  const title = Box({
    key: 'news-title',
    flexDirection: 'row',
    columnGap: 1,
    flexWrap: 'wrap',
    alignItems: 'center',
    children: [
      k.mark('news-mark', 18),
      Text({ key: 'news-name', bold: true, children: [k.mobile ? clip(name, 28) : name] }),
      ...roomPills(k, r, 'news'),
      total > 0 ? k.pill(total === 1 ? '1 new' : total + ' new', 'neutral', 'news-count') : null,
      agentBadge(k, r),
    ].filter(Boolean),
  })
  const people = st.people ?? []
  const here = people.filter((p) => p.here)
  let head = title
  if (k.mobile) {
    // Compact: who is here as a count, not faces.
    if (here.length > 0) head = k.col([title, k.dim(here.length + ' here', 'news-here')], 'news-head')
  } else if (here.length > 0) {
    head = Box({ key: 'news-head', flexDirection: 'row', justifyContent: 'space-between', flexWrap: 'wrap', columnGap: 2, children: [title, k.avatars(here, { key: 'news-avs', max: 5 })] })
  }
  const conn = notLive(r) ? Text({ key: 'news-conn', color: TONE.warning, children: [RECONNECTING] }) : null
  if (rooms.length === 0) {
    return [k.card([conn, head, hint ? hintRow(io, k, hint) : null], { key: 'news-card', tone: notLive(r) ? 'warning' : undefined })]
  }
  const said = plainLine(newestLine(r.digest ?? []))
  const tail = []
  if (r.votesAwaited === 1) tail.push('1 vote awaited')
  if (r.votesAwaited > 1) tail.push(r.votesAwaited + ' votes awaited')
  if (st.beacon) tail.push('Beacon ' + st.beacon)
  const line = [said, ...tail].filter(Boolean).join(' · ')
  const keys = [
    k.button({ key: 'catch-up', label: 'Catch up', hotkey: '1', primary: true, onPress: async () => void (await io.fill(CATCH_UP)) }),
    k.mobile ? null : k.button({ key: 'show', label: 'Show the room', hotkey: '2', onPress: async () => void (await io.open(PANE, 'Landfall', { focus: true, closeOnEscape: true })) }),
    k.button({
      key: 'later',
      label: 'Later',
      hotkey: '3',
      dim: true,
      onPress: () => {
        for (const x of rooms) room.laterAt[x.roomKey] = x.maxSeq
        io.invalidate()
      },
    }),
  ]
  return [
    k.card(
      [conn, head, line ? Text({ key: 'news-l', dimColor: true, children: [clip(line, k.mobile ? 90 : Math.max(40, k.width * 2))] }) : null, hint ? hintRow(io, k, hint) : null, k.row(keys, 'news-keys', 1)],
      { key: 'news-card', tone: severityTone(st.severity) === 'critical' ? 'critical' : undefined },
    ),
  ]
}

// paneRich is /room as cards: each room at a glance, everyone in it with
// their avatar, where they work and what their agents are doing, then the news.
function paneRich(k, act) {
  const { Box, Text } = k.els
  const cards = []
  const { shown, earlier } = roomsToList(room.snapshot.rooms)
  for (const r of shown) {
    const key = 'room-' + r.roomKey
    const st = r.status
    const kids = [
      Box({
        key: key + '-h',
        flexDirection: 'row',
        columnGap: 1,
        flexWrap: 'wrap',
        alignItems: 'center',
        children: [k.mark(key + '-m', 20), Text({ key: key + '-name', bold: true, children: [roomName(r)] }), ...roomPills(k, r, key)],
      }),
    ]
    if (notLive(r)) kids.push(Text({ key: key + '-conn', color: TONE.warning, children: [RECONNECTING] }))
    const you = youWords(r)
    if (you) kids.push(Text({ key: key + '-you', children: [you] }))
    if (st) {
      if (st.beacon) kids.push(k.dim('Beacon ' + st.beacon, key + '-beacon'))
      if (st.theory) kids.push(k.quote('Leading theory: ' + st.theory, key + '-theory'))
      const people = st.people ?? []
      if (people.length > 0) {
        kids.push(Text({ key: key + '-ph', bold: true, children: ['In the room (' + people.filter((p) => p.here).length + ' here)'] }))
        people.forEach((p, i) => kids.push(personRow(k, p, key + '-p' + i)))
      }
    }
    const news = [r.count === 1 ? '1 new' : r.count + ' new']
    kids.push(Text({ key: key + '-nh', bold: true, children: ['News · ' + news.join(' · ')] }))
    const lines = r.digest ?? []
    if (lines.length === 0) kids.push(k.dim('Nothing new since you last spoke.', key + '-none'))
    lines.forEach((line, i) => {
      const addressed = line.endsWith(ADDRESSED)
      kids.push(Text({ key: key + '-n' + i, bold: addressed, children: [(addressed ? '@ ' : '') + plainLine(line)] }))
    })
    cards.push(k.card(kids, { key, tone: st && severityTone(st.severity) === 'critical' ? 'critical' : undefined }))
  }
  if (earlier.length > 0) cards.push(k.dim(earlierLine(earlier), 'earlier'))
  const line = statusLine(room.snapshot)
  if (line) cards.push(k.dim(line, 'line'))
  cards.push(
    k.row(
      [
        k.button({ key: 'pane-catch-up', label: 'Catch up', hotkey: 'c', primary: true, onPress: act.catchUp }),
        k.button({ key: 'pane-close', label: 'Close', dismiss: true, onPress: act.close }),
      ],
      'actions',
      1,
    ),
  )
  return Box({ flexDirection: 'column', rowGap: 1, children: cards })
}

// personRow is one person: avatar, name, then where they work and what their
// agents are doing.
function personRow(k, p, key) {
  const { Box, Text } = k.els
  const tools = toolsOf(p)
  const detail = (tools.length > 0 ? tools : p.here ? [] : ['away']).join(' · ')
  const name = Text({ key: key + '-n', bold: true, dimColor: !p.here, children: [p.name + (p.you ? ' (you)' : '')] })
  if (!k.rich) return Text({ key, dimColor: !p.here, children: [personLine(p)] })
  return Box({
    key,
    flexDirection: 'row',
    columnGap: 1,
    alignItems: 'center',
    children: [
      k.avatar(p.name, !!p.here, { key: key + '-av', you: !!p.you }),
      Box({ key: key + '-c', flexDirection: 'column', children: [name, detail ? k.dim(detail, key + '-d') : null].filter(Boolean) }),
    ],
  })
}

// hintRich is the line under the prompt off the terminal: the engine's hint,
// then the others who are here, as avatars with their names.
function hintRich(k, hint) {
  const { Box, Text } = k.els
  const r = currentRoom()
  const others = (r?.status?.people ?? []).filter((p) => p.here && !p.you)
  const shown = others.slice(0, k.mobile ? 2 : 4)
  const kids = []
  if (hint) kids.push(Text({ key: 'hint', dimColor: true, children: [hint] }))
  kids.push(Text({ key: 'here', dimColor: true, children: ['Here:'] }))
  shown.forEach((p, i) => {
    const where = whereIs(p)
    kids.push(k.avatar(p.name, true, { key: 'hint-av' + i, px: 16 }))
    kids.push(Text({ key: 'hint-n' + i, dimColor: true, children: [where ? p.name + ' (' + where + ')' : p.name] }))
  })
  if (others.length > shown.length) kids.push(Text({ key: 'hint-more', dimColor: true, children: [others.length - shown.length + ' more'] }))
  return Box({ flexDirection: 'row', columnGap: 1, flexWrap: 'wrap', alignItems: 'center', children: kids })
}

// statusLine is the daemon's line with the room at a glance after the room's
// name: "🟡 Acme 82 · mitigated · SEV2 · 4 here · Beacon concluded · 3 new".
// While a room's connection is not live the dot is hollow: "○ Acme 82 · …".
export function statusLine(snap) {
  const line = glance(snap)
  if (!line || !(snap.rooms || []).some((r) => notLive(r))) return line
  return line.replace(/^(🔴|🟡|🟢)/u, '○')
}

function glance(snap) {
  const line = (snap.line || '').replace(/^🔴 landfall: /, '🔴 ')
  const r = snap.rooms.length === 1 ? snap.rooms[0] : null
  if (!line || !r || !r.status) return line
  const head = '🔴 ' + (r.displayId || r.title || 'landfall')
  if (!line.startsWith(head)) return line
  const st = r.status
  const parts = [st.status, st.severity].filter(Boolean)
  const here = (st.people ?? []).filter((p) => p.here).length
  if (here > 0) parts.push(here + ' here')
  if (st.beacon) parts.push('Beacon ' + st.beacon)
  if (parts.length === 0) return line
  return dot(st.status) + head.slice('🔴'.length) + ' · ' + parts.join(' · ') + line.slice(head.length)
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
