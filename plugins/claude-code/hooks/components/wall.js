// A mirror of the investigation wall: /wall (proposal item 06, FR-06).
//
// `landfall wall` answers the room's widgets in the arrangement the incident
// commander shared, each with its data read through the person's own session,
// so the per-viewer credential wall holds: nobody sees a series they could
// not read. The pane draws every widget type natively (vector charts on the
// desktop, cell graphics on the terminal) and reads the wall again on `r`.
// While the pane is open it is live (live.md FR-L2, FR-L3): it reads again
// every 15 s, as the web canvas does, and right away when a widget event
// lands (`widgetSeq` on the watch stream); a failed read keeps the last good
// wall and says it is stale; the last line says how old what it shows is.
// A new widget is told as a toast (FR-L4), and the band offers `w` to open
// the wall for a minute after. `a` drafts a question about the selected
// widget; it never sends.
//
// The shapes are the Go CLI's own (internal/cli/wall.go flattenWidget): a
// widget with no data yet carries `empty: true`; a stat may carry trend,
// delta, deltaLabel and baselineLabel (drawn as the web app's StatBody does);
// a graph's nodes may carry kind and its edges direction; geo carries points
// by place and codeFinding a repo, path, permalink and snippet lines. When
// the wall holds more widgets than the CLI reads at once, `totalWidgets` says
// how many there are and the pane says so.

import { HOST, addCommand, currentRoom, parseAnswer, room, roomName } from '../core.js'
import { kit } from '../kit.js'
import { closed, drawn, due, liveFooter, livePane, readLive, WALL_MS, widgetSeqOf, opened as markOpen } from '../live.js'
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
// The web canvas's own cadence (useLiveWidgets intervalMs): an open wall
// reads its data again this often, and within a tick of a widget event.
export const REFRESH_MS = WALL_MS
// New widgets are told at most this often; the band offers `w` this long after.
export const NEWS_GAP_MS = 10000
export const HINT_MS = 60000

// The pane's live read (live.js): the last good `landfall wall` answer, open
// or not, stale or not. Module state: a hot reload starts it over, and the
// next draw of the pane marks it open again.
const lp = livePane()
const wall = {
  roomKey: '',
  selected: 0,
}

// New widgets (FR-L4): the newest widget seq seen per room (the first sight
// of a room is not news), one waiting while the toast gap runs, when the last
// toast was shown and until when the band offers `w`.
const news = { seen: {}, pending: null, toastAt: null, hintUntil: 0, hintTitle: '' }

export function install(on) {
  addCommand({ name: 'wall', description: "Show the war room's investigation wall" })

  on('command.run', { command: 'wall' }, async ($) => {
    const r = currentRoom()
    const opened = await $.ui.open({ id: PANE, title: paneTitle(r), focus: true, closeOnEscape: true })
    if (!opened || !opened.isPlaced) {
      await loadWall(paneIo($))
      return { text: wallText(lp.last, r) }
    }
    markOpen(lp)
    await loadWall(paneIo($))
    return {}
  })

  // Closed by the person (esc, the close mark) or an unload: the wall stops
  // reading. The mod's own close (closeWall) marks it itself, since a
  // plugin's own $.ui.close is not raised to its own hooks.
  on('ui.close', { id: PANE }, async ($, e, next) => {
    closed(lp)
    return next(e)
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const k = kit($.ui.resolve(e), e)
    // Drawn means open: after a hot reload the engine draws the pane again
    // and the wall goes on reading.
    drawn(lp)
    const nowMs = await clockNow($)
    const r = currentRoom()
    const a = lp.answer
    const rows = []
    const by = a && a.ok && a.sharedBy ? 'Wall · shared by ' + a.sharedBy : 'Wall'
    const dim = [r ? roomName(r) : '', a && a.ok ? windowWords(a.windowMs) : '', lp.inFlight ? 'reading…' : ''].filter(Boolean).join(' · ')
    rows.push(k.header({ key: 'wall-h', title: by, dim }))
    if (!a) {
      rows.push(k.text(lp.inFlight ? 'Reading the wall through your session…' : 'Press r to read the wall.', { key: 'wall-empty', dimColor: true }))
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
        gone.forEach((u, i) => rows.push(k.text((u.title || u.type || u.id) + ': ' + (u.reason || 'not available'), { key: 'un-' + i, dimColor: true })))
      }
      const more = moreWords(a)
      if (more) rows.push(k.text(more, { key: 'wall-more', dimColor: true }))
    }
    rows.push(liveFooter(k, lp, nowMs, r, 'wall-live'))
    const sel = selectedWidget()
    rows.push(
      k.row(
        [
          sel ? k.button({ key: 'wall-ask', label: 'ask about ' + clipText(sel.title || sel.type, 28), hotkey: 'a', primary: true, onPress: () => askAbout($, sel) }) : null,
          a && a.ok && (a.widgets || []).length > 1 ? k.button({ key: 'wall-next', label: 'next widget', hotkey: 'n', onPress: () => selectNext($) }) : null,
          k.button({ key: 'wall-refresh', label: 'refresh', hotkey: 'r', onPress: () => loadWall(paneIo($)) }),
          k.button({ key: 'wall-close', label: 'close (esc)', dim: true, onPress: () => closeWall($) }),
        ],
        'wall-keys',
      ),
    )
    return k.col(rows.filter(Boolean), 'wall')
  })
}

