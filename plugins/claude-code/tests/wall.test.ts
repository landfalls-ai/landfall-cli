import { expect, test } from 'claude-code/testing'
import { Drawn, fakeIo, kitFor, openConsoleOn, setRooms, settle } from './_tab'
import { consoleState } from '../hooks/core.js'
import * as wallTab from '../hooks/components/wall.js'
import * as rosterTab from '../hooks/components/roster.js'

// `landfall wall` as the Go CLI prints it (internal/cli/wall.go
// flattenWidget, wall_test.go): every widget type, data already resolved
// through the person's own session; points are [ms, v]; a widget with no data
// yet is `empty: true`; an unavailable one names its type.
const T0 = 1791472800000 // 2026-10-08T15:20:00Z
const MIN = 60000
const ERR = [0.2, 0.2, 0.3, 0.2, 0.3, 0.2, 0.2, 0.3, 0.9, 1.8, 3.4, 5.9, 6.1, 5.2, 4.4, 3.6, 3.1, 3.1, 3.0, 3.1]
const WALL = {
  ok: true,
  sharedBy: 'carol',
  windowMs: 21600000,
  widgets: [
    {
      id: 'w1',
      type: 'chart',
      title: '5xx error rate',
      tone: 'critical',
      series: [{ label: '5xxErrorRate', points: ERR.map((v, i) => [T0 + i * MIN, v]), unit: '%' }],
      markers: [{ atMs: T0 + 9 * MIN, label: 'web-edge v2.3.1' }],
      threshold: 2,
      unit: '%',
    },
    { id: 'w2', type: 'stat', title: 'Healthy origins', value: '4', unit: '/6', tone: 'warning', trend: 'down', baselineLabel: '6 an hour ago', delta: -2, spark: [6, 6, 6, 5, 4, 4] },
    {
      id: 'w3',
      type: 'logView',
      title: 'Logs · web-edge',
      lines: [
        { level: 'error', text: 'upstream timeout pool=origin-b', at: '2026-10-08T15:30:02Z' },
        { level: 'error', text: 'upstream timeout pool=origin-b', at: '2026-10-08T15:30:04Z' },
        { level: 'warn', text: 'retry budget 80% used' },
      ],
    },
    {
      id: 'w4',
      type: 'graph',
      title: 'Topology',
      nodes: [
        { id: 'cf', label: 'cloudfront', tone: 'warning', kind: 'cdn' },
        { id: 'alb', label: 'web-edge-alb', tone: 'warning', trust: 'confirmed' },
        { id: 'c', label: 'us-east-1c', tone: 'critical' },
      ],
      edges: [
        { from: 'cf', to: 'alb', trust: 'confirmed' },
        { from: 'alb', to: 'c', trust: 'established', direction: 'both' },
      ],
      focus: 'alb',
    },
    {
      id: 'w5',
      type: 'table',
      title: 'Target groups',
      columns: ['target', 'zone', 'state'],
      rows: [
        ['i-0a3f', 'us-east-1a', 'healthy'],
        ['i-07aa', 'us-east-1c', 'unhealthy'],
      ],
    },
    {
      id: 'w6',
      type: 'events',
      title: 'Deploys',
      events: [{ at: '2026-10-08T15:29:00Z', actor: 'deploy-bot', action: 'UpdateService', target: 'web-edge', outcome: 'success' }],
    },
    { id: 'w7', type: 'timeline', title: 'What happened', items: [{ at: '2026-10-08T15:31:00Z', label: 'Alarm 5xx over 2%', tone: 'critical' }] },
    { id: 'w8', type: 'heatmapOfTheFuture', title: 'Mystery widget' },
    {
      id: 'w10',
      type: 'geo',
      title: '5xx by region',
      points: [
        { place: 'us-east-1', label: 'N. Virginia', value: 34, unit: '%', tone: 'critical' },
        { place: 'eu-west-1', value: 0.4, unit: '%' },
      ],
    },
    {
      id: 'w11',
      type: 'codeFinding',
      title: 'Pool size',
      repo: 'acme/web-edge',
      path: 'src/pool.ts',
      permalink: 'https://github.com/acme/web-edge/blob/abc123/src/pool.ts#L41-L43',
      startLine: 41,
      lines: ['export const POOL = {', '  max: 16,', '}'],
    },
    { id: 'w12', type: 'chart', title: 'p99 latency', empty: true },
  ],
  unavailable: [{ id: 'w9', type: 'chart', title: 'RDS replica lag', reason: 'needs a connection you cannot read' }],
}

const ROOM = {
  roomKey: 'k168',
  incidentId: 'i168',
  displayId: 'Landfall 168',
  title: 'cloudfront-5xx-high',
  slug: 'acme',
  connection: 'live',
  count: 0,
  addressed: 0,
  votesAwaited: 0,
  maxSeq: 233,
  widgetSeq: 231,
  digest: [] as string[],
  status: { status: 'investigating', severity: 'SEV2', people: [] as unknown[] },
}

const SURFACES = ['terminal', 'desktop'] as const

// A fresh tab on a room, the way the console opens it: nothing read, nothing selected.
function begin(rooms: unknown[] = [ROOM]) {
  wallTab.reset()
  wallTab.setWallCap()
  rosterTab.reset()
  setRooms(rooms)
  openConsoleOn('wall')
}

async function draw(surface: 'terminal' | 'desktop' | 'vscode' | 'mobile', io: ReturnType<typeof fakeIo>, args: string | null = null, width = 100) {
  return new Drawn(wallTab.tab(kitFor(surface, width), io as never, io.clock.t, args))
}

