import { expect, test } from 'claude-code/testing'
import { NOW, chromed, buttonOf, fakeIo, fakeKit, hookTab, nodes, paneProps, setRoom, textsOf, watchRoom } from './_tabd'
import { consoleState } from '../hooks/core.js'
import * as lbMod from '../hooks/components/lb.js'

// `landfall lb` as the Go CLI really prints it (internal/cli/lb.go, its
// lb_test.go fixture): 5xx is per TARGET GROUP (`fiveXxBy: "targetGroup"`),
// each group carrying per-minute `fiveXx` and `requests` (a missing minute is
// 0), `fiveXxPct` and `healthyHostCount` (a missing minute is null); targets
// carry port, zone, state, reason and detail, sorted by zone then id. One ALB,
// two groups, 36 minutes; web-edge-tg went bad at minute 15 and had no
// requests at minutes 3 and 4; the canary group served nothing until minute 33.
const START = 1791478200000 // 2026-10-08T16:50:00Z
const N = 36
const REQ = Array.from({ length: N }, (_, m) => (m === 3 || m === 4 ? 0 : 500))
const PCT = Array.from({ length: N }, (_, m) => (m === 3 || m === 4 ? null : m >= 18 ? 34 : m >= 15 ? 12 : 0.2))
const ERRS = PCT.map((p) => (p == null ? 0 : (p * 500) / 100))
const HHC = Array.from({ length: N }, (_, m) => (m === 3 ? null : m >= 17 ? 4 : m >= 15 ? 5 : 6))
const CANARY_REQ = Array.from({ length: N }, (_, m) => (m >= 33 ? 20 : 0))
const TARGETS = [
  { id: 'i-0a3f', zone: 'us-east-1a', state: 'healthy', reason: '', port: 8080 },
  { id: 'i-0b71', zone: 'us-east-1a', state: 'healthy', reason: '', port: 8080 },
  { id: 'i-04c2', zone: 'us-east-1b', state: 'healthy', reason: '', port: 8080 },
  { id: 'i-09de', zone: 'us-east-1b', state: 'healthy', reason: '', port: 8080 },
  { id: 'i-02f9', zone: 'us-east-1c', state: 'unhealthy', reason: 'Target.Timeout', port: 8080, detail: 'Request timed out' },
  { id: 'i-07aa', zone: 'us-east-1c', state: 'unhealthy', reason: 'Target.Timeout', port: 8080, detail: 'Request timed out' },
]
const QUERY = {
  source: 'cloudwatch',
  operation: 'getMetricData',
  params: {
    MetricDataQueries: [
      {
        Id: 'e0',
        Label: 'web-edge-tg',
        MetricStat: {
          Metric: {
            Namespace: 'AWS/ApplicationELB',
            MetricName: 'HTTPCode_Target_5XX_Count',
            Dimensions: [
              { Name: 'TargetGroup', Value: 'targetgroup/web-edge-tg/73e2d6bc24d8a067' },
              { Name: 'LoadBalancer', Value: 'app/web-edge-alb/50dc6c495c0c9188' },
            ],
          },
          Period: 60,
          Stat: 'Sum',
        },
        ReturnData: true,
      },
    ],
    StartTime: '2026-10-08T16:50:00Z',
    EndTime: '2026-10-08T17:26:00Z',
    ScanBy: 'TimestampAscending',
  },
  title: '5xx per target group · web-edge-alb',
}
const LB = {
  ok: true,
  account: '123456789012',
  region: 'us-east-1',
  minutes: N,
  fiveXxBy: 'targetGroup',
  loadBalancers: [
    {
      name: 'web-edge-alb',
      arn: 'arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/web-edge-alb/50dc6c495c0c9188',
      scheme: 'internet-facing',
      state: 'active',
      healthy: 5,
      total: 7,
      targetGroups: [
        {
          name: 'web-edge-tg',
          arn: 'arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/web-edge-tg/73e2d6bc24d8a067',
          protocol: 'HTTP',
          port: 8080,
          healthCheck: '/healthz',
          targets: TARGETS,
          healthyHostCount: HHC,
          fiveXx: ERRS,
          requests: REQ,
          fiveXxPct: PCT,
        },
        {
          name: 'web-edge-canary-tg',
          arn: 'arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/web-edge-canary-tg/1111',
          protocol: 'HTTP',
          port: 8081,
          targets: [{ id: '10.0.4.17', zone: 'us-east-1a', state: 'healthy', reason: '', port: 9000 }],
          healthyHostCount: Array.from({ length: N }, () => 1),
          fiveXx: Array.from({ length: N }, () => 0),
          requests: CANARY_REQ,
          fiveXxPct: CANARY_REQ.map((r) => (r ? 0 : null)),
        },
      ],
      healthyHostCount: HHC.map((v) => (v == null ? null : v + 1)),
      minuteStartMs: START,
      zones: ['us-east-1a', 'us-east-1b', 'us-east-1c'],
    },
  ],
  query: QUERY,
}
// Health and metrics that could not be read: still an answer, saying which part is missing.
const PARTIAL = {
  ok: true,
  account: '123456789012',
  region: 'us-east-1',
  minutes: 60,
  fiveXxBy: 'targetGroup',
  loadBalancers: [
    {
      name: 'web-edge-alb',
      arn: LB.loadBalancers[0].arn,
      scheme: 'internet-facing',
      state: 'active',
      healthy: 0,
      total: 0,
      targetGroups: [
        {
          name: 'web-edge-tg',
          arn: LB.loadBalancers[0].targetGroups[0].arn,
          protocol: 'HTTP',
          port: 8080,
          healthCheck: '/healthz',
          healthUnavailable: "The room's AWS connection could not read load balancers. Try again shortly.",
          targets: [],
          healthyHostCount: [],
          fiveXx: [],
          requests: [],
          fiveXxPct: Array.from({ length: 60 }, () => null),
        },
      ],
      healthyHostCount: Array.from({ length: 60 }, () => null),
      minuteStartMs: START,
      zones: ['us-east-1a', 'us-east-1c'],
    },
  ],
  query: QUERY,
  truncated: true,
  metricsUnavailable: "The room's AWS connection is not allowed to read load balancers.",
}
const NO_ALB = '{"ok":false,"error":"The room\'s AWS connection lists no Application Load Balancers in this region."}'

