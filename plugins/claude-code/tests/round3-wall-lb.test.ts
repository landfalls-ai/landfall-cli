import { expect, test } from 'claude-code/testing'
import { Drawn, fakeIo, kitFor, openConsoleOn, setRooms, settle } from './_tab'
import { chartSvg, seriesTone } from '../hooks/views.js'
import * as wallTab from '../hooks/components/wall.js'
import * as lbTab from '../hooks/components/lb.js'
import { consoleState } from '../hooks/core.js'

// Round 3: what the real-app captures of round 2 found on the Wall and the Load balancers tabs
// (rounds/2/REVIEW.md items 3, 6, 7, 9, 14, 16 and 19), one test per fix the test kit can see.

const T0 = 1791472800000
const ROOM = {
  roomKey: 'k168', incidentId: 'i168', displayId: '168', title: 'cloudfront-5xx-high', slug: 'acme', connection: 'live', count: 0, addressed: 0, votesAwaited: 0, maxSeq: 233,
  digest: [] as string[], status: { status: 'investigating', severity: 'SEV2', people: [] as unknown[] },
}
const UNTONED_STAT = { id: 's1', type: 'stat', title: 'Requests per minute', value: '48200', trend: 'up', delta: 2.6, deltaLabel: '↑ 2.6%', baselineLabel: '6 an hour ago', spark: [1, 2, 3, 4] }
const TONED_STAT = { id: 's2', type: 'stat', title: 'Healthy origins', value: '4', unit: '/6', tone: 'warning', trend: 'down', delta: -2, spark: [6, 5, 4] }
const CHART = { id: 'c1', type: 'chart', title: 'Latency', series: [{ label: 'p99', points: [1, 3, 2, 5].map((v, i) => [T0 + i * 60000, v]) }] }
const ANSWER = {
  ok: true, sharedBy: 'carol', windowMs: 21600000, widgets: [UNTONED_STAT, TONED_STAT, CHART],
  unavailable: [{ id: 'u1', type: 'chart', title: 'RDS replica lag', reason: 'Sign in to read this widget as yourself: run landfall login.' }], totalWidgets: 9,
}

async function drawWall(surface: 'terminal' | 'desktop', answer: unknown = ANSWER) {
  wallTab.reset()
  wallTab.setWallCap()
  setRooms([ROOM])
  openConsoleOn('wall')
  consoleState.bodyRows = 0
  const io = fakeIo(() => answer)
  wallTab.warm(io as never)
  await settle()
  return new Drawn(wallTab.tab(kitFor(surface), io as never, io.clock.t, null))
}

// ---------- item 6: a delta is a label only when the widget carries a tone ----------

for (const surface of ['terminal', 'desktop'] as const) {
  test(`item 6, ${surface}: an untoned rise is dim text, a toned stat's change is a label in its tone`, async () => {
    const pane = await drawWall(surface)
    const dim = pane.find({ type: 'Text', key: 'w-s1-d' })!
    expect(dim.props.dimColor).toBe(true)
    expect(dim.props.color).toBeUndefined()
    expect(String(dim.children[0])).toBe('↑ 2.6%')
    const label = pane.find({ type: 'Text', key: 'w-s2-d-t' }) ?? pane.find({ type: 'Text', key: 'w-s2-d' })!
    expect(String(label.children[0])).toMatch(/^● ↓ 2/)
    expect(label.props.color).toBe('#fab219')
  })
}

// ---------- item 7: a count and a humanized phrase are never joined ----------

test('item 7: the baseline line says when in a short unit, and never `6 an hour ago`', () => {
  expect(wallTab.baselineWords('6 an hour ago')).toBe('6 · 1h ago')
  expect(wallTab.baselineWords('6 a minute ago')).toBe('6 · 1m ago')
  expect(wallTab.baselineWords('120 3 hours ago')).toBe('120 · 3h ago')
  expect(wallTab.baselineWords('48 45 minutes ago')).toBe('48 · 45m ago')
  expect(wallTab.baselineWords('an hour ago')).toBe('as of 1h ago')
  expect(wallTab.baselineWords('2 hours ago')).toBe('as of 2h ago')
  expect(wallTab.baselineWords('vs last week')).toBe('vs last week')
  expect(wallTab.baselineWords('6 an hour ago')).not.toMatch(/\d an /)
})