async function keysOf(surface: 'terminal' | 'desktop', io: ReturnType<typeof fakeIo>) {
  return new Drawn(wallTab.keys(kitFor(surface), io as never))
}

test('the wall draws every widget type in the shared arrangement, on each surface', async () => {
  for (const surface of SURFACES) {
    begin()
    wallTab.setWallCap(99) // eleven widgets at once; the six-widget cap has its own test below
    const io = fakeIo(() => WALL)
    wallTab.warm(io as never)
    await settle()
    expect(io.runs.at(-1)).toEqual(['wall', '--host', 'claude-code', '--room', 'k168'])
    const pane = await draw(surface, io)
    expect(pane.find({ type: 'Text', text: 'Wall · shared by carol' })).toBeDefined()
    expect(pane.find({ type: 'Text', text: ' · last 6h' })?.props.dimColor).toBe(true)
    // Every widget is a title the person can select.
    for (const title of ['5xx error rate', 'Healthy origins', 'Logs · web-edge', 'Topology', 'Target groups', 'Deploys', 'What happened', 'Mystery widget', '5xx by region', 'Pool size', 'p99 latency']) {
      expect(pane.find({ type: 'Button', text: new RegExp(title.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '$') })).toBeDefined()
    }
    // stat: the big value and its unit, its change in the stat's tone, and its baseline.
    expect(pane.find({ type: 'Text', text: '4' })).toBeDefined()
    expect(pane.find({ type: 'Text', text: '/6' })).toBeDefined()
    expect(pane.find({ type: 'Text', text: '● ↓ 2' })?.props.color).toBe('#fab219')
    expect(pane.find({ type: 'Text', text: '6 an hour ago' })).toBeDefined()
    // geo: the place plain, its share as a label in its tone.
    expect(pane.find({ type: 'Text', text: 'us-east-1 · N. Virginia' })).toBeDefined()
    expect(pane.find({ type: 'Text', text: '● 34%' })?.props.color).toBe('#d03b3b')
    expect(pane.find({ type: 'Text', text: 'eu-west-1' })).toBeDefined()
    expect(pane.find({ type: 'Text', text: '0.4%' })).toBeDefined()
    // codeFinding: where, then the snippet with its line numbers, and the link.
    expect(pane.find({ type: 'Text', text: 'acme/web-edge · src/pool.ts' })).toBeDefined()
    expect(pane.find({ type: 'Text', text: '42' })).toBeDefined()
    expect(pane.find({ type: 'Text', text: '  max: 16,' })).toBeDefined()
    expect(pane.find({ type: 'Link' })).toBeDefined()
    // A widget with no data yet says so.
    expect(pane.find({ type: 'Text', text: 'No data in this window yet.' })).toBeDefined()
    // logView: level-colored tags.
    const errs = pane.all({ type: 'Text', text: 'ERR' })
    expect(errs).toHaveLength(2)
    expect(errs[0].props.color).toBe('#d03b3b')
    expect(pane.find({ type: 'Text', text: 'WRN' })?.props.color).toBe('#fab219')
    // table: aligned columns, header first.
    expect(pane.find({ type: 'Text', text: /^target\s+zone\s+state$/ })).toBeDefined()
    expect(pane.find({ type: 'Text', text: /^i-07aa\s+us-east-1c\s+unhealthy$/ })).toBeDefined()
    // events and timeline.
    expect(pane.find({ type: 'Text', text: /^15:29 deploy-bot UpdateService web-edge/ })).toBeDefined()
    expect(pane.find({ type: 'Text', text: 'Alarm 5xx over 2%' })).toBeDefined()
    // an unknown type is its title alone.
    expect(pane.find({ type: 'Text', text: 'heatmapOfTheFuture widgets draw in the web app.' })).toBeDefined()
    // unavailable, with the reason.
    expect(pane.find({ type: 'Text', text: 'RDS replica lag: needs a connection you cannot read' })).toBeDefined()

    if (surface === 'terminal') {
      // chart: a Raster sparkline per series and the deploy marker under it.
      expect(pane.find({ type: 'Raster', key: 'w-w1-s0' })).toBeDefined()
      expect(pane.find({ type: 'Text', text: /^ {9}↑ web-edge v2\.3\.1 15:29Z$/ })).toBeDefined()
      expect(pane.find({ type: 'Raster', key: 'w-w2-s' })).toBeDefined()
      // graph: an indented tree with trust words.
      expect(pane.find({ type: 'Text', text: 'cloudfront' })).toBeDefined()
      expect(pane.find({ type: 'Text', text: 'confirmed' })).toBeDefined()
      expect(pane.find({ type: 'Text', text: 'established, both ways' })).toBeDefined()
      expect(pane.find({ type: 'Text', text: 'cloudfront (cdn)' })).toBeDefined()
      expect(pane.all({ type: 'Svg' })).toHaveLength(0)
    } else {
      // chart: a vector line with the threshold and the deploy marker.
      const svgs = pane.all({ type: 'Svg' })
      const chart = svgs.find((x) => String(x.props.alt).startsWith('5xx error rate:'))
      expect(chart?.props.source).toContain('stroke-dasharray="4 3"')
      expect(chart?.props.source).toContain('web-edge v2.3.1')
      expect(chart?.props.alt).toBe('5xx error rate: 5xxErrorRate last 3.1, peak 6.1; threshold 2; web-edge v2.3.1 at 15:29Z')
      expect(chart?.props.isInteractive).toBe(true)
      // graph: trust as line style.
      const graph = svgs.find((x) => String(x.props.alt).startsWith('Topology:'))
      expect(graph?.props.source).toContain('stroke-dasharray="6 3"')
      expect(graph?.props.source).toContain('#d03b3b')
      expect(graph?.props.source).toContain('<title>cloudfront (cdn) · warning</title>')
      expect(graph?.props.source).toContain('<title>web-edge-alb to us-east-1c: established, both ways</title>')
      // Two columns when the pane is wide enough.
      expect(pane.find({ type: 'Box', key: 'w-grid' })).toBeDefined()
      expect(pane.all({ type: 'Raster' })).toHaveLength(0)
    }
  }
})