// cell reads one Raster cell's code point: [codePoint, fg, bg] u32 words, base64.
function cell(raster: any, row: number, col: number) {
  const bin = atob(raster.props.cells)
  const at = (row * raster.props.columns + col) * 12
  return bin.charCodeAt(at) | (bin.charCodeAt(at + 1) << 8) | (bin.charCodeAt(at + 2) << 16)
}


const NEW_ROOM = () => {
  const room = watchRoom()
  setRoom([room])
  return room
}

// readBoard answers `landfall lb` with `answer` and reads it, as the console's warm does.
async function readBoard(answer: unknown, io = fakeIo({ run: async () => answer })) {
  await lbMod.warm(io.io)
  return io
}

for (const surface of ['terminal', 'desktop'] as const) {
  test(`${surface}: the engine draws each target group with its targets by zone and 5xx per target group per minute`, async ($, on) => {
    NEW_ROOM()
    await readBoard(LB)
    hookTab(on, 'd-lb', (k, io, now, args) => chromed(lbMod, k, io, now, args))
    const pane = await $.ui.mount({ ...paneProps('d-lb', { viewport: { columns: 140, rows: 50 } }), surface } as never)
    expect(await pane.find({ type: 'Text', text: 'web-edge-alb' })).toBeDefined()
    expect((await pane.find({ type: 'Text', text: '● healthy 5 of 7' }))?.props.color).toBe('#fab219')
    expect(await pane.find({ type: 'Text', text: 'us-east-1 · internet-facing' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: 'Zones: us-east-1a, us-east-1b, us-east-1c' })).toBeDefined()
    // The short heading, and the group's latest 5xx share as a label right after it.
    if (surface === 'terminal') expect(await pane.find({ type: 'Text', text: 'web-edge-tg · HTTP 8080 · /healthz' })).toBeDefined()
    else {
      // Desktop: the path is the useful part, so it has its own dim line and is never clipped.
      expect(await pane.find({ type: 'Text', text: 'web-edge-tg · HTTP 8080' })).toBeDefined()
      expect((await pane.find({ type: 'Text', text: 'health check /healthz' }))?.props.dimColor).toBe(true)
    }
    expect(await pane.find({ type: 'Text', text: 'web-edge-canary-tg · HTTP 8081' })).toBeDefined()
    expect((await pane.find({ type: 'Text', text: '● 5xx 34%' }))?.props.color).toBe('#d03b3b')
    expect((await pane.find({ type: 'Text', text: '● 5xx 0%' }))?.props.color).toBe('#0ca30c')
    // Targets by zone: the square in its health tone, the id plain.
    for (const zone of ['us-east-1a', 'us-east-1b', 'us-east-1c']) expect((await pane.findAll({ type: 'Text', text: zone })).length).toBeGreaterThan(0)
    const squares = (await pane.findAll({ type: 'Text', text: '■' })).map((x: any) => x.props.color)
    // The desktop legend draws its swatches as squares too: one more red, one more green.
    const legend = surface === 'terminal' ? 0 : 1
    expect(squares.filter((c: string) => c === '#d03b3b')).toHaveLength(2 + legend)
    expect(squares.filter((c: string) => c === '#0ca30c')).toHaveLength(5 + legend)
    expect((await pane.find({ type: 'Text', text: 'i-07aa' }))?.props.color).toBeUndefined()
    expect(await pane.find({ type: 'Text', text: '10.0.4.17:9000' })).toBeDefined()
    // The legend: swatches carry the tone, the words are dim, quiet is neutral.
    expect((await pane.find({ type: 'Text', text: surface === 'terminal' ? '▪' : '■', props: { color: '#898781' } as any }))).toBeDefined()
    expect((await pane.find({ type: 'Text', text: surface === 'terminal' ? 'under 1%' : '<1%' }))?.props.dimColor).toBe(true)
    expect((await pane.find({ type: 'Text', text: surface === 'terminal' ? '█' : '■', props: { color: '#d03b3b' } as any }))).toBeDefined()
    expect((await pane.find({ type: 'Text', text: surface === 'terminal' ? 'over 20%' : '>20%' }))?.props.dimColor).toBe(true)
    expect(await pane.find({ type: 'Text', text: 'no requests' })).toBeDefined()
    expect((await pane.find({ type: 'Text', text: '5xx per target group, last 36 minutes' }))?.props.dimColor).toBe(true)
    expect(await pane.find({ type: 'Text', text: surface === 'terminal' ? 'healthy hosts' : 'Healthy hosts' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: '5 of 7' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: /^live · updated 0s ago/ })).toBeDefined()

    if (surface === 'terminal') {
      // One Raster: rows are target groups, columns minutes.
      const heat = await pane.find({ type: 'Raster', key: 'heat' })
      expect(heat?.props.rows).toBe(2)
      expect(heat?.props.columns).toBe(36)
      expect(cell(heat, 0, 0)).toBe(0x25aa) // 0.2%: quiet
      expect(cell(heat, 0, 3)).toBe(0x00b7) // no requests: no data
      expect(cell(heat, 0, 20)).toBe(0x2588) // 34%
      expect(cell(heat, 1, 0)).toBe(0x00b7) // the canary served nothing yet
      expect(cell(heat, 1, 35)).toBe(0x25aa)
      expect(await pane.find({ type: 'Text', text: 'web-edge-tg' })).toBeDefined()
      expect((await pane.find({ type: 'Text', text: '● 34%' }))?.props.color).toBe('#d03b3b')
      expect(await pane.find({ type: 'Text', text: /^ +16:50 +now$/ })).toBeDefined()
      expect(await pane.find({ type: 'Text', text: '  i-07aa: Request timed out' })).toBeDefined()
      // The healthy-hosts sparkline is exactly as wide as a heat row.
      expect((await pane.find({ type: 'Raster', key: 'lb0-hhc' }))?.props.columns).toBe(36)
      expect(await pane.findAll({ type: 'Svg' })).toHaveLength(0)
    } else {
      // One Svg row per target group with a hover title per cell, each followed by its latest share.
      const svgs = await pane.findAll({ type: 'Svg' })
      const heats = svgs.filter((x: any) => String(x.props.alt).startsWith('5xx per minute'))
      expect(heats.map((x: any) => x.props.alt)).toEqual(['5xx per minute, web-edge-tg, 36 minutes', '5xx per minute, web-edge-canary-tg, 36 minutes'])
      expect(heats[0].props.isInteractive).toBe(true)
      expect(heats[0].props.source).toContain('<title>web-edge-tg · 17:10Z · 5xx 34% (170 of 500 requests)</title>')
      expect(heats[0].props.source).toContain('<title>web-edge-tg · 16:53Z · no requests</title>')
      expect(heats[0].props.source).toContain('stroke-dasharray="2 2"')
      expect((await pane.find({ type: 'Text', text: '● 34%' }))?.props).toBeDefined()
      const spark = svgs.find((x: any) => x.props.alt === 'healthy host count')
      expect(spark).toBeDefined()
      // Exactly as wide as a heat row, 44 tall.
      expect(spark.props.width).toBe(heats[0].props.width)
      expect(spark.props.height).toBe(44)
      expect(await pane.find({ type: 'Text', text: '  i-07aa: Request timed out' })).toBeDefined()
      expect(await pane.findAll({ type: 'Raster' })).toHaveLength(0)
    }
  })

  test(`${surface}: s shares the board through the chart path with the CLI's title, a asks about the worst zone; both are keys`, async () => {
    const room = NEW_ROOM()
    const k = fakeKit(surface)
    const f = fakeIo({
      run: async (args: string[]) => {
        f.log.runs.push(args)
        return args[0] === 'chart' ? { ok: true, title: '5xx per target group · web-edge-alb' } : LB
      },
    })
    await lbMod.warm(f.io)
    f.log.runs.length = 0
    const keys = lbMod.keys(k, f.io, NOW, null)
    expect(keys.map((b: any) => b.props.hotkey)).toEqual(['s', 'a'])
    expect(keys[0].props.label).toBe(surface === 'terminal' ? 'share as widget' : 'Share as widget')
    expect(keys[0].props.variant).toBe(surface === 'terminal' ? undefined : 'primary')
    expect(keys[1].props.label).toBe((surface === 'terminal' ? 'ask about ' : 'Ask about ') + 'us-east-1c')
    await keys[0].props.onPress()
    expect(f.log.runs[0].slice(0, 4)).toEqual(['chart', '--host', 'claude-code', '--room'])
    expect(f.log.runs[0][4]).toBe(room.roomKey)
    expect(JSON.parse(f.log.runs[0][6])).toEqual(QUERY)
    expect(f.log.toasts).toEqual(['Chart added to your dashboard in the room: 5xx per target group · web-edge-alb'])
    await lbMod.keys(k, f.io, NOW, null)[1].props.onPress()
    expect(f.log.filled).toEqual([{ text: 'Why are targets in us-east-1c behind web-edge-alb unhealthy or returning 5xx?', mode: undefined }])
  })
}

