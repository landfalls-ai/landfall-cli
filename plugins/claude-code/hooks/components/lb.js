// Load balancer health board: the console's Load balancers tab (spec 4.5; proposal item 07, FR-07).
//
// A local component, not a widget anybody posted: `landfall lb` runs the CloudWatch source's own
// reads through the room daemon (the room's connection and session), and the tab draws each load
// balancer's target groups, every target's health by zone, and 5xx per target group per minute
// (CloudWatch publishes ALB 5xx per target group, never per target: the CLI says so with
// `fiveXxBy: "targetGroup"`): a Raster heat map on the terminal, an Svg grid with hover on the
// desktop. A minute with no requests is a no-data cell, never a quiet one. `s` shares the view as
// a real widget through the chart path (`landfall chart`, as key 4 does); `a` drafts a question
// about the worst zone or group. Nothing sends.
//
// It is warm from the moment the console opens and reads again every 60 s, CloudWatch's own
// minute. A failed read keeps the last good board and says it is stale; the footer says how old
// what it shows is. `/landfall lb <name>` shows one load balancer.

import { HOST, consoleState, currentRoom, roomArgs, roomName } from '../core.js'
import { TONE } from '../kit.js'
import { closed, drawn, due, LB_MS, livePane, readLive, opened as markOpen } from '../live.js'
import { byZone, clipText, fiveXxTone, fmt, healthTone, healthyTone, hhmm, lastPct, pctCell, toneColor } from '../views.js'
import { centerRow, footerOf, keyButton, nowOf, resetLive, roomKeyOf, say } from './tabparts.js'

// The one label column of the whole board (spec 4.5): zone names, target-group names, the
// healthy-hosts label.
const LABEL_W = 17

// The board's live read (live.js): the last good `landfall lb` answer.
const lp = livePane()
const lb = {
  roomKey: '', // the room this board is of
  sharing: false,
  name: '', // the --lb the person asked for
  argsApplied: '', // the name a `/landfall lb <name>` already chose
}

// forRoom drops what was read of another room: a different incident starts the board over.
function forRoom(r) {
  const key = roomKeyOf(r)
  if (key === lb.roomKey) return
  Object.assign(lb, { roomKey: key, sharing: false, name: '', argsApplied: '' })
  resetLive(lp)
}

// No hooks of its own: the console owns the pane and the `/landfall` command.
export function install(on) {}

export async function band(io, e, k) {
  return null
}

export function onSnapshot(io, snap, prev) {}

export function start(io) {}

// tick: an open board reads again every 60 s; closing the console stops the reads and forgets
// the name the person asked for.
export async function tick(io, nowMs) {
  if (lp.open && !consoleState.open) {
    closed(lp)
    lb.name = ''
    lb.argsApplied = ''
  }
  const moved = roomKeyOf(currentRoom()) !== lb.roomKey
  forRoom(currentRoom())
  if (!lp.open) return
  if (moved || due(lp, nowMs, LB_MS)) void loadLb(io)
}

// warm starts the board's reads when the console opens (and reads again when asked).
export async function warm(io) {
  forRoom(currentRoom())
  markOpen(lp)
  await loadLb(io)
}

export async function refresh(io) {
  await warm(io)
}

// Load balancers carry no badge.
export function badge() {
  return null
}

// The live read's state (live.js), so Home can say how old its load balancer block is.
export function readState() {
  return lp
}

// The last good answer, for Home's load balancer block.
export function answerOf() {
  return lp.answer && lp.answer.ok ? lp.answer : null
}

