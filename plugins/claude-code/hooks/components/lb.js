// Load balancer health board: /lb [name] (proposal item 07, FR-07).
//
// A local component, not a widget anybody posted: `landfall lb` runs the
// CloudWatch source's own reads through the room daemon (the room's
// connection and session), and the pane draws each load balancer's target
// groups by zone, every target's health, and 5xx per target per minute: a
// Raster heat map on the terminal, an Svg grid with hover on the desktop. `s`
// shares the view as a real widget through the chart path (`landfall chart`,
// as key 4 does); `a` drafts a question about the worst zone. Nothing sends.

import { HOST, addCommand, clip, currentRoom, parseAnswer, room, roomName } from '../core.js'
import { kit } from '../kit.js'
import { byZone, clipText, fiveXxTone, fmt, healthTone, healthyTone, hhmm, lastPct, shortZone, toneColor } from '../views.js'

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
      for (const [i, one] of (a.loadBalancers || []).entries()) rows.push(...lbView(k, one, 'lb' + i, a.region))
      if ((a.loadBalancers || []).length === 0) rows.push(k.text('No load balancers in scope.', { key: 'lb-none', dimColor: true }))
    }
    const worst = a && a.ok ? worstZone(a) : null
    rows.push(
      k.row(
        [
          a && a.ok && a.query ? k.button({ key: 'lb-share', label: lb.sharing ? 'sharing…' : 'share as widget', hotkey: 's', primary: true, onPress: () => shareLb($) }) : null,
          worst ? k.button({ key: 'lb-ask', label: 'ask about ' + worst.zone, hotkey: 'a', onPress: () => askZone($, worst) }) : null,
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

async function askZone($, worst) {
  await $.prompt.fill({ text: 'Why are targets in ' + worst.zone + ' behind ' + worst.lb + ' unhealthy or returning 5xx?' })
}

export function lbArgs(r, name) {
  const args = ['lb', '--host', HOST]
  if (r && r.roomKey) args.push('--room', r.roomKey)
  if (name) args.push('--lb', name)
  return args
}

export function shareTitle(a) {
  const names = (a.loadBalancers || []).map((x) => x.name).filter(Boolean)
  return '5xx per target' + (names.length === 1 ? ' · ' + names[0] : '')
}

// worstZone is the zone with the most unhealthy targets, then the highest
// latest 5xx share; null when every target is healthy and quiet.
export function worstZone(a) {
  let best = null
  for (const one of a.loadBalancers || []) {
    for (const tg of one.targetGroups || []) {
      for (const z of byZone(tg.targets)) {
        const bad = z.targets.filter((t) => healthTone(t.state) !== 'good').length
        const pct = Math.max(0, ...z.targets.map((t) => lastPct(t) ?? 0))
        if (bad === 0 && pct < 5) continue
        const score = bad * 1000 + pct
        if (!best || score > best.score) best = { zone: z.zone, lb: one.name, score }
      }
    }
  }
  return best
}

// lbView is one load balancer: "healthy N of M", then each target group with
// its targets by zone, the heat map, and (where it draws) the healthy host count.
function lbView(k, one, key, region) {
  const { Text } = k.els
  const rows = []
  const tone = healthyTone(one.healthy, one.total)
  rows.push(k.header({ key: key + '-h', title: one.name || 'load balancer', pills: [{ text: 'healthy ' + (one.healthy ?? 0) + ' of ' + (one.total ?? 0), tone }], dim: region || '' }))
  ;(one.targetGroups || []).forEach((tg, gi) => {
    const gk = key + '-tg' + gi
    const what = ['Target group ' + (tg.name || ''), [tg.protocol, tg.port].filter(Boolean).join(' '), tg.healthCheck ? 'health check ' + tg.healthCheck : ''].filter(Boolean).join(' · ')
    rows.push(k.text(clipText(what, k.width), { key: gk + '-n', bold: true }))
    const zones = byZone(tg.targets)
    if (zones.length === 0) rows.push(k.text('No registered targets.', { key: gk + '-none', dimColor: true }))
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
              children: [Text({ key: 'zn', bold: true, children: [z.zone] }), ...z.targets.map((t, ti) => targetText(k, t, 't' + ti))],
            }),
          ),
        }),
      )
    } else {
      zones.forEach((z, zi) => {
        rows.push(
          k.row(
            [Text({ key: 'zn', dimColor: true, children: [z.zone] }), ...z.targets.map((t, ti) => targetText(k, t, 't' + ti))],
            gk + '-z' + zi,
            2,
          ),
        )
      })
    }
    const heat = heatView(k, tg, one, gk)
    if (heat) rows.push(...heat)
  })
  if (k.rich && (one.healthyHostCount || []).length >= 2) {
    rows.push(k.text('Healthy host count · AWS/ApplicationELB · 1 min', { key: key + '-hhc-l', dimColor: true }))
    rows.push(k.spark(one.healthyHostCount, { key: key + '-hhc', tone: tone === 'good' ? 'good' : 'warning', px: 320, height: 46, label: 'healthy host count', hover: true }))
  } else if ((one.healthyHostCount || []).length >= 2) {
    rows.push(
      k.row(
        [k.text('healthy hosts', { key: 'l', dimColor: true }), k.spark(one.healthyHostCount, { key: 's', tone: tone === 'good' ? 'good' : 'warning', width: 30, label: 'healthy host count' })],
        key + '-hhc',
        1,
      ),
    )
  }
  return rows
}

