import { expect, test } from 'claude-code/testing'

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

const PANE = { title: 'Load balancers', isFocused: true, bodyColumns: 100, placement: 'dock', scroll: { offset: 0, bodyRows: 60 }, view: {} } as const
const VIEW = { columns: 140, rows: 50, isFullscreen: true }

function ran(stdout: string) {
  return { value: { exitCode: 0, stdout, stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
}

// cell reads one Raster cell's code point: [codePoint, fg, bg] u32 words, base64.
function cell(raster: any, row: number, col: number) {
  const bin = atob(raster.props.cells)
  const at = (row * raster.props.columns + col) * 12
  return bin.charCodeAt(at) | (bin.charCodeAt(at + 1) << 8) | (bin.charCodeAt(at + 2) << 16)
}

test('/lb draws each target group with its targets by zone and 5xx per target group per minute', async ($, on) => {
  const runs: Array<readonly string[]> = []
  on('process.run', ($, e) => {
    runs.push(e.argv)
    return ran(JSON.stringify(LB) + '\n')
  })
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('command.register', () => ({ value: undefined }))
  for (const surface of ['terminal', 'desktop'] as const) {
    await $.command.run({ command: 'lb', args: 'web-edge-alb' })
    expect(runs.at(-1)).toEqual(['landfall', 'lb', '--host', 'claude-code', '--lb', 'web-edge-alb'])

    const pane = await $.ui.mount({ plugin: 'landfall', surface, component: 'Pane', requestId: 'landfall-lb', viewport: VIEW, props: PANE })
    expect(await pane.find({ type: 'Text', text: 'web-edge-alb' })).toBeDefined()
    expect((await pane.find({ type: 'Text', text: '● healthy 5 of 7' }))?.props.color).toBe('#fab219')
    expect(await pane.find({ type: 'Text', text: 'us-east-1 · internet-facing' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: 'Zones: us-east-1a, us-east-1b, us-east-1c' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: 'Target group web-edge-tg · HTTP 8080 · health check /healthz' })).toBeDefined()
    // The group's latest 5xx share, in its tone; 5xx is the group's, never a target's.
    expect((await pane.find({ type: 'Text', text: '● 5xx 34%' }))?.props.color).toBe('#d03b3b')
    expect((await pane.find({ type: 'Text', text: '● 5xx 0%' }))?.props.color).toBe('#0ca30c')
    expect(await pane.find({ type: 'Text', text: /5xx 34%$/ })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: /^■ i-07aa.*5xx/ })).toBeUndefined()
    // Zones in order, each target in its health color; a target on another port says so.
    for (const zone of ['us-east-1a', 'us-east-1b', 'us-east-1c']) expect(await pane.find({ type: 'Text', text: zone })).toBeDefined()
    expect((await pane.find({ type: 'Text', text: /^■ i-07aa/ }))?.props.color).toBe('#d03b3b')
    expect((await pane.find({ type: 'Text', text: /^■ i-0a3f/ }))?.props.color).toBe('#0ca30c')
    expect(await pane.find({ type: 'Text', text: /^■ 10\.0\.4\.17:9000/ })).toBeDefined()
    // The legend, with the no-data cell.
    expect(await pane.find({ type: 'Text', text: '▪ under 1%' })).toBeDefined()
    expect((await pane.find({ type: 'Text', text: '█ over 20%' }))?.props.color).toBe('#d03b3b')
    expect(await pane.find({ type: 'Text', text: surface === 'terminal' ? '· no requests' : '□ no requests' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: '5xx per target group, last 36 minutes' })).toBeDefined()

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
      expect(await pane.find({ type: 'Text', text: /^web-edge-tg +$/ })).toBeDefined()
      expect(await pane.find({ type: 'Text', text: 'web-edge-canary-tg' })).toBeDefined()
      expect((await pane.find({ type: 'Text', text: '34%' }))?.props.color).toBe('#d03b3b')
      expect(await pane.find({ type: 'Text', text: /^ +16:50 +now$/ })).toBeDefined()
      // An unhealthy target's detail, dim, under its zone.
      expect(await pane.find({ type: 'Text', text: '  i-07aa: Request timed out' })).toBeDefined()
      expect(await pane.findAll({ type: 'Svg' })).toHaveLength(0)
    } else {
      // An Svg grid with a hover title per cell, and the healthy host count.
      const svgs = await pane.findAll({ type: 'Svg' })
      const heat = svgs.find((x) => String(x.props.alt).startsWith('5xx per target group per minute'))
      expect(heat?.props.alt).toBe('5xx per target group per minute, 2 target groups over 36 minutes')
      expect(heat?.props.isInteractive).toBe(true)
      expect(heat?.props.source).toContain('<title>web-edge-tg · 17:10Z · 5xx 34% (170 of 500 requests)</title>')
      expect(heat?.props.source).toContain('<title>web-edge-tg · 16:50Z · 5xx 0.2% (1 of 500 requests)</title>')
      expect(heat?.props.source).toContain('<title>web-edge-tg · 16:53Z · no requests</title>')
      expect(heat?.props.source).toContain('stroke-dasharray="2 2"')
      expect(svgs.find((x) => x.props.alt === 'healthy host count')).toBeDefined()
      expect(await pane.find({ type: 'Text', text: '■ i-07aa · unhealthy · Target.Timeout' })).toBeDefined()
      expect(await pane.find({ type: 'Text', text: 'Request timed out' })).toBeDefined()
      expect(await pane.findAll({ type: 'Raster' })).toHaveLength(0)
    }
    await pane.unmount()
  }
})