// tab is the Load balancers tab's body (spec 4.5): each load balancer as lbView draws it, then
// the answer's notes, the footer and the keys row. `args` is `/landfall lb <name>`.
export function tab(k, io, nowMs, args) {
  drawn(lp)
  const r = currentRoom()
  forRoom(r)
  applyArgs(io, args)
  const a = lp.answer
  const rows = []
  if (lb.name) {
    rows.push(
      k.row(
        [k.text('Showing ' + lb.name + ' only.', { key: 'lb-only', dimColor: true }), k.els.Button({ key: 'lb-all', label: 'show all load balancers', plain: true, onPress: () => showAll(io) })],
        'lb-filter',
        1,
      ),
    )
  }
  if (!a) {
    rows.push(k.text('Reading the room’s AWS connection…', { key: 'lb-empty', dimColor: true }))
  } else if (!a.ok) {
    rows.push(k.text(clipText(a.error || 'The load balancers could not be read.', k.width), { key: 'lb-err' }))
  } else {
    for (const [i, one] of (a.loadBalancers || []).entries()) rows.push(...lbView(k, one, 'lb' + i, a))
    if ((a.loadBalancers || []).length === 0) rows.push(k.text('No load balancers in scope.', { key: 'lb-none', dimColor: true }))
    for (const [i, line] of notes(a).entries()) rows.push(k.text(line, { key: 'lb-note' + i, dimColor: true }))
  }
  const foot = footerOf(k, lp, nowMs, r, 'lb-live')
  if (foot) rows.push(foot)
  const keyRow = keys(k, io, nowMs, args)
  if (keyRow.length > 0) rows.push(k.row(keyRow, 'lb-keys', 2))
  return rows
}

// keys are the tab's letters: `s: share as widget` (primary while a query exists), `a: ask about
// <worst>`. Console.js adds `r: refresh` and `close (esc)`.
export function keys(k, io, nowMs, args) {
  const a = lp.answer
  const worst = a && a.ok ? worstSpot(a) : null
  const out = []
  if (a && a.ok && a.query) {
    out.push(
      keyButton(k, {
        key: 'lb-share',
        label: lb.sharing ? 'sharing…' : k.terminal ? 'share as widget' : 'Share as widget',
        hotkey: 's',
        primary: true,
        onPress: () => shareLb(io),
      }),
    )
  }
  if (worst) out.push(keyButton(k, { key: 'lb-ask', label: (k.terminal ? 'ask about ' : 'Ask about ') + worst.label, hotkey: 'a', onPress: () => askWorst(io, worst) }))
  return out
}

// applyArgs takes `/landfall lb <name>` once: the name is chosen and read, then not chosen again
// on every draw.
function applyArgs(io, args) {
  if (args == null || args === '') {
    lb.argsApplied = ''
    return
  }
  const asked = String(typeof args === 'object' ? args.name || '' : args).trim()
  if (consoleState.args === args) consoleState.args = null
  if (!asked || asked === lb.argsApplied) return
  lb.argsApplied = asked
  if (asked !== lb.name) chooseName(asked, io)
}

function chooseName(name, io) {
  lb.name = name
  lp.answer = null
  lp.last = null
  void loadLb(io)
}

function showAll(io) {
  lb.argsApplied = ''
  chooseName('', io)
}

// loadLb runs `landfall lb`, one read at a time.
async function loadLb(io) {
  const fetch = () => {
    io.invalidate()
    return io.run(lbArgs(currentRoom(), lb.name), { timeoutMs: 40000 })
  }
  await readLive(lp, fetch, () => nowOf(io, lp.triedAt))
  io.invalidate()
}

// shareLb puts the board's read on the room's canvas as a chart: the same `landfall chart` path
// key 4 takes (components/chart.js pinChart), so the room daemon makes the read and nobody copies
// points.
async function shareLb(io) {
  const a = lp.answer
  if (!a || !a.ok || !a.query || lb.sharing) return
  lb.sharing = true
  io.invalidate()
  const title = shareTitle(a)
  try {
    const answer = await io.run(['chart', '--host', HOST, ...roomArgs(currentRoom()), '--query', JSON.stringify({ ...a.query, title })], { timeoutMs: 40000 })
    if (answer.ok) say(io, 'Chart added to your dashboard in the room: ' + (answer.title || title), 6000)
    else say(io, 'Chart not added: ' + String(answer.error || 'no answer'), 8000)
  } catch (err) {
    say(io, 'Chart not added: ' + String(err), 8000)
  }
  lb.sharing = false
  io.invalidate()
}

async function askWorst(io, worst) {
  await io.fill(worst.ask)
}

export function lbArgs(r, name) {
  const args = ['lb', '--host', HOST]
  if (r && r.roomKey) args.push('--room', r.roomKey)
  if (name) args.push('--lb', name)
  return args
}

// shareTitle is the widget's title: the CLI's own when it gives one.
export function shareTitle(a) {
  if (a.query && a.query.title) return a.query.title
  const names = (a.loadBalancers || []).map((x) => x.name).filter(Boolean)
  return '5xx per target group' + (names.length === 1 ? ' · ' + names[0] : '')
}