test('a refused share says why, within 80 characters however long the sentence', async () => {
  NEW_ROOM()
  const f = fakeIo({ run: async (args: string[]) => (args[0] === 'chart' ? { ok: false, error: 'This checkout is not reading any room right now. '.repeat(3) } : LB) })
  await lbMod.warm(f.io)
  await lbMod.keys(fakeKit('terminal'), f.io, NOW, null)[0].props.onPress()
  expect(f.log.toasts).toHaveLength(1)
  expect(f.log.toasts[0].startsWith('Chart not added: This checkout is not reading any room right now.')).toBe(true)
  expect(f.log.toasts[0].length).toBeLessThanOrEqual(80)
  const short = fakeIo({ run: async (args: string[]) => (args[0] === 'chart' ? { ok: false, error: 'This checkout is not reading any room right now.' } : LB) })
  await lbMod.warm(short.io)
  await lbMod.keys(fakeKit('terminal'), short.io, NOW, null)[0].props.onPress()
  expect(short.log.toasts).toEqual(['Chart not added: This checkout is not reading any room right now.'])
})

test('the keys are none while the board cannot be read', async ($, on) => {
  NEW_ROOM()
  await readBoard({ ok: false, error: "The room's AWS connection lists no Application Load Balancers in this region." })
  const k = fakeKit('terminal')
  expect(lbMod.keys(k, fakeIo().io, NOW, null)).toEqual([])
  hookTab(on, 'd-lb-err', (kk, io, now, args) => chromed(lbMod, kk, io, now, args))
  for (const surface of ['terminal', 'desktop'] as const) {
    const pane = await $.ui.mount({ ...paneProps('d-lb-err'), surface } as never)
    expect(await pane.find({ type: 'Text', text: "The room's AWS connection lists no Application Load Balancers in this region." })).toBeDefined()
    expect(await pane.find({ type: 'Button' })).toBeUndefined()
  }
})

