// A mirror of the investigation wall: /wall (proposal item 06, FR-06).
//
// `landfall wall` answers the room's widgets in the arrangement the incident
// commander shared, each with its data read through the person's own session,
// so the per-viewer credential wall holds: nobody sees a series they could
// not read. The pane draws every widget type natively (vector charts on the
// desktop, cell graphics on the terminal), reads the wall again on `r`, and
// again on its own when the room moves on, at most every 20 seconds while the
// pane is open. `a` drafts a question about the selected widget; it never sends.

import { HOST, addCommand, currentRoom, parseAnswer, room, roomName } from '../core.js'
import { kit } from '../kit.js'
import {
  chartAlt,
  chartSvg,
  clipText,
  eventLine,
  fmt,
  graphLines,
  graphView,
  hhmm,
  levelTag,
  levelTone,
  markerCaption,
  outcomeTone,
  seriesTone,
  tableRows,
  toneColor,
  values,
  windowWords,
} from '../views.js'

export const PANE = 'landfall-wall'
export const REFRESH_MS = 20000

// What the pane draws from. Module state: a hot reload starts it over, and
// the next /wall or `r` fills it again.
const wall = {
  answer: null, // the last `landfall wall` answer
  loading: false,
  open: false,
  roomKey: '',
  loadedSeq: -1, // the room's maxSeq when the wall was last read
  loadedAt: 0, // $.clock time of the last read
  seenAt: 0, // $.clock time the pane last drew
  drawnSnap: -1, // which snapshot the pane last drew after
  snaps: 0, // snapshots seen
  selected: 0,
}

export function install(on) {
  addCommand({ name: 'wall', description: "Show the war room's investigation wall" })

  on('command.run', { command: 'wall' }, async ($) => {
    const r = currentRoom()
    const opened = await $.ui.open({ id: PANE, title: paneTitle(r), focus: true, closeOnEscape: true })
    if (!opened || !opened.isPlaced) {
      await loadWall($)
      return { text: wallText(wall.answer, r) }
    }
    wall.open = true
    wall.drawnSnap = wall.snaps
    await loadWall($)
    return {}
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const k = kit($.ui.resolve(e), e)
    try {
      wall.seenAt = await $.clock.now()
    } catch {
      // No clock: the wall waits for r rather than reading on its own.
    }
    wall.drawnSnap = wall.snaps
    wall.open = true
    const r = currentRoom()
    const a = wall.answer
    const rows = []
    const by = a && a.ok && a.sharedBy ? 'Wall · shared by ' + a.sharedBy : 'Wall'
    const dim = [r ? roomName(r) : '', a && a.ok ? windowWords(a.windowMs) : '', wall.loading ? 'reading…' : ''].filter(Boolean).join(' · ')
    rows.push(k.header({ key: 'wall-h', title: by, dim }))
    if (!a) {
      rows.push(k.text(wall.loading ? 'Reading the wall through your session…' : 'Press r to read the wall.', { key: 'wall-empty', dimColor: true }))
    } else if (!a.ok) {
      rows.push(k.text(clipText(a.error || 'The wall could not be read.', k.width), { key: 'wall-err' }))
    } else {
      const widgets = a.widgets || []
      if (widgets.length === 0) rows.push(k.text('Nothing is on the wall yet.', { key: 'wall-none', dimColor: true }))
      const two = k.rich && k.width >= 90
      const cards = widgets.map((w, i) => card($, k, w, i, two))
      if (two) rows.push(k.els.Box({ key: 'wall-grid', flexDirection: 'row', flexWrap: 'wrap', columnGap: 2, rowGap: 1, children: cards }))
      else rows.push(k.col(cards, 'wall-list', 1))
      const gone = a.unavailable || []
      if (gone.length > 0) {
        rows.push(k.text('Not shown here', { key: 'un-h', bold: true }))
        gone.forEach((u, i) => rows.push(k.text(clipText((u.title || u.id) + ': ' + (u.reason || 'not available'), k.width), { key: 'un-' + i, dimColor: true })))
      }
    }
    const sel = selectedWidget()
    rows.push(
      k.row(
        [
          sel ? k.button({ key: 'wall-ask', label: 'ask about ' + clipText(sel.title || sel.type, 28), hotkey: 'a', primary: true, onPress: () => askAbout($, sel) }) : null,
          a && a.ok && (a.widgets || []).length > 1 ? k.button({ key: 'wall-next', label: 'next widget', hotkey: 'n', onPress: () => selectNext($) }) : null,
          k.button({ key: 'wall-refresh', label: 'refresh', hotkey: 'r', onPress: () => loadWall($) }),
          k.button({ key: 'wall-close', label: 'close (esc)', dim: true, onPress: () => closeWall($) }),
        ],
        'wall-keys',
      ),
    )
    return k.col(rows, 'wall')
  })
}