test('item 7: the tab draws the baseline through the formatter', async () => {
  const pane = await drawWall('desktop')
  expect(pane.find({ type: 'Text', text: '6 · 1h ago' })).toBeDefined()
  expect(pane.find({ type: 'Text', text: /an hour ago/ })).toBeUndefined()
})

// ---------- item 9: an untoned chart is neutral ----------

test('item 9: a lone series the widget never toned is neutral, on the stat sparkline and the chart', async () => {
  expect(seriesTone(0, undefined, 1)).toBe('neutral')
  expect(seriesTone(0, 'critical', 1)).toBe('critical')
  expect(seriesTone(1, undefined, 3)).toBe('violet')
  const series = [{ label: 's', points: [[1, 1], [2, 3], [3, 2]] }]
  const svg = chartSvg(series, [], undefined, { width: 100, height: 40 })
  expect(svg).toContain('stroke="#898781"')
  expect(svg).not.toContain('#2a78d6')
  const toned = chartSvg(series, [], undefined, { width: 100, height: 40, tone: 'critical' })
  expect(toned).toContain('stroke="#d03b3b"')
  const pane = await drawWall('desktop')
  const sparks = pane.all({ type: 'Svg' }).filter((x) => /trend$/.test(String(x.props.alt)))
  expect(sparks.length).toBeGreaterThan(0)
  for (const s of sparks.filter((x) => String(x.props.alt).startsWith('Requests'))) {
    expect(s.props.source).toContain('#898781')
    expect(s.props.source).not.toContain('#2a78d6')
  }
  const chartSvgNode = pane.all({ type: 'Svg' }).find((x) => String(x.props.alt).startsWith('Latency'))!
  expect(chartSvgNode.props.source).toContain('stroke="#898781"')
  expect(chartSvgNode.props.source).not.toContain('stroke="#2a78d6"')
})

// ---------- item 16: one blank row between blocks on the terminal Wall ----------

test('item 16: the terminal Wall puts one blank row after the selector and before each tail block, never two', async () => {
  const pane = await drawWall('terminal')
  const rows = (wallTab.tab(kitFor('terminal'), fakeIo(() => ANSWER) as never, 0, null) as any[]).filter(Boolean)
  const blank = (n: any) => n.type === 'Text' && String(n.children[0]) === ' '
  expect(rows[0].key).toBe('wall-sel')
  expect(blank(rows[1])).toBe(true)
  const keys = rows.map((n) => n.key)
  const at = (key: string) => keys.indexOf(key)
  expect(blank(rows[at('un-h') - 1])).toBe(true)
  expect(blank(rows[at('wall-more') - 1])).toBe(true)
  for (let i = 1; i < rows.length; i++) expect(blank(rows[i]) && blank(rows[i - 1])).toBe(false)
  expect(blank(rows.at(-1))).toBe(false) // the console adds the one before the keys, not the tab
  void pane
  // The desktop draws no blank rows: cards space themselves.
  const desk = await drawWall('desktop')
  expect(desk.all({ type: 'Text', text: /^ $/ })).toHaveLength(0)
})

// ---------- item 19: the unavailable list says why in plain words ----------

test('item 19: an unavailable widget is its name, then a plain reason with at most one colon', async () => {
  expect(wallTab.unavailableWords({ title: 'RDS replica lag', reason: 'Sign in to read this widget as yourself: run landfall login.' })).toBe('RDS replica lag · needs your sign-in. Run landfall login.')
  expect(wallTab.unavailableWords({ title: 'X', reason: 'Needs a connection: you cannot read it' })).toBe('X · needs a connection, you cannot read it')
  expect(wallTab.unavailableWords({ type: 'chart' })).toBe('chart · not available')
  for (const surface of ['terminal', 'desktop'] as const) {
    const pane = await drawWall(surface)
    const line = pane.find({ type: 'Text', key: 'un-0' })!
    expect(String(line.children[0])).toBe('RDS replica lag · needs your sign-in. Run landfall login.')
    expect(String(line.children[0]).split(':').length - 1).toBeLessThanOrEqual(1)
  }
})

// ---------- items 3 and 14: the Load balancers board on the desktop ----------