test('a asks about a target group returning 5xx when every target is healthy', async () => {
  NEW_ROOM()
  const healthy = structuredClone(LB)
  for (const t of healthy.loadBalancers[0].targetGroups[0].targets) Object.assign(t, { state: 'healthy', reason: '', detail: undefined })
  const f = await readBoard(healthy)
  const ask = lbMod.keys(fakeKit('terminal'), f.io, NOW, null)[1]
  expect(ask.props.label).toBe('ask about web-edge-tg')
  await ask.props.onPress()
  expect(f.log.filled[0].text).toBe('Why is target group web-edge-tg behind web-edge-alb returning 5xx?')
})

test('health or metrics that could not be read, and a cut list, are said in sentences', async ($, on) => {
  NEW_ROOM()
  await readBoard(PARTIAL)
  hookTab(on, 'd-lb-partial', (k, io, now, args) => chromed(lbMod, k, io, now, args))
  for (const surface of ['terminal', 'desktop'] as const) {
    const pane = await $.ui.mount({ ...paneProps('d-lb-partial'), surface } as never)
    expect(await pane.find({ type: 'Text', text: "The room's AWS connection could not read load balancers. Try again shortly." })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: "5xx and healthy host counts are not shown. The room's AWS connection is not allowed to read load balancers." })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: 'Some load balancers or target groups are not shown. Run /landfall lb <name> to see one load balancer alone.' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: 'No registered targets.' })).toBeUndefined()
    // No heat map of nothing: a grid of empty minutes would read as data.
    expect(await pane.findAll({ type: 'Raster' })).toHaveLength(0)
    expect((await pane.findAll({ type: 'Svg' })).filter((x: any) => String(x.props.alt).startsWith('5xx'))).toHaveLength(0)
  }
  expect(lbMod.keys(fakeKit('terminal'), fakeIo().io, NOW, null).map((b: any) => b.props.key)).toEqual(['lb-share'])
})