export async function band(io, e, k) {
  return null
}

// onSnapshot reads the wall again when the room has moved on, while the pane
// is open, at most every 20 seconds. The pane is open when it drew after the
// previous snapshot (each snapshot redraws every open site).
export function onSnapshot(io, snap, prev) {
  const wasDrawn = wall.drawnSnap >= wall.snaps
  wall.snaps += 1
  if (!wall.open) return
  if (!wasDrawn) {
    wall.open = false
    return
  }
  if (wall.loading) return
  const r = (snap.rooms || []).find((x) => x.roomKey === wall.roomKey) || snap.rooms[0]
  if (!r || !(r.maxSeq > wall.loadedSeq)) return
  if (wall.seenAt - wall.loadedAt < REFRESH_MS) return
  void reloadFromSnapshot(io, r)
}

export function start(io) {}

async function reloadFromSnapshot(io, r) {
  wall.loading = true
  wall.loadedSeq = r.maxSeq
  wall.loadedAt = wall.seenAt
  try {
    const answer = await io.run(wallArgs(r))
    wall.answer = answer
  } catch (err) {
    wall.answer = { ok: false, error: String(err).slice(0, 200) }
  }
  wall.loading = false
  io.invalidate()
}

// loadWall runs `landfall wall` for the current room.
async function loadWall($) {
  if (wall.loading) return
  const r = currentRoom()
  wall.loading = true
  wall.roomKey = r ? r.roomKey : ''
  wall.loadedSeq = r ? r.maxSeq ?? -1 : -1
  try {
    wall.loadedAt = await $.clock.now()
  } catch {
    wall.loadedAt = 0
  }
  $.ui.invalidate('ui.render')
  try {
    wall.answer = parseAnswer(await $.process.run([room.bin, ...wallArgs(r)], { timeoutMs: 30000 }))
  } catch (err) {
    wall.answer = { ok: false, error: String(err).slice(0, 200) }
  }
  wall.loading = false
  const n = wall.answer && wall.answer.ok ? (wall.answer.widgets || []).length : 0
  if (wall.selected >= n) wall.selected = 0
  $.ui.invalidate('ui.render')
}

async function askAbout($, w) {
  const r = currentRoom()
  const where = r ? r.displayId || roomName(r) : 'the war room'
  await $.prompt.fill({ text: 'Tell me about the ' + (w.title || w.type) + ' widget in ' + where + '.' })
}

function selectNext($) {
  const n = wall.answer && wall.answer.ok ? (wall.answer.widgets || []).length : 0
  if (n > 0) wall.selected = (wall.selected + 1) % n
  $.ui.invalidate('ui.render')
}

function selectWidget($, i) {
  wall.selected = i
  $.ui.invalidate('ui.render')
}

async function closeWall($) {
  wall.open = false
  await $.ui.close({ id: PANE })
}

// card is one widget: its title (a press selects it), then its body.
function card($, k, w, i, two) {
  const key = 'w-' + (w.id || i)
  const chosen = i === wall.selected
  const inner = two ? Math.max(20, Math.floor(k.width / 2) - 4) : k.width
  const head = k.row(
    [
      k.els.Button({ key: key + '-t', label: (chosen ? '▸ ' : '') + clipText(w.title || w.type || 'widget', inner - 4), plain: true, onPress: () => selectWidget($, i) }),
      w.tone && w.tone !== 'neutral' ? k.pill(w.tone, w.tone, key + '-p') : null,
    ],
    key + '-head',
    1,
  )
  const body = widgetBody(k, w, key, two ? 300 : 560, inner)
  const props = { key, flexDirection: 'column', children: [head, ...body].filter(Boolean) }
  if (k.rich) {
    props.borderStyle = 'round'
    props.paddingX = 1
    if (two) props.width = '48%'
  }
  return k.els.Box(props)
}

