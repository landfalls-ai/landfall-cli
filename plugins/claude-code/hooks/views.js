// Drawing helpers for the view panes (wall, lb, topology, timeline): pure
// functions over the CLI's JSON (contracts/cli-json.md §4 to §6). Nothing here
// holds `$`: the SVG builders answer markup strings, the text builders answer
// lines, and graphView takes the kit (`k`), which carries only the element
// table.

import { SVG_THEME, TONE } from './kit.js'

export function esc(s) {
  return String(s ?? '').replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c])
}

// clipText cuts a line to a width with an ellipsis.
export function clipText(text, width) {
  text = String(text ?? '')
  return text.length > width ? text.slice(0, Math.max(1, width - 1)) + '…' : text
}

// hhmm is a time as a person reads it in a room: "15:52" (UTC), from an RFC
// 3339 string or epoch milliseconds. No Date, so it reads the same everywhere.
export function hhmm(at) {
  if (typeof at === 'number' && isFinite(at)) {
    const m = Math.floor(at / 60000) % 1440
    return String(Math.floor(m / 60)).padStart(2, '0') + ':' + String(m % 60).padStart(2, '0')
  }
  const s = String(at ?? '')
  const hit = /T(\d\d:\d\d)/.exec(s)
  return hit ? hit[1] : s.slice(0, 5)
}

// windowWords is a window in words: "last 6h", "last 30m".
export function windowWords(ms) {
  if (!ms || !isFinite(ms)) return ''
  const m = Math.round(ms / 60000)
  if (m < 120) return 'last ' + m + 'm'
  const h = Math.round(m / 60)
  return h < 48 ? 'last ' + h + 'h' : 'last ' + Math.round(h / 24) + 'd'
}

// fmt is a number short enough for a tile: 48200 -> "48.2k", 3.14159 -> "3.14".
export function fmt(v) {
  if (typeof v !== 'number' || !isFinite(v)) return String(v ?? '')
  const a = Math.abs(v)
  if (a >= 1e6) return (v / 1e6).toFixed(1).replace(/\.0$/, '') + 'M'
  if (a >= 1e4) return (v / 1e3).toFixed(1).replace(/\.0$/, '') + 'k'
  if (a >= 100 || Number.isInteger(v)) return String(Math.round(v))
  return String(Number(v.toFixed(2)))
}

export function toneColor(tone) {
  return TONE[tone] || TONE.neutral
}

// ---------- charts ----------

const SERIES_TONES = ['info', 'violet', 'serious', 'neutral', 'good']

// seriesTone is the color of the i-th series of `count`: the widget's own tone first. A lone series
// the widget never toned is neutral (spec §6 Sparklines: color only for state, round 2 issue 9);
// several untoned series take the categorical palette, since they must be told apart.
export function seriesTone(i, tone, count = 2) {
  if (i === 0 && tone && TONE[tone] && tone !== 'neutral') return tone
  if (count <= 1) return 'neutral'
  return SERIES_TONES[i % SERIES_TONES.length]
}

// values are a series' numbers in time order.
export function values(series) {
  return (series?.points ?? []).map((p) => (Array.isArray(p) ? p[1] : p)).filter((v) => typeof v === 'number' && isFinite(v))
}

function times(series) {
  return (series?.points ?? []).map((p) => (Array.isArray(p) ? p[0] : NaN)).filter((t) => typeof t === 'number' && isFinite(t))
}

