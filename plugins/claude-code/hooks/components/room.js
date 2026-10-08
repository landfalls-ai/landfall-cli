// The room at a glance: the band's news rows (keys 1 to 3), the /room pane,
// who is here under the prompt, the status line, and the toasts for an
// @-mention, a status change and Beacon ending a run.

import {
  ADDRESSED,
  addCommand,
  CATCH_UP,
  clip,
  dot,
  newestLine,
  pending,
  personLine,
  plainLine,
  room,
  roomName,
  statusWords,
  whoIsHere,
} from '../core.js'

export const PANE = 'landfall-room'

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
    return next({ ...e, props: { ...e.props, tail: (e.props.tail ? e.props.tail + '  ' : '') + tail } })
  })

  // The room, in a pane: the room at a glance, then every untold line.
  on('ui.render', { component: 'Pane' }, async ($, e, next) => {
    if (e.requestId !== PANE) return next(e)
    const { Box, Text, Button } = $.ui.resolve(e)
    const width = Math.max(20, (e.props.bodyColumns ?? 80) - 2)
    const rows = []
    for (const r of room.snapshot.rooms) {
      rows.push(Text({ key: 'h-' + r.roomKey, bold: true, children: [clip(roomName(r), width)] }))
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
      if (r.connection && r.connection !== 'live') news.push(r.connection)
      rows.push(Text({ key: 'nh-' + r.roomKey, bold: true, children: ['News · ' + news.join(' · ')] }))
      const lines = r.digest ?? []
      if (lines.length === 0) rows.push(Text({ key: 'n-' + r.roomKey, dimColor: true, children: ['Nothing new since you last spoke.'] }))
      lines.forEach((line, i) => {
        const addressed = line.endsWith(ADDRESSED)
        rows.push(Text({ key: r.roomKey + '-' + i, bold: addressed, children: [clip((addressed ? '@ ' : '  ') + plainLine(line), width)] }))
      })
      rows.push(Text({ key: 's-' + r.roomKey, children: [' '] }))
    }
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
export async function band(io, e, k) {
  const rooms = pending()
  if (rooms.length === 0) return null
  const { Text } = k.els
  const r = rooms[0]
  const total = rooms.reduce((n, x) => n + x.count, 0)
  const head = [roomName(r), total === 1 ? '1 new' : total + ' new']
  if (r.votesAwaited === 1) head.push('1 vote awaited')
  if (r.votesAwaited > 1) head.push(r.votesAwaited + ' votes awaited')
  if (r.connection && r.connection !== 'live') head.push(r.connection)
  const newest = clip(plainLine(newestLine(r.digest ?? [])), k.width)
  const rows = [Text({ key: 'news-h', bold: true, children: ['Landfall · ' + head.join(' · ')] })]
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

// statusLine is the daemon's line with the room at a glance after the room's
// name: "🟡 Acme 82 · mitigated · SEV2 · 4 here · Beacon concluded · 3 new".
export function statusLine(snap) {
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
  for (const r of room.snapshot.rooms) {
    out.push(roomName(r) + ' · ' + r.count + ' new')
    const st = r.status
    if (st) {
      const state = statusWords(st)
      if (state.length > 0) out.push('  ' + state.join(' · '))
      if (st.theory) out.push('  Leading theory: ' + st.theory)
      for (const p of st.people ?? []) out.push('  ' + personLine(p))
    }
    for (const line of r.digest ?? []) out.push('  ' + plainLine(line))
  }
  return out.join('\n')
}