test('first read says it is reading, a failed refresh keeps the board and the footer says stale', async () => {
  NEW_ROOM()
  const k = fakeKit('terminal')
  let answer: unknown = LB
  const f = fakeIo({ run: async () => answer })
  expect(textsOf(chromed(lbMod, k, f.io, NOW, null))).toEqual(['Reading the room’s AWS connection…'])
  await lbMod.warm(f.io)
  answer = { ok: false, error: 'Your sign-in expired. Run landfall login.' }
  await lbMod.refresh(f.io)
  const texts = textsOf(chromed(lbMod, k, f.io, NOW + 120000, null))
  expect(texts).toContain('web-edge-alb')
  expect(texts).toContain('stale · updated 2m ago · Your sign-in expired. Run landfall login.')
})

test('with no load balancer the tab says so', async () => {
  NEW_ROOM()
  await readBoard({ ok: true, region: 'us-east-1', loadBalancers: [] })
  expect(textsOf(chromed(lbMod, fakeKit('terminal'), fakeIo().io, NOW, null))).toContain('No load balancers in scope.')
})

test('/landfall lb <name> reads that load balancer once and offers to show them all', async () => {
  const room = NEW_ROOM()
  const k = fakeKit('terminal')
  const f = fakeIo({ run: async (args: string[]) => (f.log.runs.push(args), LB) })
  consoleState.args = 'web-edge-alb'
  const tree = chromed(lbMod, k, f.io, NOW, 'web-edge-alb')
  expect(consoleState.args).toBeNull()
  await new Promise((r) => setTimeout(r, 0))
  expect(f.log.runs).toEqual([['lb', '--host', 'claude-code', '--room', room.roomKey, '--lb', 'web-edge-alb']])
  expect(textsOf(tree)).toContain('Showing web-edge-alb only.')
  // Drawn again with the same words, it does not read again.
  chromed(lbMod, k, f.io, NOW, 'web-edge-alb')
  await new Promise((r) => setTimeout(r, 0))
  expect(f.log.runs).toHaveLength(1)
  // The person shows them all.
  buttonOf(chromed(lbMod, k, f.io, NOW, null), 'lb-all')!.props.onPress()
  await new Promise((r) => setTimeout(r, 0))
  expect(f.log.runs.at(-1)).toEqual(['lb', '--host', 'claude-code', '--room', room.roomKey])
})