// chartSvg is a line chart over every series: a faint grid, the threshold as
// a dashed critical line, each marker (a deploy) as a dashed vertical with its
// label, and each series with an emphasized end point.
export function chartSvg(series, markers, threshold, opts = {}) {
  const w = opts.width || 300
  const h = opts.height || 90
  const padTop = 12
  const padBottom = 4
  const all = (series || []).filter((s) => values(s).length >= 2)
  const ts = all.flatMap(times)
  const vs = all.flatMap(values)
  if (typeof threshold === 'number' && isFinite(threshold)) vs.push(threshold)
  const t0 = ts.length ? Math.min(...ts) : 0
  const t1 = ts.length ? Math.max(...ts) : 1
  const lo = Math.min(0, ...vs)
  const hi = Math.max(...vs, lo + 1)
  const x = (t) => ((t - t0) / (t1 - t0 || 1)) * w
  const y = (v) => h - padBottom - ((v - lo) / (hi - lo || 1)) * (h - padTop - padBottom)
  let s = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ' + w + ' ' + h + '">' + SVG_THEME
  for (let g = 1; g < 4; g++) s += '<line x1="0" x2="' + w + '" y1="' + (h * g) / 4 + '" y2="' + (h * g) / 4 + '" stroke="#898781" stroke-opacity=".35"/>'
  if (typeof threshold === 'number' && isFinite(threshold)) {
    const ty = y(threshold).toFixed(1)
    s += '<line x1="0" x2="' + w + '" y1="' + ty + '" y2="' + ty + '" stroke="' + TONE.critical + '" stroke-dasharray="4 3" stroke-opacity=".8"><title>threshold ' + esc(fmt(threshold)) + '</title></line>'
  }
  for (const m of markers || []) {
    if (typeof m.atMs !== 'number' || !ts.length || m.atMs < t0 || m.atMs > t1) continue
    const mx = x(m.atMs).toFixed(1)
    s += '<line x1="' + mx + '" x2="' + mx + '" y1="0" y2="' + h + '" stroke="#898781" stroke-dasharray="2 3"><title>' + esc(m.label) + ' at ' + hhmm(m.atMs) + 'Z</title></line>'
    s += '<text x="' + (Number(mx) + 3) + '" y="9" font-size="8" class="mu">' + esc(clipText(m.label, 24)) + '</text>'
  }
  all.forEach((ser, i) => {
    const color = TONE[seriesTone(i, opts.tone, all.length)]
    const pts = (ser.points || []).filter((p) => Array.isArray(p) && isFinite(p[0]) && isFinite(p[1])).map((p) => [x(p[0]), y(p[1])])
    if (pts.length < 2) return
    const d = pts.map((p, j) => (j ? 'L' : 'M') + p[0].toFixed(1) + ' ' + p[1].toFixed(1)).join(' ')
    if (all.length === 1) s += '<path d="' + d + ' L' + w + ' ' + h + ' L0 ' + h + ' Z" fill="' + color + '" fill-opacity=".12"/>'
    s += '<path d="' + d + '" fill="none" stroke="' + color + '" stroke-width="1.8" stroke-linejoin="round"><title>' + esc(ser.label || 'series ' + (i + 1)) + '</title></path>'
    const last = pts[pts.length - 1]
    s += '<circle cx="' + last[0].toFixed(1) + '" cy="' + last[1].toFixed(1) + '" r="2.6" fill="' + color + '"/>'
  })
  return s + '</svg>'
}

// chartAlt says what a chart shows for a reader that cannot see it.
export function chartAlt(title, series, markers, threshold) {
  const parts = []
  for (const s of series || []) {
    const v = values(s)
    if (v.length === 0) continue
    parts.push((s.label || 'series') + ' last ' + fmt(v[v.length - 1]) + ', peak ' + fmt(Math.max(...v)))
  }
  if (typeof threshold === 'number') parts.push('threshold ' + fmt(threshold))
  for (const m of markers || []) parts.push(m.label + ' at ' + hhmm(m.atMs) + 'Z')
  return (title ? title + ': ' : '') + (parts.join('; ') || 'no data')
}

// markerColumn is where a time falls in a sparkline of `cols` cells drawn from
// these points (kit.spark keeps every point when they fit, else buckets them).
export function markerColumn(series, atMs, cols) {
  const ts = times(series)
  if (ts.length < 2 || typeof atMs !== 'number') return -1
  if (atMs < ts[0] || atMs > ts[ts.length - 1]) return -1
  let best = 0
  for (let i = 1; i < ts.length; i++) if (Math.abs(ts[i] - atMs) < Math.abs(ts[best] - atMs)) best = i
  if (ts.length <= cols) return best
  return Math.min(cols - 1, Math.floor((best * cols) / ts.length))
}

// markerCaption is the line under a terminal sparkline: "      ↑ web-edge v2.3.1".
export function markerCaption(series, markers, cols, width) {
  const out = []
  for (const m of markers || []) {
    const col = markerColumn(series, m.atMs, cols)
    if (col < 0) continue
    const label = '↑ ' + m.label + ' ' + hhmm(m.atMs) + 'Z'
    const at = Math.max(0, Math.min(col, width - label.length))
    out.push(' '.repeat(at) + label)
  }
  return out.map((l) => clipText(l, width))
}