export async function band(io, e, k) {
  return null
}

// onSnapshot: a widget landed on the wall. Told as a toast (FR-L4), and an
// open wall reads again right away (FR-L2: within 2 s of the event).
export function onSnapshot(io, snap, prev) {
  for (const r of snap.rooms || []) {
    const nw = r.newestWidget
    const seq = nw && typeof nw.seq === 'number' ? nw.seq : null
    const seen = news.seen[r.roomKey]
    if (seen == null) {
      // The first sight of a room is what is already there, not news.
      news.seen[r.roomKey] = seq ?? 0
      continue
    }
    if (seq == null || seq <= seen) continue
    news.seen[r.roomKey] = seq
    news.pending = { title: nw.title || nw.type || 'a widget', by: nw.by || '' }
  }
  if (news.pending) void announce(io)
  if (!lp.open) return
  const r = wallRoom(snap)
  const ws = widgetSeqOf(r)
  if (ws != null && ws > lp.seq) void loadWall(io)
}

export function start(io) {}

// tick: the open wall reads again every 15 s, and the age under it moves; a
// new-widget toast held by the gap is told once the gap has run.
export async function tick(io, nowMs) {
  if (news.pending) await announce(io, nowMs)
  if (news.hintUntil && nowMs >= news.hintUntil) {
    news.hintUntil = 0
    io.invalidate()
  }
  if (!lp.open) return
  io.invalidate()
  const ws = widgetSeqOf(wallRoom(room.snapshot))
  if (due(lp, nowMs, REFRESH_MS) || (ws != null && ws > lp.seq && !lp.inFlight)) void loadWall(io)
}

// announce shows the waiting new-widget toast, unless one was shown within
// the gap; then the next tick tells it.
async function announce(io, nowMs) {
  const now = nowMs ?? (await ioNow(io))
  if (!news.pending) return
  if (news.toastAt != null && now - news.toastAt < NEWS_GAP_MS) return
  const w = news.pending
  news.pending = null
  news.toastAt = now
  news.hintUntil = now + HINT_MS
  news.hintTitle = w.title
  io.toast(newWidgetWords(w), 6000)
  io.invalidate()
}

// newWidgetWords is the toast: "New on the wall: 5xx by target group · by bob".
export function newWidgetWords(w) {
  return 'New on the wall: ' + w.title + (w.by ? ' · by ' + w.by : '')
}

// wallHint is what the band offers for a minute after a new widget: the
// widget's title, or null.
export function wallHint(nowMs) {
  if (!news.hintUntil || nowMs >= news.hintUntil) return null
  return { title: news.hintTitle }
}

// openWall opens the wall from the band's `w` and reads it.
export async function openWall(io) {
  news.hintUntil = 0
  const r = currentRoom()
  const opened = await io.open(PANE, paneTitle(r), { focus: true, closeOnEscape: true })
  if (opened && opened.isPlaced) markOpen(lp)
  await loadWall(io)
}

// paneIo is what this file's reads take, from the hook's own `$`, shaped as
// register.js shapes `io`, so a key, a command and the clock read the same way.
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

