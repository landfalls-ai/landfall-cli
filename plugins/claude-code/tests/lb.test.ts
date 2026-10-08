import { expect, test } from 'claude-code/testing'

// `landfall lb` as contracts/cli-json.md §6 prints it: one ALB, one target
// group, six targets over three zones, 36 minutes of 5xx share per target.
// us-east-1c went bad at minute 15.
const START = 1791478200000 // 2026-10-08T16:50:00Z
const pct = (ti: number) =>
  Array.from({ length: 36 }, (_, m) => {
    if (ti >= 4 && m >= 15) return m < 18 ? 12 : 34
    if (m >= 15 && m < 20) return 2.5
    return 0.2
  })
const TARGETS = [
  { id: 'i-0a3f', zone: 'us-east-1a', state: 'healthy', reason: '' },
  { id: 'i-0b71', zone: 'us-east-1a', state: 'healthy', reason: '' },
  { id: 'i-04c2', zone: 'us-east-1b', state: 'healthy', reason: '' },
  { id: 'i-09de', zone: 'us-east-1b', state: 'healthy', reason: '' },
  { id: 'i-07aa', zone: 'us-east-1c', state: 'unhealthy', reason: 'Target.Timeout' },
  { id: 'i-02f9', zone: 'us-east-1c', state: 'unhealthy', reason: 'Target.Timeout' },
].map((t, i) => ({ ...t, fiveXxPct: pct(i) }))
const QUERY = {
  source: 'cloudwatch',
  operation: 'getMetricData',
  params: {
    MetricDataQueries: [
      { Id: 'e5xx', MetricStat: { Metric: { Namespace: 'AWS/ApplicationELB', MetricName: 'HTTPCode_Target_5XX_Count', Dimensions: [{ Name: 'LoadBalancer', Value: 'app/web-edge-alb/50dc6c495c0c9188' }] }, Period: 60, Stat: 'Sum' } },
    ],
    StartTime: '2026-10-08T16:50:00Z',
    EndTime: '2026-10-08T17:26:00Z',
  },
}
const LB = {
  ok: true,
  account: '123456789012',
  region: 'us-east-1',
  loadBalancers: [
    {
      name: 'web-edge-alb',
      arn: 'arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/web-edge-alb/50dc6c495c0c9188',
      healthy: 4,
      total: 6,
      targetGroups: [{ name: 'web-edge-tg', port: 8080, protocol: 'HTTP', healthCheck: '/healthz', targets: TARGETS }],
      healthyHostCount: [6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 5, 5, 4, 4, 4],
      minuteStartMs: START,
    },
  ],
  query: QUERY,
}
const NO_ALB = '{"ok":false,"error":"The room\'s AWS connection lists no Application Load Balancers in this region."}'

const PANE = { title: 'Load balancers', isFocused: true, bodyColumns: 100, placement: 'dock', scroll: { offset: 0, bodyRows: 60 }, view: {} } as const
const VIEW = { columns: 140, rows: 50, isFullscreen: true }

function ran(stdout: string) {
  return { value: { exitCode: 0, stdout, stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
}

test('/lb draws each target group by zone, target health and 5xx per target per minute', async ($, on) => {
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
    expect((await pane.find({ type: 'Text', text: '● healthy 4 of 6' }))?.props.color).toBe('#fab219')
    expect(await pane.find({ type: 'Text', text: 'Target group web-edge-tg · HTTP 8080 · health check /healthz' })).toBeDefined()
    // Zones in order, each target in its health color.
    for (const zone of ['us-east-1a', 'us-east-1b', 'us-east-1c']) expect(await pane.find({ type: 'Text', text: zone })).toBeDefined()
    const bad = await pane.find({ type: 'Text', text: /^■ i-07aa/ })
    expect(bad?.props.color).toBe('#d03b3b')
    expect((await pane.find({ type: 'Text', text: /^■ i-0a3f/ }))?.props.color).toBe('#0ca30c')
    // The legend.
    expect(await pane.find({ type: 'Text', text: '▪ under 1%' })).toBeDefined()
    expect((await pane.find({ type: 'Text', text: '█ over 20%' }))?.props.color).toBe('#d03b3b')

    if (surface === 'terminal') {
      // One Raster: rows are targets, columns minutes.
      const heat = await pane.find({ type: 'Raster', key: 'heat' })
      expect(heat?.props.rows).toBe(6)
      expect(heat?.props.columns).toBe(36)
      expect(await pane.find({ type: 'Text', text: /^1c i-07aa/ })).toBeDefined()
      expect(await pane.find({ type: 'Text', text: /^ +16:50 +now$/ })).toBeDefined()
      expect(await pane.findAll({ type: 'Svg' })).toHaveLength(0)
    } else {
      // An Svg grid with a hover title per cell, and the healthy host count.
      const svgs = await pane.findAll({ type: 'Svg' })
      const heat = svgs.find((x) => String(x.props.alt).startsWith('5xx per target per minute'))
      expect(heat?.props.isInteractive).toBe(true)
      expect(heat?.props.source).toContain('<title>i-07aa us-east-1c · 17:10Z · 5xx 34%</title>')
      expect(heat?.props.source).toContain('<title>i-0a3f us-east-1a · 16:50Z · 5xx 0.2%</title>')
      expect(svgs.find((x) => x.props.alt === 'healthy host count')).toBeDefined()
      expect(await pane.find({ type: 'Text', text: /^■ i-07aa · unhealthy · Target.Timeout · 5xx 34%$/ })).toBeDefined()
      expect(await pane.findAll({ type: 'Raster' })).toHaveLength(0)
    }
    await pane.unmount()
  }
})

test('s shares the board as a widget through the chart path, and a asks about the worst zone', async ($, on) => {
  const runs: Array<readonly string[]> = []
  const toasts: string[] = []
  const filled: string[] = []
  let mcpCalls = 0
  on('process.run', ($, e) => {
    runs.push(e.argv)
    if (e.argv[1] === 'chart') return ran('{"ok":true,"title":"5xx per target · web-edge-alb"}\n')
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
    expect(JSON.parse(runs[1][5] as string)).toEqual({ ...QUERY, title: '5xx per target · web-edge-alb' })
    expect(toasts).toEqual(['Chart added to your dashboard in the room: 5xx per target · web-edge-alb'])
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
      'web-edge-alb · healthy 4 of 6 · us-east-1',
      '  Target group web-edge-tg · HTTP 8080',
      '    us-east-1a: i-0a3f healthy 5xx 0.2%, i-0b71 healthy 5xx 0.2%',
      '    us-east-1b: i-04c2 healthy 5xx 0.2%, i-09de healthy 5xx 0.2%',
      '    us-east-1c: i-07aa unhealthy (Target.Timeout) 5xx 34%, i-02f9 unhealthy (Target.Timeout) 5xx 34%',
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
    expect(await pane.find({ type: 'Text', text: '● healthy 4 of 6' })).toBeDefined()
    expect(await pane.find({ type: 'Button', key: 'lb-share' })).toBeDefined()
    await pane.unmount()
  }
})