// widgetBody draws one widget by type; a type this mod does not know is its
// title alone.
export function widgetBody(k, w, key, px, cells) {
  const { Text } = k.els
  switch (w.type) {
    case 'stat': {
      const value = Text({ key: key + '-v', bold: true, color: toneColor(w.tone || 'neutral'), children: [String(w.value ?? '')] })
      const unit = w.unit ? Text({ key: key + '-u', dimColor: true, children: [String(w.unit)] }) : null
      return [k.row([value, unit], key + '-vr', 0), k.spark(w.spark, { key: key + '-s', tone: w.tone && w.tone !== 'neutral' ? w.tone : 'info', width: Math.min(40, cells), px, height: 34, label: (w.title || 'stat') + ' trend' })]
    }
    case 'chart': {
      const series = (w.series || []).filter((s) => values(s).length > 0)
      if (series.length === 0) return [k.text('No points in this window.', { key: key + '-none', dimColor: true })]
      if (k.rich) {
        const h = 90
        return [
          k.svg(chartSvg(series, w.markers, w.threshold, { width: px, height: h, tone: w.tone }), { key: key + '-svg', alt: chartAlt(w.title, series, w.markers, w.threshold), width: px, height: h, hover: true }),
          caption(k, w, key),
        ]
      }
      const out = []
      const cols = Math.min(60, Math.max(10, cells - 2))
      series.slice(0, 4).forEach((s, i) => {
        const v = values(s)
        const tone = seriesTone(i, w.tone)
        out.push(k.text(clipText((s.label || 'series ' + (i + 1)) + '  last ' + fmt(v[v.length - 1]) + '  peak ' + fmt(Math.max(...v)), cells), { key: key + '-l' + i, dimColor: true }))
        const over = typeof w.threshold === 'number' ? (x) => (x >= w.threshold ? 'critical' : tone) : undefined
        out.push(k.spark(v, { key: key + '-s' + i, tone, width: cols, toneFor: over, label: s.label }))
        if (i === 0) {
          markerCaption(s, w.markers, Math.min(cols, v.length), cells).forEach((line, j) => out.push(k.text(line, { key: key + '-m' + j, dimColor: true })))
        }
      })
      if (series.length > 4) out.push(k.text(series.length - 4 + ' more series', { key: key + '-more', dimColor: true }))
      if (typeof w.threshold === 'number') out.push(k.text('threshold ' + fmt(w.threshold), { key: key + '-thr', dimColor: true }))
      return out
    }
    case 'logView': {
      const lines = (w.lines || []).slice(-8)
      if (lines.length === 0) return [k.text('No log lines in this window.', { key: key + '-none', dimColor: true })]
      return lines.map((l, i) =>
        k.row(
          [
            Text({ key: 'lv', bold: true, color: toneColor(levelTone(l.level)), children: [levelTag(l.level)] }),
            Text({ key: 'lt', children: [clipText(l.text, Math.max(10, cells - 4))] }),
          ],
          key + '-ln' + i,
          1,
        ),
      )
    }
    case 'table': {
      const lines = tableRows(w.columns, w.rows, cells)
      if (lines.length === 0) return [k.text('No rows.', { key: key + '-none', dimColor: true })]
      return lines.map((l, i) => k.text(l, { key: key + '-r' + i, bold: i === 0, dimColor: i > 0 && i === lines.length - 1 && /more rows$/.test(l) }))
    }
    case 'events': {
      const evs = (w.events || []).slice(-8)
      if (evs.length === 0) return [k.text('No events in this window.', { key: key + '-none', dimColor: true })]
      return evs.map((ev, i) => {
        const tone = outcomeTone(ev.outcome)
        const props = { key: key + '-e' + i, children: [clipText(eventLine(ev), cells)] }
        if (tone !== 'neutral') props.color = toneColor(tone)
        return Text(props)
      })
    }
    case 'timeline': {
      const items = (w.items || []).slice(-8)
      if (items.length === 0) return [k.text('Nothing on this timeline yet.', { key: key + '-none', dimColor: true })]
      return items.map((it, i) =>
        k.row(
          [
            Text({ key: 'ta', dimColor: true, children: [hhmm(it.at)] }),
            Text({ key: 'tg', color: toneColor(it.tone || 'neutral'), children: ['●'] }),
            Text({ key: 'tl', children: [clipText(it.label, Math.max(10, cells - 9))] }),
          ],
          key + '-i' + i,
          1,
        ),
      )
    }
    case 'graph':
      return graphView(k, w, key)
    default:
      return [k.text((w.type ? w.type + ' widgets' : 'This widget') + ' draw in the web app.', { key: key + '-x', dimColor: true })]
  }
}