// worstSpot is what `a` asks about: the zone with the most unhealthy targets
// (then its group's latest 5xx share), else a target group returning 5% or
// more 5xx; null when every target is healthy and every group quiet.
export function worstSpot(a) {
  let best = null
  const consider = (score, label, ask) => {
    if (!best || score > best.score) best = { score, label, ask }
  }
  for (const one of a.loadBalancers || []) {
    for (const tg of one.targetGroups || []) {
      const pct = lastPct(tg) ?? 0
      for (const z of byZone(tg.targets)) {
        const bad = z.targets.filter((t) => healthTone(t.state) !== 'good').length
        if (bad > 0) consider(bad * 1000 + pct, z.zone, 'Why are targets in ' + z.zone + ' behind ' + one.name + ' unhealthy or returning 5xx?')
      }
      if (pct >= 5) consider(pct, tg.name, 'Why is target group ' + tg.name + ' behind ' + one.name + ' returning 5xx?')
    }
  }
  return best
}

// notes are the answer's own caveats, as sentences under the board.
export function notes(a) {
  const out = []
  if (a.metricsUnavailable) out.push('5xx and healthy host counts are not shown. ' + a.metricsUnavailable)
  if (a.truncated) out.push('Some load balancers or target groups are not shown. Run /landfall lb <name> to see one load balancer alone.')
  return out
}

// groupWords is a target group's heading: "Target group web-edge-tg · HTTP 8080 · health check /healthz".
function groupWords(tg) {
  return ['Target group ' + (tg.name || ''), [tg.protocol, tg.port].filter(Boolean).join(' '), tg.healthCheck ? 'health check ' + tg.healthCheck : ''].filter(Boolean).join(' · ')
}

// targetName is a target's id, with its port when it differs from its group's.
function targetName(t, tg) {
  return (t.id || '?') + (t.port && t.port !== tg.port ? ':' + t.port : '')
}

// groupHead is a target group's short heading: "web-edge-tg · HTTP 8080 · /healthz".
function groupHead(tg) {
  return [tg.name || '', [tg.protocol, tg.port].filter(Boolean).join(' '), tg.healthCheck || ''].filter(Boolean).join(' · ')
}

// labelCell is one cell of the board's label column.
function labelCell(k, text, key, dim) {
  return k.els.Box({ key, width: LABEL_W, flexShrink: 0, children: [k.els.Text({ key: key + '-t', dimColor: dim !== false, children: [clipText(text, LABEL_W - 1)] })] })
}

// lbView is one load balancer: "healthy N of M", then each target group with its latest 5xx
// share and its targets by zone, then 5xx per target group per minute and the healthy host
// count.
function lbView(k, one, key, a) {
  const { Text } = k.els
  const rows = []
  const tone = healthyTone(one.healthy, one.total)
  const head = [
    Text({ key: key + '-hn', bold: true, children: [one.name || 'load balancer'] }),
    k.pill('healthy ' + (one.healthy ?? 0) + ' of ' + (one.total ?? 0), tone, key + '-hp'),
  ]
  if (one.state && one.state !== 'active') head.push(k.pill(one.state, /fail/.test(one.state) ? 'critical' : 'warning', key + '-hs'))
  const where = [a.region, one.scheme].filter(Boolean).join(' · ')
  if (where) head.push(Text({ key: key + '-hw', dimColor: true, children: [where] }))
  rows.push(centerRow(k, head, key + '-h', 1))
  if ((one.zones || []).length > 0) rows.push(k.text(clipText('Zones: ' + one.zones.join(', '), k.width), { key: key + '-zl', dimColor: true }))
  const groups = one.targetGroups || []
  if (groups.length === 0) rows.push(k.text('No target groups behind it.', { key: key + '-notg', dimColor: true }))
  groups.forEach((tg, gi) => {
    const gk = key + '-tg' + gi
    const pct = lastPct(tg)
    rows.push(
      centerRow(k, 
        [Text({ key: 'n', bold: true, children: [clipText(groupHead(tg), Math.max(16, k.width - 14))] }), pct == null ? null : k.pill('5xx ' + fmt(pct) + '%', fiveXxTone(pct) || 'good', 'p')],
        gk + '-n',
        1,
      ),
    )
    if (tg.healthUnavailable) rows.push(k.text(tg.healthUnavailable, { key: gk + '-hu', dimColor: true }))
    const zones = byZone(tg.targets)
    if (zones.length === 0 && !tg.healthUnavailable) rows.push(k.text('No registered targets.', { key: gk + '-none', dimColor: true }))
    zones.forEach((z, zi) => {
      const items = z.targets.map((t, ti) => centerRow(k, [Text({ key: 's', color: toneColor(healthTone(t.state)), children: ['■'] }), Text({ key: 'i', children: [targetName(t, tg)] })], gk + '-z' + zi + 't' + ti, 1))
      rows.push(centerRow(k, [labelCell(k, z.zone, gk + '-z' + zi + 'l'), ...items], gk + '-z' + zi, 2))
    })
    for (const [ti, t] of (tg.targets || []).entries()) {
      if (t.state === 'healthy' || !(t.detail || t.reason)) continue
      rows.push(k.text(clipText('  ' + targetName(t, tg) + ': ' + (t.detail || t.reason), k.width), { key: gk + '-d' + ti, dimColor: true }))
    }
  })
  if (!a.metricsUnavailable) {
    const heat = heatView(k, one, key, a)
    if (heat) rows.push(...heat)
  }
  return rows
}