async function clockNow($) {
  try {
    return Number(await $.clock.now())
  } catch {
    return lp.goodAt
  }
}

async function ioNow(io) {
  try {
    return Number(await io.now())
  } catch {
    return lp.triedAt
  }
}

// wallRoom is the room the wall reads: the one it was opened on, else the first.
function wallRoom(snap) {
  const rooms = (snap && snap.rooms) || []
  return rooms.find((x) => x.roomKey === wall.roomKey) || rooms[0] || null
}

// loadWall runs `landfall wall` for the wall's room, one read at a time; a
// widget event seen mid-read reads once more after it.
async function loadWall(io) {
  if (!wall.roomKey || !room.snapshot.rooms.some((x) => x.roomKey === wall.roomKey)) {
    const r = currentRoom()
    wall.roomKey = r ? r.roomKey : ''
  }
  const fetch = () => {
    const r = wallRoom(room.snapshot)
    const ws = widgetSeqOf(r)
    lp.seq = ws == null ? -1 : ws
    io.invalidate()
    return io.run(wallArgs(r), { timeoutMs: 30000 })
  }
  await readLive(lp, fetch, () => ioNow(io))
  const n = lp.answer && lp.answer.ok ? (lp.answer.widgets || []).length : 0
  if (wall.selected >= n) wall.selected = 0
  io.invalidate()
}

async function askAbout($, w) {
  const r = currentRoom()
  const where = r ? r.displayId || roomName(r) : 'the war room'
  await $.prompt.fill({ text: 'Tell me about the ' + (w.title || w.type) + ' widget in ' + where + '.' })
}

function selectNext($) {
  const n = lp.answer && lp.answer.ok ? (lp.answer.widgets || []).length : 0
  if (n > 0) wall.selected = (wall.selected + 1) % n
  $.ui.invalidate('ui.render')
}

function selectWidget($, i) {
  wall.selected = i
  $.ui.invalidate('ui.render')
}