test('a on the selected widget drafts a question about it, and never sends', async () => {
  for (const surface of SURFACES) {
    begin()
    const io = fakeIo(() => WALL)
    wallTab.warm(io as never)
    await settle()
    const pane = await draw(surface, io)
    await pane.press('w-w3-t')
    await (await keysOf(surface, io)).press('wall-ask')
    await pane.press('w-w1-t')
    await (await keysOf(surface, io)).press('wall-next')
    await (await keysOf(surface, io)).press('wall-ask')
    expect(io.filled).toEqual(['Tell me about the Logs · web-edge widget in Landfall 168.', 'Tell me about the Healthy origins widget in Landfall 168.'])
    expect(io.appended).toEqual([])
    // The selected widget's title carries the mark; the key names it.
    const again = await draw(surface, io)
    expect(again.find({ type: 'Button', text: '▸ Healthy origins' })).toBeDefined()
    expect((await keysOf(surface, io)).find({ key: 'wall-ask' })?.props.label).toBe('ask about Healthy origins')
  }
})

test('the wall answers in text where no pane can be placed, and says why when it cannot be read', async () => {
  begin()
  let answer: unknown = WALL
  const io = fakeIo(() => answer)
  const text = await wallTab.text(io as never, '')
  expect(text).toContain('Wall · shared by carol · Landfall 168 · cloudfront-5xx-high · last 6h')
  expect(text).toContain('Healthy origins\n  4/6 ↓ 2\n  6 an hour ago')
  expect(text).toContain('5xx by region\n  us-east-1 · N. Virginia 34% (critical)\n  eu-west-1 0.4%')
  expect(text).toContain('Pool size\n  acme/web-edge · src/pool.ts\n  41  export const POOL = {\n  42    max: 16,\n  43  }\n  https://github.com/acme/web-edge/blob/abc123/src/pool.ts#L41-L43')
  expect(text).toContain('p99 latency\n  No data in this window yet.')
  expect(text).toContain('  5xxErrorRate: last 3.1, peak 6.1\n  marker: web-edge v2.3.1 at 15:29Z\n  threshold 2')
  expect(text).toContain('Topology\n  cloudfront (cdn)  warning\n  └─ web-edge-alb  confirmed  warning\n     └─ us-east-1c  established, both ways  critical')
  expect(text).toContain('  ERR upstream timeout pool=origin-b')
  expect(text).toContain('Not shown here:\n  RDS replica lag: needs a connection you cannot read')

  answer = { ok: false, error: 'Your sign-in expired. Run landfall login.' }
  expect(await wallTab.text(io as never, '')).toBe('Your sign-in expired. Run landfall login.')
})

test('the topology as text, and when the wall has none', async () => {
  begin()
  let answer: unknown = WALL
  const io = fakeIo(() => answer)
  expect(await wallTab.text(io as never, 'topology')).toBe(
    ['Topology', '  cloudfront (cdn)  warning', '  └─ web-edge-alb  confirmed  warning', '     └─ us-east-1c  established, both ways  critical'].join('\n'),
  )
  answer = { ...WALL, widgets: [WALL.widgets[0]] }
  expect(await wallTab.text(io as never, 'topology')).toBe('The wall has no topology yet. Ask your agent or Beacon to map the services this incident touches.')
})

test('/landfall topology selects the first graph once, and says so when the wall has none', async () => {
  begin()
  const io = fakeIo(() => WALL)
  wallTab.warm(io as never)
  await settle()
  const pane = await draw('terminal', io, 'topology')
  expect(pane.find({ type: 'Button', text: '▸ Topology' })).toBeDefined()
  expect(wallTab.graphKey()).toBe('w-w4')
  expect(consoleState.args).toBeNull()
  // The person's own choice wins from then on.
  await pane.press('w-w2-t')
  expect((await draw('terminal', io)).find({ type: 'Button', text: '▸ Healthy origins' })).toBeDefined()

  begin()
  const bare = fakeIo(() => ({ ...WALL, widgets: [WALL.widgets[0]] }))
  wallTab.warm(bare as never)
  await settle()
  const none = await draw('terminal', bare, 'topology')
  expect(none.find({ type: 'Text', text: /^The wall has no topology yet\./ })).toBeDefined()
  expect(wallTab.graphKey()).toBe('')
})

test('a failed read shows its sentence', async () => {
  for (const surface of SURFACES) {
    begin()
    const io = fakeIo(() => ({ ok: false, error: 'This checkout is not reading any room right now.' }))
    wallTab.warm(io as never)
    await settle()
    const pane = await draw(surface, io)
    expect(pane.find({ type: 'Text', text: 'This checkout is not reading any room right now.' })).toBeDefined()
    expect((await keysOf(surface, io)).find({ key: 'wall-ask' })).toBeUndefined()
  }
})