// ---------- tables, logs, events ----------

// tableRows aligns a table's columns into lines, header first, every line
// clipped to the width; a cell wider than its share is cut.
export function tableRows(columns, rows, width, maxRows = 12) {
  const cols = (columns || []).map(String)
  const body = (rows || []).slice(0, maxRows).map((r) => (r || []).map((c) => (c == null ? '' : String(c))))
  const n = Math.max(cols.length, ...body.map((r) => r.length), 0)
  if (n === 0) return []
  const want = []
  for (let i = 0; i < n; i++) want.push(Math.max((cols[i] || '').length, ...body.map((r) => (r[i] || '').length), 1))
  const room = Math.max(n * 4, width - (n - 1) * 2)
  const total = want.reduce((a, b) => a + b, 0)
  const widths = total <= room ? want : want.map((x) => Math.max(3, Math.floor((x / total) * room)))
  const line = (cells) => clipText(widths.map((wd, i) => clipText(cells[i] || '', wd).padEnd(wd)).join('  ').trimEnd(), width)
  const out = [line(cols)]
  for (const r of body) out.push(line(r))
  if ((rows || []).length > maxRows) out.push((rows.length - maxRows) + ' more rows')
  return out
}

// levelTone colors a log line by its level.
export function levelTone(level) {
  const l = String(level || '').toLowerCase()
  if (/err|fatal|crit|panic/.test(l)) return 'critical'
  if (/warn/.test(l)) return 'warning'
  if (/info|notice/.test(l)) return 'info'
  return 'neutral'
}

export function levelTag(level) {
  const t = levelTone(level)
  return t === 'critical' ? 'ERR' : t === 'warning' ? 'WRN' : t === 'info' ? 'INF' : 'DBG'
}

// outcomeTone colors an audit event by how it ended.
export function outcomeTone(outcome) {
  const o = String(outcome || '').toLowerCase()
  if (/fail|denied|error|refused/.test(o)) return 'critical'
  if (/success|ok|allowed/.test(o)) return 'good'
  return 'neutral'
}

// eventLine is one audit event: "15:48 deploy-bot UpdateService web-edge · success".
export function eventLine(ev) {
  const head = [hhmm(ev.at), ev.actor, ev.action, ev.target].filter(Boolean).join(' ')
  return ev.outcome ? head + ' · ' + ev.outcome : head
}

// ---------- load balancers ----------

// fiveXxTone is a 5xx share's tone: under 1% quiet (null), 1 to 5 good,
// 5 to 20 warning, over 20 critical.
export function fiveXxTone(pct) {
  if (typeof pct !== 'number' || !isFinite(pct) || pct < 1) return null
  if (pct < 5) return 'good'
  if (pct <= 20) return 'warning'
  return 'critical'
}

// pctCell is one minute of a target group's 5xx share as a heat cell:
// 'none' when the minute had no requests (the CLI sends null), else its tone.
export function pctCell(pct) {
  if (pct == null) return 'none'
  return fiveXxTone(pct)
}

// healthTone is a target's health state's tone.
export function healthTone(state) {
  const s = String(state || '').toLowerCase()
  if (s === 'healthy') return 'good'
  if (s === 'unhealthy') return 'critical'
  if (s === 'draining' || s === 'initial' || s === 'unavailable') return 'warning'
  return 'neutral'
}

// byZone groups a target group's targets by availability zone, zones in order.
export function byZone(targets) {
  const zones = []
  const index = {}
  for (const t of targets || []) {
    const z = t.zone || 'no zone'
    if (!(z in index)) {
      index[z] = zones.length
      zones.push({ zone: z, targets: [] })
    }
    zones[index[z]].targets.push(t)
  }
  return zones.sort((a, b) => (a.zone < b.zone ? -1 : a.zone > b.zone ? 1 : 0))
}

// shortZone is "1a" for "us-east-1a".
export function shortZone(zone) {
  const m = /-(\d+[a-z])$/.exec(String(zone || ''))
  return m ? m[1] : String(zone || '')
}

// healthyTone is the tone of "healthy N of M".
export function healthyTone(healthy, total) {
  if (!total) return 'neutral'
  if (healthy >= total) return 'good'
  if (healthy === 0) return 'critical'
  return 'warning'
}