test('the board reads every 60 s while the console is open, and stops when it closes', async () => {
  NEW_ROOM()
  const f = fakeIo({ run: async (args: string[]) => (f.log.runs.push(args), LB) })
  consoleState.open = true
  consoleState.warm.add('lb')
  await lbMod.warm(f.io)
  expect(f.log.runs).toHaveLength(1)
  await lbMod.tick(f.io, NOW + 30000)
  expect(f.log.runs).toHaveLength(1)
  await lbMod.tick(f.io, NOW + 60000)
  await new Promise((r) => setTimeout(r, 0))
  expect(f.log.runs).toHaveLength(2)
  consoleState.open = false
  await lbMod.tick(f.io, NOW + 130000)
  await lbMod.tick(f.io, NOW + 200000)
  expect(f.log.runs).toHaveLength(2)
})

test('every read names the room this session is in with --room, and a different room starts the board over', async () => {
  const room = NEW_ROOM()
  const f = fakeIo({ run: async (args: string[]) => (f.log.runs.push(args), LB) })
  await lbMod.warm(f.io)
  expect(f.log.runs[0]).toContain(room.roomKey)
  expect(textsOf(chromed(lbMod, fakeKit('terminal'), f.io, NOW, null))).toContain('web-edge-alb')
  NEW_ROOM()
  expect(textsOf(chromed(lbMod, fakeKit('terminal'), f.io, NOW, null))).toEqual(['Reading the room’s AWS connection…'])
})

test('text answers in words, with the sentence when there is no ALB, and says what could not be read', async () => {
  NEW_ROOM()
  expect(await lbMod.text(fakeIo({ run: async () => LB }).io, '')).toBe(
    [
      'web-edge-alb · healthy 5 of 7 · us-east-1',
      '  Target group web-edge-tg · HTTP 8080 · health check /healthz · 5xx 34% now',
      '    us-east-1a: i-0a3f healthy, i-0b71 healthy',
      '    us-east-1b: i-04c2 healthy, i-09de healthy',
      '    us-east-1c: i-02f9 unhealthy (Target.Timeout), i-07aa unhealthy (Target.Timeout)',
      '  Target group web-edge-canary-tg · HTTP 8081 · 5xx 0% now',
      '    us-east-1a: 10.0.4.17:9000 healthy',
    ].join('\n'),
  )
  NEW_ROOM()
  expect(await lbMod.text(fakeIo({ run: async () => ({ ok: false, error: "The room's AWS connection lists no Application Load Balancers in this region." }) }).io, '')).toBe(
    "The room's AWS connection lists no Application Load Balancers in this region.",
  )
  NEW_ROOM()
  expect(await lbMod.text(fakeIo({ run: async () => PARTIAL }).io, '')).toBe(
    [
      'web-edge-alb · healthy 0 of 0 · us-east-1',
      '  Target group web-edge-tg · HTTP 8080 · health check /healthz',
      "    The room's AWS connection could not read load balancers. Try again shortly.",
      "5xx and healthy host counts are not shown. The room's AWS connection is not allowed to read load balancers.",
      'Some load balancers or target groups are not shown. Run /landfall lb <name> to see one load balancer alone.',
    ].join('\n'),
  )
})

test('no standalone pane or command is left behind, and the tab has no badge', () => {
  const hooks: unknown[] = []
  lbMod.install((...a: unknown[]) => void hooks.push(a))
  expect(hooks).toEqual([])
  expect(lbMod.badge()).toBeNull()
})

test('the mobile tab draws the board and a share key', async () => {
  NEW_ROOM()
  await readBoard(LB)
  const tree = chromed(lbMod, fakeKit('mobile'), fakeIo().io, NOW, null)
  expect(textsOf(tree)).toContain('● healthy 5 of 7')
  expect(nodes(tree, 'Button').map((b) => b.props.key)).toEqual(['lb-share', 'lb-ask'])
})

test('on the desktop the health and 5xx labels sit on the text baseline of their rows', async () => {
  NEW_ROOM()
  await readBoard(LB)
  const boxes = nodes(chromed(lbMod, fakeKit('desktop'), fakeIo().io, NOW, null), 'Box')
  for (const key of ['lb0-h', 'lb0-tg0-n', 'lb0-hr0']) expect(boxes.find((b) => b.props.key === key)?.props.alignItems).toBe('center')
})