test('before the first read it says so, and outside a room it points at Incidents', async () => {
  begin()
  const io = fakeIo(() => WALL)
  expect((await draw('terminal', io)).find({ type: 'Text', text: 'Press r to read the wall.' })).toBeDefined()
  begin([])
  const none = await draw('terminal', io)
  expect(none.find({ type: 'Text', text: 'Not in a war room yet. Open Incidents above to join one, or open a share link from the room.' })).toBeDefined()
  expect(none.find({ key: 'wall-sel' })).toBeUndefined()
})

test('the wall draws on vscode and mobile too, as vectors', async () => {
  for (const surface of ['vscode', 'mobile'] as const) {
    begin()
    const io = fakeIo(() => WALL)
    wallTab.warm(io as never)
    await settle()
    const pane = await draw(surface, io, null, 48)
    expect(pane.find({ type: 'Text', text: 'Wall · shared by carol' })).toBeDefined()
    expect(pane.all({ type: 'Svg' }).length).toBeGreaterThan(2)
    // Narrow: one column.
    expect(pane.find({ type: 'Box', key: 'w-grid' })).toBeUndefined()
  }
})

test('the wall tab draws six widgets at most and says how many more there are, keys still in reach', async () => {
  const eight = { ok: true, sharedBy: 'carol', windowMs: 21600000, widgets: Array.from({ length: 8 }, (_, i) => ({ id: 'e' + i, type: 'stat', title: 'Stat ' + i, value: String(i) })) }
  for (const surface of SURFACES) {
    begin()
    const io = fakeIo(() => eight)
    wallTab.warm(io as never)
    await settle()
    const pane = await draw(surface, io)
    expect(pane.all({ type: 'Button', text: /^(▸ )?Stat \d$/ })).toHaveLength(6)
    expect(pane.find({ type: 'Text', text: 'Showing 6 of 8 widgets. Open the war room in the browser for the rest.' })).toBeDefined()
    // The keys are the console's row under the body; the wall offers them while it has widgets to walk.
    const keys = wallTab.keys(kitFor(surface), io as never)
    expect(keys.length).toBeGreaterThan(1)
    // n walks the six drawn ones, never a seventh.
    for (let i = 0; i < 6; i++) await new Drawn(keys).press('wall-next')
    const again = await draw(surface, io)
    expect(again.find({ type: 'Button', text: '▸ Stat 0' })).toBeDefined()
  }
  // The text answer is not a pane: it says all eight.
  begin()
  const text = await wallTab.text(fakeIo(() => eight) as never, '')
  expect(text).toContain('Stat 7')
  expect(text).not.toContain('Showing')
})

test('a wall with more widgets than one read says how many there are', async () => {
  // The CLI reads at most 40 widgets (wallWidgetsMax) and names the total.
  const many = {
    ok: true,
    widgets: Array.from({ length: 39 }, (_, i) => ({ id: 's' + i, type: 'stat', title: 'Stat ' + i, value: String(i) })),
    unavailable: [{ id: 'u1', type: 'chart', title: 'Replica lag', reason: 'Sign in to read this widget as yourself: run landfall login.' }],
    totalWidgets: 46,
  }
  for (const surface of SURFACES) {
    begin()
    wallTab.setWallCap(99)
    const io = fakeIo(() => many)
    wallTab.warm(io as never)
    await settle()
    const pane = await draw(surface, io)
    expect(pane.find({ type: 'Text', text: 'Showing 40 of 46 widgets. Open the war room in the browser for the rest.' })).toBeDefined()
  }
  begin()
  const text = await wallTab.text(fakeIo(() => many) as never, '')
  expect(text).toContain('Not shown here:\n  Replica lag: Sign in to read this widget as yourself: run landfall login.\n\nShowing 40 of 46 widgets.')
})

// Beacon's status and remediation cards are logView widgets whose lines carry
// only a message. A line with no level is prose; a markdown heading is bold,
// without its marks.
const PROSE = {
  ok: true,
  widgets: [
    {
      id: 'w-remediation',
      type: 'logView',
      title: 'Remediation: fast fix and durable mitigation',
      lines: [
        { level: '', text: '## FAST FIX  [temporary]' },
        { level: '', text: 'No safe fast-stabilization action found for the current event.' },
        { level: '', text: '' },
        { level: '', text: '## ROOT-CAUSE MITIGATION' },
        { level: 'warn', text: 'Root cause: not yet determined' },
      ],
    },
  ],
  unavailable: [],
}

test('a log line with no level draws as prose, a heading as bold, never DBG', async () => {
  for (const surface of SURFACES) {
    begin()
    const io = fakeIo(() => PROSE)
    wallTab.warm(io as never)
    await settle()
    const pane = await draw(surface, io)
    expect(pane.find({ type: 'Text', text: 'DBG' })).toBeUndefined()
    expect(pane.find({ type: 'Text', text: 'FAST FIX  [temporary]' })?.props.bold).toBe(true)
    expect(pane.find({ type: 'Text', text: 'ROOT-CAUSE MITIGATION' })?.props.bold).toBe(true)
    const plain = pane.find({ type: 'Text', text: /^No safe fast-stabilization action/ })
    expect(plain).toBeDefined()
    expect(plain?.props.bold).toBeFalsy()
    expect(pane.find({ type: 'Text', text: 'WRN' })).toBeDefined()
  }
  begin()
  const text = await wallTab.text(fakeIo(() => PROSE) as never, '')
  expect(text).toContain('  FAST FIX  [temporary]\n  No safe fast-stabilization action found for the current event.')
  expect(text).toContain('  WRN Root cause: not yet determined')
  expect(text).not.toContain('DBG')
  expect(text).not.toContain('##')
})