// lastPct is a target group's latest 5xx share (CloudWatch reports 5xx per
// target group, never per target), skipping minutes with no requests.
export function lastPct(t) {
  const v = (t.fiveXxPct || []).filter((x) => typeof x === 'number' && isFinite(x))
  return v.length ? v[v.length - 1] : null
}

// ---------- graphs ----------

export const TRUST_DASH = { confirmed: '', established: '6 3', inferred: '2 4' }

// layers places a graph's nodes in columns by their distance from a root
// (a node nothing points at); a cycle with no root starts at its first node.
export function layers(nodes, edges) {
  const ids = (nodes || []).map((n) => n.id)
  const known = new Set(ids)
  const into = {}
  const out = {}
  for (const id of ids) {
    into[id] = 0
    out[id] = []
  }
  for (const e of edges || []) {
    if (!known.has(e.from) || !known.has(e.to)) continue
    into[e.to] += 1
    out[e.from].push(e.to)
  }
  const depth = {}
  const queue = ids.filter((id) => into[id] === 0)
  for (const id of queue) depth[id] = 0
  const visit = () => {
    while (queue.length) {
      const id = queue.shift()
      for (const to of out[id]) {
        if (depth[to] == null) {
          depth[to] = depth[id] + 1
          queue.push(to)
        }
      }
    }
  }
  visit()
  for (const id of ids) {
    if (depth[id] == null) {
      depth[id] = 0
      queue.push(id)
      visit()
    }
  }
  const cols = []
  for (const id of ids) {
    const d = depth[id]
    while (cols.length <= d) cols.push([])
    cols[d].push(id)
  }
  return cols
}

// graphSvg draws a topology: nodes in columns by depth, outlined in their
// tone, edges styled by trust (solid confirmed, dashed established, dotted
// inferred), with a legend. Hover titles name each node and edge.
export function graphSvg(nodes, edges) {
  const cols = layers(nodes, edges)
  const byId = {}
  for (const n of nodes || []) byId[n.id] = n
  const nodeW = 112
  const nodeH = 28
  const gapX = 58
  const gapY = 18
  const tallest = Math.max(1, ...cols.map((c) => c.length))
  const w = Math.max(240, cols.length * nodeW + (cols.length - 1) * gapX + 16)
  const h = tallest * nodeH + (tallest - 1) * gapY + 16 + 18
  const pos = {}
  cols.forEach((col, ci) => {
    const colH = col.length * nodeH + (col.length - 1) * gapY
    const top = 8 + (tallest * nodeH + (tallest - 1) * gapY - colH) / 2
    col.forEach((id, ri) => {
      pos[id] = { x: 8 + ci * (nodeW + gapX), y: top + ri * (nodeH + gapY) }
    })
  })
  let s = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ' + w + ' ' + h + '">' + SVG_THEME
  for (const e of edges || []) {
    const a = pos[e.from]
    const b = pos[e.to]
    if (!a || !b) continue
    const trust = String(e.trust || 'inferred')
    const dash = TRUST_DASH[trust] ?? TRUST_DASH.inferred
    const said = [trust, e.label || '', e.direction === 'both' ? 'both ways' : ''].filter(Boolean).join(', ')
    s += '<line x1="' + (a.x + nodeW) + '" y1="' + (a.y + nodeH / 2) + '" x2="' + b.x + '" y2="' + (b.y + nodeH / 2) + '" stroke="#8a8a8a" stroke-width="1.4"' + (dash ? ' stroke-dasharray="' + dash + '"' : '') + '><title>' + esc((byId[e.from]?.label || e.from) + ' to ' + (byId[e.to]?.label || e.to) + ': ' + said) + '</title></line>'
  }
  for (const n of nodes || []) {
    const p = pos[n.id]
    if (!p) continue
    const toned = n.tone && n.tone !== 'neutral' && TONE[n.tone]
    s += '<g><title>' + esc(n.label || n.id) + (n.kind ? ' (' + esc(n.kind) + ')' : '') + (n.tone ? ' · ' + esc(n.tone) : '') + '</title>'
    s += '<rect x="' + p.x + '" y="' + p.y + '" width="' + nodeW + '" height="' + nodeH + '" rx="7" fill="' + (toned ? TONE[n.tone] : '#8a8a8a') + '" fill-opacity=".08" stroke="' + (toned ? TONE[n.tone] : '#8a8a8a') + '" stroke-width="' + (toned ? 2 : 1) + '"/>'
    s += '<text x="' + (p.x + nodeW / 2) + '" y="' + (p.y + nodeH / 2 + 4) + '" text-anchor="middle" font-size="11" font-family="ui-monospace,monospace" class="fg">' + esc(clipText(n.label || n.id, 16)) + '</text></g>'
  }
  s += '<text x="8" y="' + (h - 4) + '" font-size="10" class="mu">solid confirmed · dashed established · dotted inferred</text>'
  return { source: s + '</svg>', width: w, height: h }
}