// heatView is 5xx per target group per minute: one row per target group, the name in the label
// column, the minutes as cells toned by share (a minute with no requests drawn as no data), the
// latest share as a label two cells after the last cell, then the legend, the caption and the
// healthy-host sparkline exactly as wide as a heat row.
function heatView(k, one, key, a) {
  const groups = (one.targetGroups || []).filter((tg) => (tg.fiveXxPct || []).length > 0)
  if (groups.length === 0) return null
  const minutes = Math.max(...groups.map((tg) => tg.fiveXxPct.length))
  const { Text, Box } = k.els
  // The terminal keeps the newest minutes that fit beside the label column and the share label.
  const cols = k.rich ? minutes : Math.max(4, Math.min(minutes, k.width - LABEL_W - 12))
  const from = minutes - cols
  const at = (tg, x) => {
    const v = tg.fiveXxPct
    return v[x - (minutes - v.length)]
  }
  const gridOf = (tg) => {
    const row = []
    for (let x = from; x < minutes; x++) row.push(pctCell(at(tg, x)))
    return row
  }
  const span = (a.minutes || minutes) + ' minutes'
  const latest = (tg, i) => {
    const pct = lastPct(tg)
    if (pct == null) return Text({ key: 'p' + i, dimColor: true, children: ['no requests'] })
    return k.pill(fmt(pct) + '%', fiveXxTone(pct) || 'good', 'p' + i)
  }
  // Desktop cells scale to the card, up to 18 px; the row is as wide as its cells.
  const cell = k.rich ? Math.max(5, Math.min(18, Math.floor((k.width * 8 - 260) / minutes) - 2)) : 0
  const out = []
  const start = typeof one.minuteStartMs === 'number' ? hhmm(one.minuteStartMs + from * 60000) : ''
  if (k.rich) {
    out.push(Box({ key: key + '-axis', flexDirection: 'row', paddingLeft: LABEL_W, width: LABEL_W + minutes * (cell + 2), justifyContent: 'space-between', children: [Text({ key: 'a', dimColor: true, children: [start] }), Text({ key: 'b', dimColor: true, children: ['now'] })] }))
    groups.forEach((tg, gi) => {
      const titleFor = (y, x) => {
        const m = from + x
        const v = at(tg, m)
        const off = minutes - tg.fiveXxPct.length
        const req = (tg.requests || [])[m - off]
        const err = (tg.fiveXx || [])[m - off]
        const when = typeof one.minuteStartMs === 'number' ? hhmm(one.minuteStartMs + m * 60000) + 'Z · ' : ''
        if (typeof v !== 'number') return tg.name + ' · ' + when + 'no requests'
        return tg.name + ' · ' + when + '5xx ' + fmt(v) + '%' + (typeof req === 'number' && typeof err === 'number' ? ' (' + fmt(err) + ' of ' + fmt(req) + ' requests)' : '')
      }
      out.push(centerRow(k, [labelCell(k, tg.name || 'target group', key + '-hl' + gi), k.heat([gridOf(tg)], { key: key + '-heat' + gi, cell, label: '5xx per minute, ' + (tg.name || 'target group') + ', ' + span, titleFor }), latest(tg, gi)], key + '-hr' + gi, 2))
    })
  } else {
    out.push(k.text(' '.repeat(LABEL_W) + clipText(start + ' '.repeat(Math.max(1, cols - start.length - 3)) + 'now', cols), { key: key + '-axis', dimColor: true }))
    const labels = groups.map((tg, i) => labelCell(k, tg.name || 'target group', 'l' + i))
    out.push(centerRow(k, [k.col(labels, 'labels'), k.heat(groups.map(gridOf), { key: 'heat', label: '5xx per target group per minute' }), k.col(groups.map(latest), 'latest')], key + '-heat', 2))
  }
  const swatch = (ch, color, words, i) => centerRow(k, [Text({ key: 'sw' + i, color, children: [ch] }), Text({ key: 'wd' + i, dimColor: true, children: [words] })], key + '-lg' + i, 1)
  out.push(
    centerRow(k, 
      [
        swatch('▪', TONE.neutral, 'under 1%', 0),
        swatch('█', toneColor('good'), '1-5%', 1),
        swatch('█', toneColor('warning'), '5-20%', 2),
        swatch('█', toneColor('critical'), 'over 20%', 3),
        swatch(k.rich ? '□' : '·', TONE.neutral, 'no requests', 4),
      ],
      key + '-legend',
      2,
    ),
  )
  out.push(k.text('5xx per target group, last ' + span, { key: key + '-cap', dimColor: true }))
  const hhc = fillGaps(one.healthyHostCount || [])
  if (hhc.length >= 2) {
    const sparkTone = healthyTone(one.healthy, one.total) === 'good' ? 'good' : 'warning'
    const total = k.rich ? minutes * (cell + 2) : cols
    const sp = k.rich ? k.spark(hhc, { key: key + '-hhc', tone: sparkTone, px: total, height: 44, label: 'healthy host count', hover: true }) : k.spark(hhc, { key: key + '-hhc', tone: sparkTone, width: cols, label: 'healthy host count' })
    out.push(centerRow(k, [labelCell(k, 'healthy hosts', key + '-hhl'), sp, Text({ key: key + '-hhn', dimColor: true, children: [(one.healthy ?? 0) + ' of ' + (one.total ?? 0)] })], key + '-hhc-r', 2))
  }
  return out
}