function targetText(k, t, key) {
  const pct = lastPct(t)
  const why = [t.state || 'unknown', t.reason || '', pct == null ? '' : '5xx ' + fmt(pct) + '%'].filter(Boolean).join(' · ')
  return k.els.Text({ key, color: toneColor(healthTone(t.state)), children: ['■ ' + (t.id || '?') + (k.rich ? ' · ' + why : '')] })
}

// heatView is 5xx per target per minute: rows are targets in zone order,
// columns minutes, cells toned by share; a legend under it.
function heatView(k, tg, one, key) {
  const targets = byZone(tg.targets).flatMap((z) => z.targets)
  if (targets.length === 0) return null
  const minutes = Math.max(0, ...targets.map((t) => (t.fiveXxPct || []).length))
  if (minutes === 0) return null
  const { Text } = k.els
  const labelW = Math.max(...targets.map((t) => (shortZone(t.zone) + ' ' + t.id).length))
  const cols = k.rich ? minutes : Math.max(4, Math.min(minutes, k.width - labelW - 12))
  // On the terminal, keep the newest minutes that fit.
  const from = minutes - cols
  const grid = targets.map((t) => {
    const v = t.fiveXxPct || []
    const row = []
    for (let x = from; x < minutes; x++) row.push(fiveXxTone(v[x - (minutes - v.length)]))
    return row
  })
  const out = []
  if (k.rich) {
    const start = one.minuteStartMs
    const titleFor = (y, x) => {
      const t = targets[y]
      const v = (t.fiveXxPct || [])[x - (minutes - (t.fiveXxPct || []).length)]
      const at = typeof start === 'number' ? hhmm(start + (from + x) * 60000) + 'Z · ' : ''
      return t.id + ' ' + (t.zone || '') + ' · ' + at + '5xx ' + (typeof v === 'number' ? fmt(v) + '%' : 'no data')
    }
    out.push(k.text('5xx per target per minute', { key: key + '-hl', dimColor: true }))
    out.push(k.heat(grid, { key: key + '-heat', cell: 9, label: '5xx per target per minute, ' + targets.length + ' targets over ' + minutes + ' minutes', titleFor }))
  } else {
    const start = typeof one.minuteStartMs === 'number' ? hhmm(one.minuteStartMs + from * 60000) : ''
    out.push(k.text(' '.repeat(labelW + 1) + clipText(start + ' '.repeat(Math.max(1, cols - start.length - 3)) + 'now', cols), { key: key + '-axis', dimColor: true }))
    const labels = targets.map((t, i) => Text({ key: 'l' + i, dimColor: true, children: [(shortZone(t.zone) + ' ' + t.id).padEnd(labelW)] }))
    const states = targets.map((t, i) => Text({ key: 's' + i, color: toneColor(healthTone(t.state)), children: [t.state || 'unknown'] }))
    out.push(
      k.row(
        [k.col(labels, 'labels'), k.heat(grid, { key: 'heat', label: '5xx per target per minute' }), k.col(states, 'states')],
        key + '-heat',
        1,
      ),
    )
  }
  out.push(
    k.row(
      [
        Text({ key: 'q', dimColor: true, children: ['▪ under 1%'] }),
        Text({ key: 'g', color: toneColor('good'), children: ['█ 1-5%'] }),
        Text({ key: 'w', color: toneColor('warning'), children: ['█ 5-20%'] }),
        Text({ key: 'c', color: toneColor('critical'), children: ['█ over 20%'] }),
        Text({ key: 'x', dimColor: true, children: ['5xx per target'] }),
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
      out.push('  Target group ' + (tg.name || '') + (tg.port ? ' · ' + [tg.protocol, tg.port].filter(Boolean).join(' ') : ''))
      for (const z of byZone(tg.targets)) {
        out.push(
          '    ' +
            z.zone +
            ': ' +
            z.targets
              .map((t) => {
                const pct = lastPct(t)
                return t.id + ' ' + (t.state || 'unknown') + (t.reason ? ' (' + t.reason + ')' : '') + (pct == null ? '' : ' 5xx ' + fmt(pct) + '%')
              })
              .join(', '),
        )
      }
    }
  }
  if (out.length === 0) out.push('No load balancers in scope.')
  return out.join('\n')
}