// graphAlt says what a topology shows.
export function graphAlt(title, nodes, edges) {
  const flagged = (nodes || []).filter((n) => n.tone === 'critical' || n.tone === 'serious' || n.tone === 'warning').map((n) => (n.label || n.id) + ' ' + n.tone)
  return (title || 'Topology') + ': ' + (nodes || []).length + ' nodes, ' + (edges || []).length + ' links' + (flagged.length ? '; ' + flagged.join(', ') : '')
}

// graphTree is a topology as an indented list for the terminal: each root,
// then what it reaches, one line per link with its trust word. A node reached
// twice is listed again by name with "(above)" rather than walked twice.
export function graphTree(nodes, edges) {
  const byId = {}
  for (const n of nodes || []) byId[n.id] = n
  const out = {}
  const into = {}
  for (const n of nodes || []) {
    out[n.id] = []
    into[n.id] = 0
  }
  for (const e of edges || []) {
    if (!byId[e.from] || !byId[e.to]) continue
    out[e.from].push(e)
    into[e.to] += 1
  }
  const lines = []
  const seen = new Set()
  const walk = (id, depth, trust, last, prefix) => {
    const n = byId[id]
    const again = seen.has(id)
    const branch = depth === 0 ? '' : prefix + (last ? '└─' : '├─')
    lines.push({ id, depth, label: (n.label || id) + (n.kind ? ' (' + n.kind + ')' : ''), tone: n.tone || 'neutral', trust: trust || '', branch, again })
    if (again) return
    seen.add(id)
    const kids = out[id]
    kids.forEach((e, i) => walk(e.to, depth + 1, String(e.trust || 'inferred') + (e.direction === 'both' ? ', both ways' : ''), i === kids.length - 1, depth === 0 ? '' : prefix + (last ? '   ' : '│  ')))
  }
  for (const n of nodes || []) if (into[n.id] === 0) walk(n.id, 0, '', true, '')
  for (const n of nodes || []) if (!seen.has(n.id)) walk(n.id, 0, '', true, '')
  return lines
}

// graphLines are graphTree's lines as text.
export function graphLines(nodes, edges) {
  return graphTree(nodes, edges).map((l) => (l.branch ? l.branch + ' ' : '') + l.label + (l.again ? ' (above)' : '') + (l.trust ? '  ' + l.trust : '') + (l.tone && l.tone !== 'neutral' ? '  ' + l.tone : ''))
}

// graphView draws a graph widget with the kit: an Svg on a surface that has
// one, the tree list on the terminal (trust words dim, nodes in their tone).
export function graphView(k, widget, key) {
  const nodes = widget.nodes || []
  const edges = widget.edges || []
  if (nodes.length === 0) return [k.text('No nodes yet.', { key: key + '-none', dimColor: true })]
  if (k.rich) {
    const g = graphSvg(nodes, edges)
    return [k.svg(g.source, { key: key + '-svg', alt: graphAlt(widget.title, nodes, edges), width: g.width, height: g.height, hover: true })]
  }
  const { Text } = k.els
  return graphTree(nodes, edges).map((l, i) => {
    const name = { key: 'n', bold: l.depth === 0, children: [clipText(l.label + (l.again ? ' (above)' : ''), Math.max(8, k.width - l.branch.length - 14))] }
    if (l.tone && l.tone !== 'neutral') name.color = toneColor(l.tone)
    return k.row(
      [
        l.branch ? Text({ key: 'b', dimColor: true, children: [l.branch] }) : null,
        Text(name),
        l.trust ? Text({ key: 't', dimColor: true, children: [l.trust] }) : null,
      ],
      key + '-g' + i,
      1,
    )
  })
}