// fillGaps is the healthy host count with each minute CloudWatch had no sample for (null) carried
// from the minute before (the first from the one after), so the sparkline has one point per heat
// cell and is exactly as wide as a heat row. Empty when there is no sample at all.
function fillGaps(values) {
  const first = values.find((v) => typeof v === 'number')
  if (first === undefined) return []
  let last = first
  return values.map((v) => (typeof v === 'number' ? (last = v) : last))
}

// lbText is /lb's answer where no pane can be drawn.
export function lbText(a) {
  if (!a) return 'The load balancers could not be read.'
  if (!a.ok) return a.error || 'The load balancers could not be read.'
  const out = []
  for (const one of a.loadBalancers || []) {
    out.push((one.name || 'load balancer') + ' · healthy ' + (one.healthy ?? 0) + ' of ' + (one.total ?? 0) + (a.region ? ' · ' + a.region : ''))
    for (const tg of one.targetGroups || []) {
      const pct = a.metricsUnavailable ? null : lastPct(tg)
      out.push('  ' + groupWords(tg) + (pct == null ? '' : ' · 5xx ' + fmt(pct) + '% now'))
      if (tg.healthUnavailable) out.push('    ' + tg.healthUnavailable)
      for (const z of byZone(tg.targets)) {
        out.push('    ' + z.zone + ': ' + z.targets.map((t) => targetName(t, tg) + ' ' + (t.state || 'unknown') + (t.reason ? ' (' + t.reason + ')' : '')).join(', '))
      }
    }
  }
  if (out.length === 0) out.push('No load balancers in scope.')
  out.push(...notes(a))
  return out.join('\n')
}

// text is the Load balancers tab's answer where no pane can be placed (`claude -p`): it reads the
// board (one load balancer when `args` names one) and says it.
export async function text(io, args) {
  forRoom(currentRoom())
  const name = String(args || '').trim()
  if (name !== lb.name) {
    lb.name = name
    lp.answer = null
  }
  await loadLb(io)
  return lbText(lp.last)
}