// A geo place whose label already names it is said once (the server's own label for a critical place).
test('a geo place whose label already names it is said once', async () => {
  const geo = {
    ok: true,
    widgets: [
      {
        id: 'g1',
        type: 'geo',
        title: '5xx Error Rate by Region',
        points: [
          { place: 'eu-west-1', label: 'eu-west-1 · 13.1%', value: 13.1, unit: '%', tone: 'critical' },
          { place: 'us-east-1', value: 0.28, unit: '%', tone: 'good' },
        ],
      },
    ],
    unavailable: [],
  }
  for (const surface of SURFACES) {
    begin()
    const io = fakeIo(() => geo)
    wallTab.warm(io as never)
    await settle()
    const pane = await draw(surface, io)
    expect(pane.find({ type: 'Text', text: 'eu-west-1 · 13.1%' })).toBeDefined()
    expect(pane.find({ type: 'Text', text: /eu-west-1 · eu-west-1/ })).toBeUndefined()
    expect(pane.find({ type: 'Text', text: 'us-east-1' })).toBeDefined()
    expect(pane.find({ type: 'Text', text: '● 0.28%' })?.props.color).toBe('#0ca30c')
  }
})

// ---------------------------------------------------------------------------
// The dashboard selector (spec §4.2) and a person's dashboard (`--person`).

const DAVE = { name: 'dave', you: true, here: true, humanActorId: 'h-dave', agents: [{ tool: 'Claude Code', label: 'dave-cc', here: true }] }
const ALICE = { name: 'alice', here: true, humanActorId: 'h-alice', agents: [{ tool: 'Codex', label: 'alice-codex', here: true, doing: 'reading origin pool metrics' }] }
// bob's row carries no humanActorId yet: the wall's people[] names him by displayName.
const BOB = { name: 'bob', here: true, agents: [{ tool: 'Claude Code', label: 'bob-cc', here: true }] }
const CAROL = { name: 'carol', here: true, browser: true, humanActorId: 'h-carol' }
const ERIN = { name: 'erin', here: false, humanActorId: 'h-erin' }
const WITH_PEOPLE = { ...ROOM, status: { ...ROOM.status, people: [DAVE, ERIN, CAROL, BOB, ALICE] } }

const WALL_PEOPLE = [
  { humanActorId: 'h-alice', displayName: 'alice', edgeAgentLabel: 'Codex', kind: 'human', widgets: 2, trail: 3, artifacts: 1, latestSeq: 220 },
  { humanActorId: 'h-bob', displayName: 'Bob', edgeAgentLabel: 'Claude Code', kind: 'human', widgets: 0, trail: 1, artifacts: 0, latestSeq: 210 },
  { humanActorId: 'h-carol', displayName: 'carol', edgeAgentLabel: '', kind: 'human', widgets: 1, trail: 0, artifacts: 0, latestSeq: 200 },
  { humanActorId: 'h-dave', displayName: 'dave', edgeAgentLabel: 'Claude Code', kind: 'human', widgets: 2, trail: 0, artifacts: 0, latestSeq: 190 },
  { humanActorId: 'h-gone', displayName: 'frank', edgeAgentLabel: '', kind: 'human', widgets: 1, trail: 0, artifacts: 0, latestSeq: 150 },
]
const WALL2 = { ...WALL, widgets: [{ id: 'w2', type: 'stat', title: 'Healthy origins', value: '3', unit: '/6', tone: 'serious' }] }
const SHARED = { ...WALL, people: WALL_PEOPLE }

const SNAP_AT = '2026-10-08T15:11:00Z' // 9 minutes before T0
const ALICE_WIDGETS = [
  { id: 'edge-widget-201', type: 'stat', title: 'Origin pool saturation', value: '94', unit: '%', tone: 'critical', spark: [40, 55, 70, 90, 94], capturedAt: SNAP_AT, seq: 201 },
  { id: 'edge-widget-205', type: 'geo', title: 'Pool connections by zone', points: [{ place: 'us-east-1a', value: 98, unit: '%', tone: 'critical' }], capturedAt: '2026-10-08T15:13:00Z', seq: 205 },
]
const alicesDashboard = {
  ok: true,
  person: { humanActorId: 'h-alice', displayName: 'alice', edgeAgentLabel: 'Codex', kind: 'human', you: false },
  widgets: ALICE_WIDGETS,
  unavailable: [],
  trail: [],
  artifacts: [],
  people: WALL_PEOPLE,
  arranged: false,
}

function personAnswers(overrides: Record<string, unknown> = {}) {
  return (argv: string[]) => {
    const at = argv.indexOf('--person')
    if (at < 0) return SHARED
    const who = argv[at + 1]
    if (who in overrides) return overrides[who]
    if (who === 'h-alice') return alicesDashboard
    if (who === 'me') return { ...alicesDashboard, person: { humanActorId: 'h-dave', displayName: 'dave', you: true }, widgets: [ALICE_WIDGETS[0], { ...ALICE_WIDGETS[1], id: 'e-3', title: 'Second' }] }
    return { ...alicesDashboard, person: { humanActorId: who }, widgets: [] }
  }
}