function caption(k, w, key) {
  const parts = (w.markers || []).map((m) => m.label + ' at ' + hhmm(m.atMs) + 'Z')
  if (typeof w.threshold === 'number') parts.push('threshold ' + fmt(w.threshold))
  if (parts.length === 0) return null
  return k.text(parts.join(' · '), { key: key + '-cap', dimColor: true })
}

function selectedWidget() {
  const a = wall.answer
  if (!a || !a.ok) return null
  return (a.widgets || [])[wall.selected] || null
}

function paneTitle(r) {
  return r && r.displayId ? 'Wall · ' + r.displayId : 'Wall'
}

export function wallArgs(r) {
  const args = ['wall', '--host', HOST]
  if (r && r.roomKey) args.push('--room', r.roomKey)
  return args
}

// wallText is /wall's answer where no pane can be drawn.
export function wallText(a, r) {
  if (!a) return 'The wall could not be read.'
  if (!a.ok) return a.error || 'The wall could not be read.'
  const out = [(a.sharedBy ? 'Wall · shared by ' + a.sharedBy : 'Wall') + (r ? ' · ' + roomName(r) : '') + (a.windowMs ? ' · ' + windowWords(a.windowMs) : '')]
  const widgets = a.widgets || []
  if (widgets.length === 0) out.push('Nothing is on the wall yet.')
  for (const w of widgets) {
    out.push('')
    out.push(w.title || w.type)
    for (const line of widgetLines(w)) out.push('  ' + line)
  }
  const gone = a.unavailable || []
  if (gone.length > 0) {
    out.push('')
    out.push('Not shown here:')
    for (const u of gone) out.push('  ' + (u.title || u.id) + ': ' + (u.reason || 'not available'))
  }
  return out.join('\n')
}

function widgetLines(w) {
  switch (w.type) {
    case 'stat':
      return [String(w.value ?? '') + (w.unit || '')]
    case 'chart': {
      const out = []
      for (const s of w.series || []) {
        const v = values(s)
        if (v.length) out.push((s.label || 'series') + ': last ' + fmt(v[v.length - 1]) + ', peak ' + fmt(Math.max(...v)))
      }
      for (const m of w.markers || []) out.push('marker: ' + m.label + ' at ' + hhmm(m.atMs) + 'Z')
      if (typeof w.threshold === 'number') out.push('threshold ' + fmt(w.threshold))
      return out.length ? out : ['No points in this window.']
    }
    case 'logView':
      return (w.lines || []).slice(-8).map((l) => levelTag(l.level) + ' ' + l.text)
    case 'table':
      return tableRows(w.columns, w.rows, 100)
    case 'events':
      return (w.events || []).slice(-8).map(eventLine)
    case 'timeline':
      return (w.items || []).slice(-8).map((it) => hhmm(it.at) + ' ' + it.label)
    case 'graph':
      return graphLines(w.nodes, w.edges)
    default:
      return [(w.type ? w.type + ' widgets' : 'This widget') + ' draw in the web app.']
  }
}

