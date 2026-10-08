// Load balancer health board: /lb [name] (proposal item 07, FR-07).
//
// A local component, not a widget anybody posted: `landfall lb` runs the
// CloudWatch source's own reads through the room daemon (the room's
// connection and session), and the pane draws each load balancer's target
// groups, every target's health by zone, and 5xx per target group per minute
// (CloudWatch publishes ALB 5xx per target group, never per target: the CLI
// says so with `fiveXxBy: "targetGroup"`): a Raster heat map on the terminal,
// an Svg grid with hover on the desktop. A minute with no requests is a
// no-data cell, never a quiet one. `s` shares the view as a real widget
// through the chart path (`landfall chart`, as key 4 does); `a` drafts a
// question about the worst zone or group. Nothing sends.

import { HOST, addCommand, clip, currentRoom, parseAnswer, room, roomName } from '../core.js'
import { kit } from '../kit.js'
import { byZone, clipText, fiveXxTone, fmt, healthTone, healthyTone, hhmm, lastPct, pctCell, toneColor } from '../views.js'

export const PANE = 'landfall-lb'

const lb = {
  answer: null,
  loading: false,
  sharing: false,
  name: '', // the --lb the person asked for
}

export function install(on) {
  addCommand({ name: 'lb', description: "Show load balancer health from the room's AWS connection", argumentHint: '[name]' })

  on('command.run', { command: 'lb' }, async ($, e) => {
    lb.name = String(e.args || '').trim()
    const r = currentRoom()
    const opened = await $.ui.open({ id: PANE, title: r && r.displayId ? 'Load balancers · ' + r.displayId : 'Load balancers', focus: true, closeOnEscape: true })
    await loadLb($)
    if (!opened || !opened.isPlaced) return { text: lbText(lb.answer) }
    return {}
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const k = kit($.ui.resolve(e), e)
    const a = lb.answer
    const rows = []
    const r = currentRoom()
    if (!a) {
      rows.push(k.header({ key: 'lb-h', title: 'Load balancers', dim: r ? roomName(r) : '' }))
      rows.push(k.text(lb.loading ? 'Reading the room’s AWS connection…' : 'Press r to read the load balancers.', { key: 'lb-empty', dimColor: true }))
    } else if (!a.ok) {
      rows.push(k.header({ key: 'lb-h', title: 'Load balancers', dim: r ? roomName(r) : '' }))
      rows.push(k.text(clipText(a.error || 'The load balancers could not be read.', k.width), { key: 'lb-err' }))
    } else {
      for (const [i, one] of (a.loadBalancers || []).entries()) rows.push(...lbView(k, one, 'lb' + i, a))
      if ((a.loadBalancers || []).length === 0) rows.push(k.text('No load balancers in scope.', { key: 'lb-none', dimColor: true }))
      for (const [i, line] of notes(a).entries()) rows.push(k.text(line, { key: 'lb-note' + i, dimColor: true }))
    }
    const worst = a && a.ok ? worstSpot(a) : null
    rows.push(
      k.row(
        [
          a && a.ok && a.query ? k.button({ key: 'lb-share', label: lb.sharing ? 'sharing…' : 'share as widget', hotkey: 's', primary: true, onPress: () => shareLb($) }) : null,
          worst ? k.button({ key: 'lb-ask', label: 'ask about ' + worst.label, hotkey: 'a', onPress: () => askWorst($, worst) }) : null,
          k.button({ key: 'lb-refresh', label: 'refresh', hotkey: 'r', onPress: () => loadLb($) }),
          k.button({ key: 'lb-close', label: 'close (esc)', dim: true, onPress: () => $.ui.close({ id: PANE }) }),
        ],
        'lb-keys',
      ),
    )
    return k.col(rows, 'lb')
  })
}

export async function band(io, e, k) {
  return null
}

export function onSnapshot(io, snap, prev) {}

export function start(io) {}

async function loadLb($) {
  if (lb.loading) return
  lb.loading = true
  $.ui.invalidate('ui.render')
  try {
    lb.answer = parseAnswer(await $.process.run([room.bin, ...lbArgs(currentRoom(), lb.name)], { timeoutMs: 40000 }))
  } catch (err) {
    lb.answer = { ok: false, error: String(err).slice(0, 200) }
  }
  lb.loading = false
  $.ui.invalidate('ui.render')
}