async function closeWall($) {
  closed(lp)
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
  if (w.empty) return [k.text(EMPTY, { key: key + '-empty', dimColor: true })]
  switch (w.type) {
    case 'stat': {
      const value = Text({ key: key + '-v', bold: true, color: toneColor(w.tone || 'neutral'), children: [String(w.value ?? 'no value')] })
      const unit = w.unit ? Text({ key: key + '-u', dimColor: true, children: [String(w.unit)] }) : null
      const delta = deltaText(w)
      const out = [k.row([k.row([value, unit], key + '-vu', 0), delta ? k.pill(delta, deltaTone(w), key + '-d') : null], key + '-vr', 2)]
      if (w.baselineLabel) out.push(k.text(String(w.baselineLabel), { key: key + '-b', dimColor: true }))
      out.push(k.spark(w.spark, { key: key + '-s', tone: w.tone && w.tone !== 'neutral' ? w.tone : 'info', width: Math.min(40, cells), px, height: 34, label: (w.title || 'stat') + ' trend' }))
      return out
    }
    case 'geo': {
      const pts = w.points || []
      if (pts.length === 0) return [k.text('No places in this window.', { key: key + '-none', dimColor: true })]
      const out = pts.slice(0, 8).map((p, i) =>
        k.row(
          [
            Text({ key: 'gd', color: toneColor(p.tone || 'neutral'), children: ['●'] }),
            Text({ key: 'gp', children: [clipText(geoName(p), Math.max(10, cells - 16))] }),
            typeof p.value === 'number' ? Text({ key: 'gv', bold: true, children: [withUnit(p.value, p.unit)] }) : null,
          ],
          key + '-g' + i,
          1,
        ),
      )
      if (pts.length > 8) out.push(k.text(pts.length - 8 + ' more places', { key: key + '-gmore', dimColor: true }))
      return out
    }
    case 'codeFinding': {
      const where = [w.repo, w.path].filter(Boolean).join(' · ')
      const out = [k.text(clipText(where || 'code', cells), { key: key + '-cf', bold: true })]
      const lines = (w.lines || []).slice(0, 8)
      const start = typeof w.startLine === 'number' ? w.startLine : 1
      const numW = String(start + Math.max(0, lines.length - 1)).length
      lines.forEach((l, i) => {
        out.push(
          k.row(
            [Text({ key: 'ln', dimColor: true, children: [String(start + i).padStart(numW)] }), Text({ key: 'lc', children: [clipText(String(l), Math.max(10, cells - numW - 2))] })],
            key + '-c' + i,
            1,
          ),
        )
      })
      if ((w.lines || []).length > 8) out.push(k.text((w.lines.length - 8) + ' more lines', { key: key + '-cmore', dimColor: true }))
      if (w.permalink && k.els.Link) out.push(k.els.Link({ key: key + '-link', href: w.permalink, label: 'open on GitHub' }))
      return out
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

export const EMPTY = 'No data in this window yet.'

// deltaText is the stat's change pill, as the web app's StatBody words it:
// the CLI's deltaLabel verbatim, else an arrow by trend and the size.
export function deltaText(w) {
  if (w.deltaLabel) return String(w.deltaLabel)
  if (typeof w.delta !== 'number') return ''
  const trend = w.trend || (w.delta > 0 ? 'up' : w.delta < 0 ? 'down' : 'flat')
  const arrow = trend === 'up' ? '↑ ' : trend === 'down' ? '↓ ' : ''
  return arrow + fmt(Math.abs(w.delta))
}

// deltaTone follows StatBody: a toned stat colors its change; a neutral one
// reads up as bad and down as a warning.
export function deltaTone(w) {
  const tone = w.tone || 'neutral'
  if (tone === 'neutral') {
    const trend = w.trend || (typeof w.delta === 'number' ? (w.delta > 0 ? 'up' : w.delta < 0 ? 'down' : 'flat') : '')
    return trend === 'up' ? 'critical' : trend === 'down' ? 'warning' : 'neutral'
  }
  if (tone === 'good') return 'good'
  if (tone === 'warning' || tone === 'serious') return 'warning'
  return 'critical'
}

// withUnit is a value and its unit: "34%", "120 ms".
function withUnit(v, unit) {
  if (!unit) return fmt(v)
  return fmt(v) + (unit === '%' ? '' : ' ') + unit
}

// geoName is a place as a person reads it: "us-east-1 · N. Virginia".
function geoName(p) {
  return p.label && p.label !== p.place ? p.place + ' · ' + p.label : String(p.place || p.label || '?')
}

// moreWords says the wall holds more widgets than were read.
export function moreWords(a) {
  const shown = (a.widgets || []).length + (a.unavailable || []).length
  if (!a.totalWidgets || a.totalWidgets <= shown) return ''
  return 'Showing ' + shown + ' of ' + a.totalWidgets + ' widgets. Open the war room in the browser for the rest.'
}

function caption(k, w, key) {
  const parts = (w.markers || []).map((m) => m.label + ' at ' + hhmm(m.atMs) + 'Z')
  if (typeof w.threshold === 'number') parts.push('threshold ' + fmt(w.threshold))
  if (parts.length === 0) return null
  return k.text(parts.join(' · '), { key: key + '-cap', dimColor: true })
}

function selectedWidget() {
  const a = lp.answer
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
    for (const u of gone) out.push('  ' + (u.title || u.type || u.id) + ': ' + (u.reason || 'not available'))
  }
  const more = moreWords(a)
  if (more) {
    out.push('')
    out.push(more)
  }
  return out.join('\n')
}

function widgetLines(w) {
  if (w.empty) return [EMPTY]
  switch (w.type) {
    case 'stat': {
      const out = [[String(w.value ?? 'no value') + (w.unit || ''), deltaText(w)].filter(Boolean).join(' ')]
      if (w.baselineLabel) out.push(String(w.baselineLabel))
      return out
    }
    case 'geo':
      return (w.points || []).slice(0, 8).map((p) => geoName(p) + (typeof p.value === 'number' ? ' ' + withUnit(p.value, p.unit) : '') + (p.tone && p.tone !== 'neutral' ? ' (' + p.tone + ')' : ''))
    case 'codeFinding': {
      const start = typeof w.startLine === 'number' ? w.startLine : 1
      return [[w.repo, w.path].filter(Boolean).join(' · '), ...(w.lines || []).slice(0, 8).map((l, i) => String(start + i) + '  ' + l), ...(w.permalink ? [w.permalink] : [])]
    }
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