const N = 36
const START = 1791478200000
const PCT = Array.from({ length: N }, (_, m) => (m === 3 ? null : m >= 18 ? 34 : 0.2))
const TARGETS = [
  { id: 'i-0a3f', zone: 'us-east-1a', state: 'healthy', port: 8080 },
  { id: 'i-0b71', zone: 'us-east-1a', state: 'healthy', port: 8080 },
  { id: 'i-04c2', zone: 'us-east-1b', state: 'healthy', port: 8080 },
]
const LB = {
  ok: true, region: 'us-east-1', minutes: N, fiveXxBy: 'targetGroup',
  loadBalancers: [
    {
      name: 'web-edge-alb', scheme: 'internet-facing', state: 'active', healthy: 5, total: 8, minuteStartMs: START, zones: ['us-east-1a'],
      healthyHostCount: Array.from({ length: N }, () => 5),
      targetGroups: [{ name: 'web-edge-api-tg', protocol: 'HTTP', port: 8081, healthCheck: '/healthz/deep/readiness', targets: TARGETS, fiveXx: PCT.map(() => 0), requests: PCT.map(() => 500), fiveXxPct: PCT }],
    },
  ],
}

async function drawLb(surface: 'terminal' | 'desktop') {
  setRooms([ROOM])
  openConsoleOn('lb')
  const io = fakeIo(() => LB)
  await lbTab.warm(io as never)
  return new Drawn(lbTab.tab(kitFor(surface), io as never, io.clock.t, null))
}

test('item 14, desktop: the path has its own dim line, no floating time, one wrapping legend, a 9-cell instance slot, a sentence-case caption', async () => {
  const pane = await drawLb('desktop')
  expect(pane.find({ type: 'Text', text: 'web-edge-api-tg · HTTP 8081' })).toBeDefined()
  expect(pane.find({ type: 'Text', text: /\/healthz\/deep\/readiness$/ })?.props.dimColor).toBe(true)
  expect(pane.find({ type: 'Text', text: /…/ })).toBeUndefined()
  // No axis row: neither a floating start time nor a lone "now".
  expect(pane.find({ key: 'lb0-axis' })).toBeUndefined()
  expect(pane.find({ type: 'Text', text: 'now' })).toBeUndefined()
  expect(pane.find({ type: 'Text', text: /^5xx per target group, last 36 minutes, since 16:50Z$/ })).toBeDefined()
  // The legend: one row that wraps, one glyph style.
  const legend = pane.find({ key: 'lb0-legend' })!
  expect(legend.props.flexWrap).toBe('wrap')
  const swatches = pane.all({ type: 'Text' }).filter((n) => /^(■|□)$/.test(String(n.children[0])) && n.props.key?.startsWith('sw'))
  expect(swatches).toHaveLength(5)
  // Every instance sits in a slot of 9 cells, so the ids of one zone line up with the next.
  const slots = pane.all({ type: 'Box' }).filter((n) => n.props.minWidth === 9)
  expect(slots).toHaveLength(3)
  for (const s of slots) expect(s.props.flexShrink).toBe(0)
  // The caption for the sparkline is a sentence-case heading.
  const cap = pane.find({ type: 'Text', text: 'Healthy hosts' })!
  expect(cap.props.bold).toBe(true)
  expect(pane.find({ type: 'Text', text: 'healthy hosts' })).toBeUndefined()
})

test('item 14, terminal: unchanged, the heading keeps its path and the axis keeps its labels', async () => {
  const pane = await drawLb('terminal')
  expect(pane.find({ type: 'Text', text: 'web-edge-api-tg · HTTP 8081 · /healthz/deep/readiness' })).toBeDefined()
  expect(pane.find({ type: 'Text', text: /^ +16:50 +now$/ })).toBeDefined()
  expect(pane.find({ type: 'Text', text: 'healthy hosts' })).toBeDefined()
})

test('item 3, desktop: the heat strips paint their own dark-safe ground and no light colour', async () => {
  const pane = await drawLb('desktop')
  const heat = pane.all({ type: 'Svg' }).find((x) => String(x.props.alt).startsWith('5xx per minute'))!
  expect(heat.props.isInteractive).toBe(true)
  expect(heat.props.source).toContain('prefers-color-scheme:dark')
  expect(heat.props.source).not.toMatch(/#e4e4e2|#b4b4b0/i)
  expect(heat.props.source).toContain('fill="#898781" fill-opacity=".25"')
  const spark = pane.all({ type: 'Svg' }).find((x) => x.props.alt === 'healthy host count')!
  expect(spark.props.source).toContain('prefers-color-scheme:dark')
  expect(spark.props.source).toContain('stroke="#898781" stroke-opacity=".35"')
  expect(spark.props.source).not.toMatch(/#8a8a8a|fill="#fff/i)
})