// shareLb puts the board's read on the room's canvas as a chart: the same
// `landfall chart` path key 4 takes (components/chart.js pinChart), so the
// room daemon makes the read and nobody copies points.
async function shareLb($) {
  const a = lb.answer
  if (!a || !a.ok || !a.query || lb.sharing) return
  lb.sharing = true
  $.ui.invalidate('ui.render')
  const title = shareTitle(a)
  try {
    const run = await $.process.run([room.bin, 'chart', '--host', HOST, '--query', JSON.stringify({ ...a.query, title })], { timeoutMs: 40000 })
    const answer = parseAnswer(run)
    if (answer.ok) await $.ui.toast('Chart added to your dashboard in the room: ' + (answer.title || title), { timeoutMs: 6000 })
    else await $.ui.toast('Chart not added: ' + clip(String(answer.error || 'no answer'), 200), { timeoutMs: 8000 })
  } catch (err) {
    await $.ui.toast('Chart not added: ' + String(err).slice(0, 200), { timeoutMs: 8000 })
  }
  lb.sharing = false
  $.ui.invalidate('ui.render')
}

async function askWorst($, worst) {
  await $.prompt.fill({ text: worst.ask })
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
  if (a.truncated) out.push('Some load balancers or target groups are not shown. Run /lb with a name to see one load balancer alone.')
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

// lbView is one load balancer: "healthy N of M", then each target group with
// its latest 5xx share and its targets by zone, then 5xx per target group per
// minute and (where it draws) the healthy host count.
function lbView(k, one, key, a) {
  const { Text } = k.els
  const rows = []
  const tone = healthyTone(one.healthy, one.total)
  const pills = [{ text: 'healthy ' + (one.healthy ?? 0) + ' of ' + (one.total ?? 0), tone }]
  if (one.state && one.state !== 'active') pills.push({ text: one.state, tone: /fail/.test(one.state) ? 'critical' : 'warning' })
  rows.push(k.header({ key: key + '-h', title: one.name || 'load balancer', pills, dim: [a.region, one.scheme].filter(Boolean).join(' · ') }))
  if ((one.zones || []).length > 0) rows.push(k.text(clipText('Zones: ' + one.zones.join(', '), k.width), { key: key + '-zl', dimColor: true }))
  const groups = one.targetGroups || []
  if (groups.length === 0) rows.push(k.text('No target groups behind it.', { key: key + '-notg', dimColor: true }))
  groups.forEach((tg, gi) => {
    const gk = key + '-tg' + gi
    const pct = lastPct(tg)
    rows.push(
      k.row(
        [Text({ key: 'n', bold: true, children: [clipText(groupWords(tg), Math.max(16, k.width - 14))] }), pct == null ? null : k.pill('5xx ' + fmt(pct) + '%', fiveXxTone(pct) || 'good', 'p')],
        gk + '-n',
        1,
      ),
    )
    if (tg.healthUnavailable) rows.push(k.text(tg.healthUnavailable, { key: gk + '-hu', dimColor: true }))
    const zones = byZone(tg.targets)
    if (zones.length === 0 && !tg.healthUnavailable) rows.push(k.text('No registered targets.', { key: gk + '-none', dimColor: true }))
    if (k.rich) {
      rows.push(
        k.els.Box({
          key: gk + '-zones',
          flexDirection: 'row',
          flexWrap: 'wrap',
          columnGap: 2,
          children: zones.map((z, zi) =>
            k.els.Box({
              key: 'z' + zi,
              flexDirection: 'column',
              borderStyle: 'round',
              paddingX: 1,
              children: [
                Text({ key: 'zn', bold: true, children: [z.zone] }),
                ...z.targets.flatMap((t, ti) => [targetText(k, t, tg, 't' + ti), t.detail && t.state !== 'healthy' ? Text({ key: 'd' + ti, dimColor: true, children: [clipText(t.detail, 60)] }) : null]).filter(Boolean),
              ],
            }),
          ),
        }),
      )
    } else {
      zones.forEach((z, zi) => {
        rows.push(k.row([Text({ key: 'zn', dimColor: true, children: [z.zone] }), ...z.targets.map((t, ti) => targetText(k, t, tg, 't' + ti))], gk + '-z' + zi, 2))
      })
      for (const [ti, t] of (tg.targets || []).entries()) {
        if (t.state === 'healthy' || !(t.detail || t.reason)) continue
        rows.push(k.text(clipText('  ' + targetName(t, tg) + ': ' + (t.detail || t.reason), k.width), { key: gk + '-d' + ti, dimColor: true }))
      }
    }
  })
  if (!a.metricsUnavailable) {
    const heat = heatView(k, one, key, a)
    if (heat) rows.push(...heat)
    const hhc = (one.healthyHostCount || []).filter((v) => typeof v === 'number')
    const sparkTone = tone === 'good' ? 'good' : 'warning'
    if (k.rich && hhc.length >= 2) {
      rows.push(k.text('Healthy host count · AWS/ApplicationELB · 1 min', { key: key + '-hhc-l', dimColor: true }))
      rows.push(k.spark(hhc, { key: key + '-hhc', tone: sparkTone, px: 320, height: 46, label: 'healthy host count', hover: true }))
    } else if (hhc.length >= 2) {
      rows.push(k.row([k.text('healthy hosts', { key: 'l', dimColor: true }), k.spark(hhc, { key: 's', tone: sparkTone, width: 30, label: 'healthy host count' })], key + '-hhc', 1))
    }
  }
  return rows
}

function targetText(k, t, tg, key) {
  const why = [t.state || 'unknown', t.reason || ''].filter(Boolean).join(' · ')
  return k.els.Text({ key, color: toneColor(healthTone(t.state)), children: ['■ ' + targetName(t, tg) + (k.rich ? ' · ' + why : '')] })
}

// heatView is 5xx per target group per minute: one row per target group,
// columns minutes, cells toned by share, a minute with no requests drawn as
// no data; the latest share at the end of each row and a legend under it.
function heatView(k, one, key, a) {
  const groups = (one.targetGroups || []).filter((tg) => (tg.fiveXxPct || []).length > 0)
  if (groups.length === 0) return null
  const minutes = Math.max(...groups.map((tg) => tg.fiveXxPct.length))
  const { Text } = k.els
  const names = groups.map((tg) => clipText(tg.name || 'target group', 24))
  const labelW = Math.max(...names.map((n) => n.length))
  const cols = k.rich ? minutes : Math.max(4, Math.min(minutes, k.width - labelW - 12))
  // On the terminal, keep the newest minutes that fit.
  const from = minutes - cols
  const at = (tg, x) => {
    const v = tg.fiveXxPct
    return v[x - (minutes - v.length)]
  }
  const grid = groups.map((tg) => {
    const row = []
    for (let x = from; x < minutes; x++) row.push(pctCell(at(tg, x)))
    return row
  })
  const span = (a.minutes || minutes) + ' minutes'
  const out = []
  if (k.rich) {
    const start = one.minuteStartMs
    const titleFor = (y, x) => {
      const tg = groups[y]
      const m = from + x
      const v = at(tg, m)
      const off = minutes - tg.fiveXxPct.length
      const req = (tg.requests || [])[m - off]
      const err = (tg.fiveXx || [])[m - off]
      const when = typeof start === 'number' ? hhmm(start + m * 60000) + 'Z · ' : ''
      if (typeof v !== 'number') return tg.name + ' · ' + when + 'no requests'
      return tg.name + ' · ' + when + '5xx ' + fmt(v) + '%' + (typeof req === 'number' && typeof err === 'number' ? ' (' + fmt(err) + ' of ' + fmt(req) + ' requests)' : '')
    }
    out.push(k.text('5xx per target group per minute', { key: key + '-hl', dimColor: true }))
    out.push(k.heat(grid, { key: key + '-heat', cell: 9, label: '5xx per target group per minute, ' + groups.length + (groups.length === 1 ? ' target group' : ' target groups') + ' over ' + span, titleFor }))
  } else {
    const start = typeof one.minuteStartMs === 'number' ? hhmm(one.minuteStartMs + from * 60000) : ''
    out.push(k.text(' '.repeat(labelW + 1) + clipText(start + ' '.repeat(Math.max(1, cols - start.length - 3)) + 'now', cols), { key: key + '-axis', dimColor: true }))
    const labels = names.map((n, i) => Text({ key: 'l' + i, dimColor: true, children: [n.padEnd(labelW)] }))
    const latest = groups.map((tg, i) => {
      const pct = lastPct(tg)
      if (pct == null) return Text({ key: 'p' + i, dimColor: true, children: ['no requests'] })
      return Text({ key: 'p' + i, color: toneColor(fiveXxTone(pct) || 'good'), children: [fmt(pct) + '%'] })
    })
    out.push(k.row([k.col(labels, 'labels'), k.heat(grid, { key: 'heat', label: '5xx per target group per minute' }), k.col(latest, 'latest')], key + '-heat', 1))
  }
  out.push(
    k.row(
      [
        Text({ key: 'q', dimColor: true, children: ['▪ under 1%'] }),
        Text({ key: 'g', color: toneColor('good'), children: ['█ 1-5%'] }),
        Text({ key: 'w', color: toneColor('warning'), children: ['█ 5-20%'] }),
        Text({ key: 'c', color: toneColor('critical'), children: ['█ over 20%'] }),
        Text({ key: 'n', dimColor: true, children: [(k.rich ? '□' : '·') + ' no requests'] }),
        Text({ key: 'x', dimColor: true, children: ['5xx per target group, last ' + span] }),
      ],
      key + '-legend',
      2,
    ),
  )
  return out
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

// tick runs every TICK_MS while the session lives: a component with an open
// pane refreshes it here on its own cadence (nothing to do by default).
export function tick(io, nowMs) {}