test('s shares the board as a widget through the chart path, with the CLI\'s own title, and a asks about the worst zone', async ($, on) => {
  const runs: Array<readonly string[]> = []
  const toasts: string[] = []
  const filled: string[] = []
  let mcpCalls = 0
  on('process.run', ($, e) => {
    runs.push(e.argv)
    if (e.argv[1] === 'chart') return ran('{"ok":true,"title":"5xx per target group · web-edge-alb"}\n')
    return ran(JSON.stringify(LB) + '\n')
  })
  on('mcp.call', () => {
    mcpCalls += 1
    return { value: { content: [], isError: false } }
  })
  on('ui.toast', ($, e) => {
    toasts.push(e.text)
    return { value: undefined }
  })
  on('prompt.fill', ($, e) => {
    filled.push(e.text)
    return { isFilled: true }
  })
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('command.register', () => ({ value: undefined }))
  for (const surface of ['terminal', 'desktop'] as const) {
    runs.length = 0
    toasts.length = 0
    filled.length = 0
    await $.command.run({ command: 'lb', args: '' })
    expect(runs[0]).toEqual(['landfall', 'lb', '--host', 'claude-code'])
    const pane = await $.ui.mount({ plugin: 'landfall', surface, component: 'Pane', requestId: 'landfall-lb', viewport: VIEW, props: PANE })
    await pane.press({ key: 'lb-share' })
    expect(runs[1].slice(0, 4)).toEqual(['landfall', 'chart', '--host', 'claude-code'])
    expect(runs[1][4]).toBe('--query')
    expect(JSON.parse(runs[1][5] as string)).toEqual(QUERY)
    expect(toasts).toEqual(['Chart added to your dashboard in the room: 5xx per target group · web-edge-alb'])
    await pane.press({ key: 'lb-ask' })
    expect(filled).toEqual(['Why are targets in us-east-1c behind web-edge-alb unhealthy or returning 5xx?'])
    expect(mcpCalls).toBe(0)
    await pane.unmount()
  }
})

test('a refused share says why', async ($, on) => {
  const toasts: string[] = []
  on('process.run', ($, e) => {
    if (e.argv[1] === 'chart') return ran('{"ok":false,"error":"This checkout is not reading any room right now."}\n')
    return ran(JSON.stringify(LB) + '\n')
  })
  on('ui.toast', ($, e) => {
    toasts.push(e.text)
    return { value: undefined }
  })
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('command.register', () => ({ value: undefined }))
  await $.command.run({ command: 'lb', args: '' })
  const pane = await $.ui.mount({ plugin: 'landfall', surface: 'terminal', component: 'Pane', requestId: 'landfall-lb', viewport: VIEW, props: PANE })
  await pane.press({ key: 'lb-share' })
  expect(toasts).toEqual(['Chart not added: This checkout is not reading any room right now.'])
})