test('the selector is the chip idiom: Shared wall, each person here first, mine last, counts after the names', async () => {
  for (const surface of SURFACES) {
    begin([WITH_PEOPLE])
    const io = fakeIo(personAnswers())
    wallTab.warm(io as never)
    await settle()
    const pane = await draw(surface, io)
    const sel = pane.find({ key: 'wall-sel' })!
    const chips = new Drawn(sel).all({ type: 'Button' })
    // here first (alice, bob, carol by name), then away (erin), then people who left, then mine; you are `mine`.
    expect(chips.map((c) => c.props.label.trim())).toEqual(['▸ Shared wall', 'alice 2', 'bob', 'carol 1', 'erin', 'frank 1', 'mine 2'])
    // No letters, no separators, no fill, no inverse: nothing in the row looks like the switcher.
    expect(chips.every((c) => c.props.hotkey === undefined && c.props.variant === undefined)).toBe(true)
    expect(chips.slice(1).every((c) => c.props.dimColor === true)).toBe(true)
    expect(chips[0].props.dimColor).toBeUndefined()
    const all: any[] = []
    for (const n of [sel]) new Drawn(n).all({}).forEach((x) => all.push(x))
    expect(all.some((n) => n.props.inverse || n.props.backgroundColor || n.props.hotkey)).toBe(false)
    expect(new Drawn(sel).find({ type: 'Text', text: '│' })).toBeUndefined()
    // Home's tiles and this tab read the same shared answer.
    expect(wallTab.sharedAnswer()?.sharedBy).toBe('carol')
  }
})

test('pressing a person reads their dashboard, draws it as snapshots, and the shared wall stays read', async () => {
  for (const surface of SURFACES) {
    begin([WITH_PEOPLE])
    const io = fakeIo(personAnswers())
    wallTab.warm(io as never)
    await settle()
    let pane = await draw(surface, io)
    await pane.press('sel-h-alice')
    await settle()
    expect(io.runs.at(-1)).toEqual(['wall', '--host', 'claude-code', '--room', 'k168', '--person', 'h-alice'])
    pane = await draw(surface, io)
    expect(pane.find({ type: 'Button', text: '▸ alice 2' })).toBeDefined()
    expect(pane.find({ type: 'Text', text: "alice's dashboard" })?.props.bold).toBe(true)
    expect(pane.find({ type: 'Text', text: ' · 2 widgets · snapshots · newest 7m ago' })).toBeDefined()
    expect(pane.find({ type: 'Text', text: 'snapshot 9m ago' })).toBeDefined()
    expect(pane.find({ type: 'Text', text: 'snapshot 7m ago' })).toBeDefined()
    expect(pane.find({ type: 'Button', text: '▸ Origin pool saturation' })).toBeDefined()
    expect(pane.find({ type: 'Text', text: 'Wall · shared by carol' })).toBeUndefined()
    expect(pane.find({ type: 'Text', text: '● 98%' })?.props.color).toBe('#d03b3b')
    // The keys ask about a widget on their dashboard, and d goes on to the next dashboard.
    const keys = await keysOf(surface as 'terminal' | 'desktop', io)
    await keys.press('wall-ask')
    expect(io.filled.at(-1)).toBe("Tell me about the Origin pool saturation widget on alice's dashboard in Landfall 168.")
    await keys.press('wall-dash')
    await settle()
    const next = await draw(surface, io)
    expect(next.find({ type: 'Button', text: '▸ bob' })).toBeDefined()
    // bob's id came from the wall's people[] by name, and his dashboard is empty.
    expect(io.runs.at(-1)).toEqual(['wall', '--host', 'claude-code', '--room', 'k168', '--person', 'h-bob'])
    expect(next.find({ type: 'Text', text: 'bob has not shared a dashboard yet.' })).toBeDefined()
    // back to the shared wall by its chip.
    await next.press('sel-shared')
    expect((await draw(surface, io)).find({ type: 'Text', text: 'Wall · shared by carol' })).toBeDefined()
  }
})

test('mine reads --person me; its empty state tells you how to fill it; erin who shared nothing needs no read', async () => {
  begin([WITH_PEOPLE])
  const io = fakeIo(personAnswers({ me: { ok: true, person: { humanActorId: 'h-dave', you: true }, widgets: [], unavailable: [], trail: [], artifacts: [], people: WALL_PEOPLE } }))
  wallTab.warm(io as never)
  await settle()
  let pane = await draw('terminal', io)
  await pane.press('sel-mine')
  await settle()
  expect(io.runs.at(-1)).toEqual(['wall', '--host', 'claude-code', '--room', 'k168', '--person', 'me'])
  pane = await draw('terminal', io)
  expect(pane.find({ type: 'Text', text: 'You have not shared a dashboard yet. Ask your agent to share what it reads as a widget.' })).toBeDefined()
  const before = io.runs.length
  await pane.press('sel-h-erin')
  await settle()
  pane = await draw('terminal', io)
  expect(pane.find({ type: 'Text', text: 'erin has not shared a dashboard yet.' })).toBeDefined()
  expect(io.runs.length).toBe(before + 1)
})

test('a person with their own widgets: mine has its count and heading', async () => {
  begin([WITH_PEOPLE])
  const io = fakeIo(personAnswers())
  wallTab.warm(io as never)
  await settle()
  await (await draw('terminal', io)).press('sel-mine')
  await settle()
  const pane = await draw('terminal', io)
  expect(pane.find({ type: 'Text', text: 'Your dashboard' })?.props.bold).toBe(true)
  expect(pane.find({ type: 'Text', text: ' · 2 widgets · snapshots' })).toBeDefined()
})

test('/landfall wall <name> and wall mine select that dashboard once the person is known', async () => {
  begin([WITH_PEOPLE])
  const io = fakeIo(personAnswers())
  openConsoleOn('wall', 'Alice')
  const first = await draw('terminal', io, 'Alice')
  // Chosen from the watch stream's own names, before anything was read; the args are spent.
  expect(consoleState.args).toBeNull()
  expect(first.find({ type: 'Button', text: '▸ alice' })).toBeDefined()
  await settle()
  expect(io.runs.map((r) => r.join(' '))).toContain('wall --host claude-code --room k168 --person h-alice')
  begin([WITH_PEOPLE])
  const two = fakeIo(personAnswers())
  const mine = await draw('terminal', two, 'mine')
  expect(mine.find({ type: 'Button', text: '▸ mine' })).toBeDefined()
  // A name nobody here has leaves the wall where it was.
  begin([WITH_PEOPLE])
  const three = fakeIo(personAnswers())
  const none = await draw('terminal', three, 'zed')
  expect(none.find({ type: 'Button', text: '▸ Shared wall' })).toBeDefined()
})

test('a person is the same person once the CLI says who they are', async () => {
  begin([{ ...ROOM, status: { ...ROOM.status, people: [DAVE, BOB] } }])
  const io = fakeIo(personAnswers())
  const pane = await draw('terminal', io, 'bob')
  expect(pane.find({ type: 'Button', text: '▸ bob' })).toBeDefined()
  // No wall answer yet, so no id and nothing to read.
  expect(io.runs).toEqual([])
  wallTab.warm(io as never)
  await settle()
  const after = await draw('terminal', io)
  expect(after.find({ type: 'Button', text: '▸ bob' })).toBeDefined()
  await settle()
  expect(io.runs.at(-1)).toEqual(['wall', '--host', 'claude-code', '--room', 'k168', '--person', 'h-bob'])
})

test('the selector clips names, then drops counts, then wraps', async () => {
  const long = (name: string, id: string) => ({ name, here: true, humanActorId: id })
  const people = [DAVE, long('alexandria-the-great', 'h-1'), long('bartholomew-simpson', 'h-2'), long('christopher-robin', 'h-3'), long('dorothea-lange', 'h-4')]
  const rows = people.slice(1).map((p, i) => ({ humanActorId: p.humanActorId, displayName: p.name, widgets: i + 1 }))
  begin([{ ...ROOM, status: { ...ROOM.status, people } }])
  const io = fakeIo(() => ({ ...SHARED, people: rows }))
  wallTab.warm(io as never)
  await settle()
  const pane = await draw('terminal', io, null, 60)
  const labels = new Drawn(pane.find({ key: 'wall-sel' })).all({ type: 'Button' }).map((c) => c.props.label.trim())
  expect(labels.some((l) => l.includes('…'))).toBe(true)
  expect(labels.every((l) => l.replace(/^▸ /, '').replace(/ \d+$/, '').length <= 8)).toBe(true)
  const wide = await draw('terminal', io, null, 200)
  expect(new Drawn(wide.find({ key: 'wall-sel' })).all({ type: 'Button' }).map((c) => c.props.label.trim())).toContain('alexandria-the-great 1')
})

// ---------------------------------------------------------------------------
// The live read (live.md FR-L2, FR-L3), on the session's own clock.

test('an open wall reads again every 15 s, says how old it is, and stops once the console closes', async () => {
  begin()
  const io = fakeIo(() => WALL)
  wallTab.warm(io as never)
  await settle()
  expect(io.count('wall')).toBe(1)
  const foot = () => {
    const f = wallTab.footer(kitFor('terminal'), io.clock.t)
    return f ? (f as any).children.join('') : null
  }
  expect(foot()).toBe('live · updated 0s ago')

  io.clock.t += 10000
  await wallTab.tick(io as never, io.clock.t)
  await settle()
  expect(io.count('wall')).toBe(1)
  expect(foot()).toBe('live · updated 10s ago')

  io.clock.t += 5000
  await wallTab.tick(io as never, io.clock.t)
  await settle()
  expect(io.count('wall')).toBe(2)
  expect(foot()).toBe('live · updated 0s ago')

  // Closed: no more reads, however long it has been.
  consoleState.open = false
  io.clock.t += 120000
  await wallTab.tick(io as never, io.clock.t)
  await settle()
  expect(io.count('wall')).toBe(2)
})