test('/lb answers in text where no pane can be placed, and with the sentence when there is no ALB', async ($, on) => {
  let out = JSON.stringify(LB)
  on('process.run', () => ran(out + '\n'))
  on('ui.open', () => ({ value: { isPlaced: false, reason: 'headless' } }))
  on('command.register', () => ({ value: undefined }))
  const answer = await $.command.run({ command: 'lb', args: '' })
  expect(answer.text).toBe(
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

  out = NO_ALB
  const none = await $.command.run({ command: 'lb', args: '' })
  expect(none.text).toBe("The room's AWS connection lists no Application Load Balancers in this region.")
})

test('the pane shows the sentence when the board cannot be read', async ($, on) => {
  on('process.run', () => ran(NO_ALB + '\n'))
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('command.register', () => ({ value: undefined }))
  await $.command.run({ command: 'lb', args: '' })
  for (const surface of ['terminal', 'desktop'] as const) {
    const pane = await $.ui.mount({ plugin: 'landfall', surface, component: 'Pane', requestId: 'landfall-lb', viewport: VIEW, props: PANE })
    expect(await pane.find({ type: 'Text', text: "The room's AWS connection lists no Application Load Balancers in this region." })).toBeDefined()
    expect(await pane.find({ type: 'Button', key: 'lb-share' })).toBeUndefined()
    await pane.unmount()
  }
})

test('the board draws on vscode and mobile too', async ($, on) => {
  on('process.run', () => ran(JSON.stringify(LB) + '\n'))
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('command.register', () => ({ value: undefined }))
  await $.command.run({ command: 'lb', args: '' })
  for (const surface of ['vscode', 'mobile'] as const) {
    const pane = await $.ui.mount({ plugin: 'landfall', surface, component: 'Pane', requestId: 'landfall-lb', viewport: { columns: 60, rows: 40 }, props: { ...PANE, bodyColumns: 50 } })
    expect(await pane.find({ type: 'Text', text: '● healthy 5 of 7' })).toBeDefined()
    expect(await pane.find({ type: 'Button', key: 'lb-share' })).toBeDefined()
    await pane.unmount()
  }
})

test('a asks about a target group returning 5xx when every target is healthy', async ($, on) => {
  const filled: string[] = []
  const healthy = structuredClone(LB)
  for (const t of healthy.loadBalancers[0].targetGroups[0].targets) Object.assign(t, { state: 'healthy', reason: '', detail: undefined })
  on('process.run', () => ran(JSON.stringify(healthy) + '\n'))
  on('prompt.fill', ($, e) => {
    filled.push(e.text)
    return { isFilled: true }
  })
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('command.register', () => ({ value: undefined }))
  await $.command.run({ command: 'lb', args: '' })
  const pane = await $.ui.mount({ plugin: 'landfall', surface: 'terminal', component: 'Pane', requestId: 'landfall-lb', viewport: VIEW, props: PANE })
  expect((await pane.find({ type: 'Button', key: 'lb-ask' }))?.props.label).toBe('ask about web-edge-tg')
  await pane.press({ key: 'lb-ask' })
  expect(filled).toEqual(['Why is target group web-edge-tg behind web-edge-alb returning 5xx?'])
})

test('health or metrics that could not be read, and a cut list, are said in sentences', async ($, on) => {
  on('process.run', () => ran(JSON.stringify(PARTIAL) + '\n'))
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('command.register', () => ({ value: undefined }))
  await $.command.run({ command: 'lb', args: '' })
  for (const surface of ['terminal', 'desktop'] as const) {
    const pane = await $.ui.mount({ plugin: 'landfall', surface, component: 'Pane', requestId: 'landfall-lb', viewport: VIEW, props: PANE })
    expect(await pane.find({ type: 'Text', text: "The room's AWS connection could not read load balancers. Try again shortly." })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: "5xx and healthy host counts are not shown. The room's AWS connection is not allowed to read load balancers." })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: 'Some load balancers or target groups are not shown. Run /lb with a name to see one load balancer alone.' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: 'No registered targets.' })).toBeUndefined()
    // No heat map of nothing: a grid of empty minutes would read as data.
    expect(await pane.findAll({ type: 'Raster' })).toHaveLength(0)
    expect((await pane.findAll({ type: 'Svg' })).filter((x) => String(x.props.alt).startsWith('5xx'))).toHaveLength(0)
    expect(await pane.find({ type: 'Button', key: 'lb-ask' })).toBeUndefined()
    await pane.unmount()
  }
})

test('/lb in text says what could not be read', async ($, on) => {
  on('process.run', () => ran(JSON.stringify(PARTIAL) + '\n'))
  on('ui.open', () => ({ value: { isPlaced: false, reason: 'headless' } }))
  on('command.register', () => ({ value: undefined }))
  const answer = await $.command.run({ command: 'lb', args: '' })
  expect(answer.text).toBe(
    [
      'web-edge-alb · healthy 0 of 0 · us-east-1',
      '  Target group web-edge-tg · HTTP 8080 · health check /healthz',
      "    The room's AWS connection could not read load balancers. Try again shortly.",
      "5xx and healthy host counts are not shown. The room's AWS connection is not allowed to read load balancers.",
      'Some load balancers or target groups are not shown. Run /lb with a name to see one load balancer alone.',
    ].join('\n'),
  )
})