test('a widget event reads the open wall at once, and a failed read keeps the wall, marked stale', async () => {
  begin()
  let answer: unknown = WALL
  const io = fakeIo(() => answer)
  wallTab.warm(io as never)
  await settle()
  const snap = (over: Record<string, unknown>) => ({ line: '', rooms: [{ ...ROOM, ...over }] })
  const tick = async (ms: number) => {
    io.clock.t += ms
    setRooms([{ ...ROOM, ...((tick as any).over ?? {}) }])
    await wallTab.tick(io as never, io.clock.t)
    await settle()
  }
  // Another room event that shapes no widget: nothing read.
  wallTab.onSnapshot(io as never, snap({ maxSeq: 234 }) as never, null as never)
  await settle()
  expect(io.count('wall')).toBe(1)
  // A widget event: read within the same moment, no tick needed.
  answer = WALL2
  setRooms([{ ...ROOM, maxSeq: 235, widgetSeq: 235 }])
  wallTab.onSnapshot(io as never, snap({ maxSeq: 235, widgetSeq: 235 }) as never, null as never)
  await settle()
  expect(io.count('wall')).toBe(2)
  expect((await draw('terminal', io)).find({ type: 'Text', text: '3' })).toBeDefined()
  // The next read, 15 s on, fails: the wall stays, the line says it is stale and why.
  answer = { ok: false, error: 'Your sign-in expired. Run landfall login.' }
  ;(tick as any).over = { maxSeq: 235, widgetSeq: 235 }
  await tick(17000)
  expect(io.count('wall')).toBe(3)
  expect((await draw('terminal', io)).find({ type: 'Text', text: '3' })).toBeDefined()
  expect(((wallTab.footer(kitFor('terminal'), io.clock.t) as any).children as string[]).join('')).toBe('stale · updated 17s ago · Your sign-in expired. Run landfall login.')
  // A good read clears it.
  answer = WALL
  await tick(15000)
  expect(((wallTab.footer(kitFor('terminal'), io.clock.t) as any).children as string[]).join('')).toBe('live · updated 0s ago')
  expect((await draw('terminal', io)).find({ type: 'Text', text: '4' })).toBeDefined()
})

test('a selected person reads on the same cadence and on any new room event; shared and person are read independently', async () => {
  begin([WITH_PEOPLE])
  const io = fakeIo(personAnswers())
  wallTab.warm(io as never)
  await settle()
  await (await draw('terminal', io)).press('sel-h-alice')
  await settle()
  const person = () => io.runs.filter((r) => r.includes('--person')).length
  expect(person()).toBe(1)
  // A new event (their snapshots are events): read at once.
  setRooms([{ ...WITH_PEOPLE, maxSeq: 240 }])
  wallTab.onSnapshot(io as never, { line: '', rooms: [{ ...WITH_PEOPLE, maxSeq: 240 }] } as never, null as never)
  await settle()
  expect(person()).toBe(2)
  // Not on another tab.
  consoleState.tab = 'home'
  io.clock.t += 16000
  await wallTab.tick(io as never, io.clock.t)
  await settle()
  expect(person()).toBe(2)
  consoleState.tab = 'wall'
  io.clock.t += 16000
  await wallTab.tick(io as never, io.clock.t)
  await settle()
  expect(person()).toBe(3)
})

test('while the room reconnects the wall says so, and live again it clears', async () => {
  begin([{ ...ROOM, connection: 'disconnected' }])
  const io = fakeIo(() => WALL)
  wallTab.warm(io as never)
  await settle()
  const text = (f: any) => (f ? (f.children as string[]).join('') : null)
  expect(text(wallTab.footer(kitFor('terminal'), io.clock.t))).toBe('Reconnecting to the room…')
  setRooms([ROOM])
  expect(text(wallTab.footer(kitFor('terminal'), io.clock.t))).toBe('live · updated 0s ago')
})

test('the wall reads the room this session is in, named with --room', async () => {
  const OLD = { ...ROOM, roomKey: 'k166', displayId: 'Landfall 166', maxSeq: 10, agent: { inRoom: false }, status: { ...ROOM.status, status: 'resolved' } }
  begin([OLD, { ...ROOM, agent: { inRoom: true } }])
  const io = fakeIo(() => WALL)
  wallTab.warm(io as never)
  await settle()
  const argv = io.runs.find((a) => a[0] === 'wall')!
  expect(argv[argv.indexOf('--room') + 1]).toBe('k168')
  expect(io.runs.some((a) => a.includes('k166'))).toBe(false)
})

test('a new widget is told once per 10 s, and the wall hint offers w for a minute', async () => {
  begin()
  const io = fakeIo(() => WALL)
  const feed = async (over: Record<string, unknown>) => {
    const rooms = [{ ...ROOM, ...over }]
    setRooms(rooms)
    wallTab.onSnapshot(io as never, { line: '', rooms } as never, null as never)
    await settle()
  }
  const NW = (seq: number, title: string, by?: string) => ({ widgetSeq: seq, newestWidget: { seq, title, type: 'chart', by } })
  await feed(NW(231, '5xx by target group', 'bob'))
  // The first sight of the room is not news.
  expect(io.toasts).toEqual([])
  await feed(NW(240, 'p99 latency', 'dana'))
  expect(io.toasts).toEqual(['New on the wall: p99 latency · by dana'])
  // A second within the gap waits for it.
  await feed(NW(241, 'Replica lag'))
  expect(io.toasts).toHaveLength(1)
  io.clock.t += 5000
  await wallTab.tick(io as never, io.clock.t)
  expect(io.toasts).toHaveLength(1)
  io.clock.t += 5000
  await wallTab.tick(io as never, io.clock.t)
  expect(io.toasts).toEqual(['New on the wall: p99 latency · by dana', 'New on the wall: Replica lag'])
  expect(wallTab.wallHint(io.clock.t)).toEqual({ title: 'Replica lag' })
  // w opens the console on the Wall, and spends the hint.
  await wallTab.openWall(io as never)
  expect(io.opened).toEqual(['landfall'])
  // The person pressed w, so the console takes the keyboard.
  expect((io.openedWith[0] as any).focus).toBe(true)
  expect(consoleState.tab).toBe('wall')
  expect(wallTab.wallHint(io.clock.t)).toBeNull()
})
