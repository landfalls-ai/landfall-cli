import { expect, mock, test } from 'claude-code/testing'

// `landfall wall` as contracts/cli-json.md §5 prints it: every widget type,
// data already resolved through the person's own session.
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
      series: [{ label: '5xxErrorRate', points: ERR.map((v, i) => [T0 + i * MIN, v]) }],
      markers: [{ atMs: T0 + 9 * MIN, label: 'web-edge v2.3.1' }],
      threshold: 2,
    },
    { id: 'w2', type: 'stat', title: 'Healthy origins', tone: 'warning', value: '4', unit: '/6', spark: [6, 6, 6, 5, 4, 4] },
    {
      id: 'w3',
      type: 'logView',
      title: 'Logs · web-edge',
      lines: [
        { level: 'error', text: 'upstream timeout pool=origin-b' },
        { level: 'error', text: 'upstream timeout pool=origin-b' },
        { level: 'warn', text: 'retry budget 80% used' },
      ],
    },
    {
      id: 'w4',
      type: 'graph',
      title: 'Topology',
      nodes: [
        { id: 'cf', label: 'cloudfront', tone: 'warning' },
        { id: 'alb', label: 'web-edge-alb', tone: 'warning' },
        { id: 'c', label: 'us-east-1c', tone: 'critical' },
      ],
      edges: [
        { from: 'cf', to: 'alb', trust: 'confirmed' },
        { from: 'alb', to: 'c', trust: 'established' },
      ],
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
  ],
  unavailable: [{ id: 'w9', title: 'RDS replica lag', reason: 'needs a connection you cannot read' }],
}

const ROOM = { roomKey: 'k168', incidentId: 'i168', displayId: 'Landfall 168', title: 'cloudfront-5xx-high', slug: 'acme', connection: 'live', count: 0, addressed: 0, votesAwaited: 0, digest: [] }
const snap = (maxSeq: number) => JSON.stringify({ type: 'rooms', line: '', rooms: [{ ...ROOM, maxSeq }] }) + '\n'

const PANE = { title: 'Wall', isFocused: true, bodyColumns: 100, placement: 'dock', scroll: { offset: 0, bodyRows: 60 }, view: {} } as const
const VIEW = { columns: 140, rows: 50, isFullscreen: true }

function ran(stdout: string) {
  return { value: { exitCode: 0, stdout, stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
}

async function settle(done: () => boolean) {
  for (let i = 0; i < 60 && !done(); i++) await new Promise((r) => setTimeout(r, 5))
}

test('/wall draws every widget type in the shared arrangement, on each surface', async ($, on) => {
  const runs: Array<readonly string[]> = []
  on('process.run', ($, e) => {
    runs.push(e.argv)
    return ran('{"note":"progress"}\n' + JSON.stringify(WALL) + '\n')
  })
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('command.register', () => ({ value: undefined }))
  for (const surface of ['terminal', 'desktop'] as const) {
    const answer = await $.command.run({ command: 'wall', args: '' })
    expect(answer.text).toBeUndefined()
    expect(runs.at(-1)).toEqual(['landfall', 'wall', '--host', 'claude-code'])

    const pane = await $.ui.mount({ plugin: 'landfall', surface, component: 'Pane', requestId: 'landfall-wall', viewport: VIEW, props: PANE })
    expect(await pane.find({ type: 'Text', text: 'Wall · shared by carol' })).toBeDefined()
    // Every widget is a title the person can select.
    for (const title of ['5xx error rate', 'Healthy origins', 'Logs · web-edge', 'Topology', 'Target groups', 'Deploys', 'What happened', 'Mystery widget']) {
      expect(await pane.find({ type: 'Button', text: new RegExp(title.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '$') })).toBeDefined()
    }
    // stat: the big value and its unit.
    expect(await pane.find({ type: 'Text', text: '4' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: '/6' })).toBeDefined()
    // logView: level-colored tags.
    const errs = await pane.findAll({ type: 'Text', text: 'ERR' })
    expect(errs).toHaveLength(2)
    expect(errs[0].props.color).toBe('#d03b3b')
    expect((await pane.find({ type: 'Text', text: 'WRN' }))?.props.color).toBe('#fab219')
    // table: aligned columns, header first.
    expect(await pane.find({ type: 'Text', text: /^target\s+zone\s+state$/ })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: /^i-07aa\s+us-east-1c\s+unhealthy$/ })).toBeDefined()
    // events and timeline.
    expect(await pane.find({ type: 'Text', text: /^15:29 deploy-bot UpdateService web-edge/ })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: 'Alarm 5xx over 2%' })).toBeDefined()
    // an unknown type is its title alone.
    expect(await pane.find({ type: 'Text', text: 'heatmapOfTheFuture widgets draw in the web app.' })).toBeDefined()
    // unavailable, with the reason.
    expect(await pane.find({ type: 'Text', text: 'RDS replica lag: needs a connection you cannot read' })).toBeDefined()

    if (surface === 'terminal') {
      // chart: a Raster sparkline per series and the deploy marker under it.
      expect(await pane.find({ type: 'Raster', key: 'w-w1-s0' })).toBeDefined()
      expect(await pane.find({ type: 'Text', text: /^ {9}↑ web-edge v2\.3\.1 15:29Z$/ })).toBeDefined()
      expect(await pane.find({ type: 'Raster', key: 'w-w2-s' })).toBeDefined()
      // graph: an indented tree with trust words.
      expect(await pane.find({ type: 'Text', text: 'cloudfront' })).toBeDefined()
      expect(await pane.find({ type: 'Text', text: 'confirmed' })).toBeDefined()
      expect(await pane.find({ type: 'Text', text: 'established' })).toBeDefined()
      expect(await pane.findAll({ type: 'Svg' })).toHaveLength(0)
    } else {
      // chart: a vector line with the threshold and the deploy marker.
      const svgs = await pane.findAll({ type: 'Svg' })
      const chart = svgs.find((x) => String(x.props.alt).startsWith('5xx error rate:'))
      expect(chart?.props.source).toContain('stroke-dasharray="4 3"')
      expect(chart?.props.source).toContain('web-edge v2.3.1')
      expect(chart?.props.alt).toBe('5xx error rate: 5xxErrorRate last 3.1, peak 6.1; threshold 2; web-edge v2.3.1 at 15:29Z')
      expect(chart?.props.isInteractive).toBe(true)
      // graph: trust as line style.
      const graph = svgs.find((x) => String(x.props.alt).startsWith('Topology:'))
      expect(graph?.props.source).toContain('stroke-dasharray="6 3"')
      expect(graph?.props.source).toContain('#d03b3b')
      // Two columns when the pane is wide enough.
      expect(await pane.find({ type: 'Box', key: 'wall-grid' })).toBeDefined()
      expect(await pane.findAll({ type: 'Raster' })).toHaveLength(0)
    }
    await pane.unmount()
  }
})

test('a on the selected widget drafts a question about it, and never sends', async ($, on) => {
  let filled: string[] = []
  let submitted = 0
  let statuses = 0
  mock.clock(on)
  on('env.get', () => ({ value: undefined }))
  on('env.set', () => ({ value: undefined }))
  on('command.register', () => ({ value: undefined }))
  on('ui.status', () => {
    statuses += 1
    return { value: undefined }
  })
  on('ui.toast', () => ({ value: undefined }))
  on('process.spawn', async function* () {
    yield { stream: 'stdout', text: snap(41) }
    return { value: { code: 0, signal: null } }
  })
  on('process.run', () => ran(JSON.stringify(WALL) + '\n'))
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('prompt.fill', ($, e) => {
    filled.push(e.text)
    return { isFilled: true }
  })
  on('prompt.submit', () => {
    submitted += 1
    return { text: '' }
  })
  on('session.start', () => ({ cwd: '/work' }))
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' } as never)
  await settle(() => statuses >= 1)
  for (const surface of ['terminal', 'desktop'] as const) {
    filled = []
    await $.command.run({ command: 'wall', args: '' })
    const pane = await $.ui.mount({ plugin: 'landfall', surface, component: 'Pane', requestId: 'landfall-wall', viewport: VIEW, props: PANE })
    await pane.press({ key: 'w-w3-t' })
    await pane.press({ key: 'wall-ask' })
    await pane.press({ key: 'w-w1-t' })
    await pane.press({ key: 'wall-next' })
    await pane.press({ key: 'wall-ask' })
    expect(filled).toEqual(['Tell me about the Logs · web-edge widget in Landfall 168.', 'Tell me about the Healthy origins widget in Landfall 168.'])
    expect(submitted).toBe(0)
    await pane.unmount()
  }
})

test('the wall reads again when the room moves on, at most every 20 seconds, only while open', async ($, on) => {
  // The clock is the test's: the watch stream below never ends, so nothing
  // here waits for the event loop to settle.
  let now = 0
  const release: Array<() => void> = []
  const wallRuns: Array<readonly string[]> = []
  let statuses = 0
  on('clock.now', () => ({ value: now }))
  on('env.get', () => ({ value: undefined }))
  on('env.set', () => ({ value: undefined }))
  on('command.register', () => ({ value: undefined }))
  on('ui.toast', () => ({ value: undefined }))
  on('ui.status', () => {
    statuses += 1
    return { value: undefined }
  })
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('ui.close', () => ({ value: undefined }))
  on('process.spawn', async function* () {
    for (const seq of [41, 50, 60, 70]) {
      yield { stream: 'stdout', text: snap(seq) }
      await new Promise<void>((r) => release.push(r))
    }
    return { value: { code: 0, signal: null } }
  })
  on('process.run', ($, e) => {
    if (e.argv[1] === 'wall') wallRuns.push(e.argv)
    return ran(JSON.stringify(WALL) + '\n')
  })
  on('session.start', () => ({ cwd: '/work' }))
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' } as never)
  await settle(() => statuses >= 1)

  await $.command.run({ command: 'wall', args: '' })
  expect(wallRuns).toEqual([['landfall', 'wall', '--host', 'claude-code', '--room', 'k168']])
  let pane = await $.ui.mount({ plugin: 'landfall', surface: 'terminal', component: 'Pane', requestId: 'landfall-wall', viewport: VIEW, props: PANE })

  // 10s on, a new seq, but within 20 seconds of the last read: no read.
  now = 10000
  release.shift()?.()
  await settle(() => statuses >= 2)
  await settle(() => false)
  expect(wallRuns).toHaveLength(1)

  // 21s on, the pane still drawn: the next new seq reads again.
  now = 21000
  await pane.unmount()
  pane = await $.ui.mount({ plugin: 'landfall', surface: 'terminal', component: 'Pane', requestId: 'landfall-wall', viewport: VIEW, props: PANE })
  release.shift()?.()
  await settle(() => wallRuns.length >= 2)
  expect(wallRuns).toHaveLength(2)

  // Closed: no more reads, however long it has been.
  await pane.press({ key: 'wall-close' })
  await pane.unmount()
  now = 100000
  release.shift()?.()
  await settle(() => statuses >= 4)
  await settle(() => false)
  expect(statuses).toBe(4)
  expect(wallRuns).toHaveLength(2)
})

test('/wall answers in text where no pane can be placed, and says why when the wall cannot be read', async ($, on) => {
  let answerJson = JSON.stringify(WALL)
  on('process.run', () => ran(answerJson + '\n'))
  on('ui.open', () => ({ value: { isPlaced: false, reason: 'headless' } }))
  on('command.register', () => ({ value: undefined }))

  const answer = await $.command.run({ command: 'wall', args: '' })
  expect(answer.text).toContain('Wall · shared by carol · last 6h')
  expect(answer.text).toContain('Healthy origins\n  4/6')
  expect(answer.text).toContain('  5xxErrorRate: last 3.1, peak 6.1\n  marker: web-edge v2.3.1 at 15:29Z\n  threshold 2')
  expect(answer.text).toContain('Topology\n  cloudfront  warning\n  └─ web-edge-alb  confirmed  warning\n     └─ us-east-1c  established  critical')
  expect(answer.text).toContain('  ERR upstream timeout pool=origin-b')
  expect(answer.text).toContain('Not shown here:\n  RDS replica lag: needs a connection you cannot read')

  answerJson = '{"ok":false,"error":"Your sign-in expired. Run landfall login."}'
  const failed = await $.command.run({ command: 'wall', args: '' })
  expect(failed.text).toBe('Your sign-in expired. Run landfall login.')
})

test('a failed read shows its sentence in the pane', async ($, on) => {
  on('process.run', () => ran('{"ok":false,"error":"This checkout is not reading any room right now."}\n'))
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('command.register', () => ({ value: undefined }))
  for (const surface of ['terminal', 'desktop'] as const) {
    await $.command.run({ command: 'wall', args: '' })
    const pane = await $.ui.mount({ plugin: 'landfall', surface, component: 'Pane', requestId: 'landfall-wall', viewport: VIEW, props: PANE })
    expect(await pane.find({ type: 'Text', text: 'This checkout is not reading any room right now.' })).toBeDefined()
    expect(await pane.find({ type: 'Button', key: 'wall-ask' })).toBeUndefined()
    await pane.unmount()
  }
})

test('the wall draws on vscode and mobile too, as vectors', async ($, on) => {
  on('process.run', () => ran(JSON.stringify(WALL) + '\n'))
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('command.register', () => ({ value: undefined }))
  await $.command.run({ command: 'wall', args: '' })
  for (const surface of ['vscode', 'mobile'] as const) {
    const pane = await $.ui.mount({ plugin: 'landfall', surface, component: 'Pane', requestId: 'landfall-wall', viewport: { columns: 60, rows: 40 }, props: { ...PANE, bodyColumns: 50 } })
    expect(await pane.find({ type: 'Text', text: 'Wall · shared by carol' })).toBeDefined()
    expect((await pane.findAll({ type: 'Svg' })).length).toBeGreaterThan(2)
    // Narrow: one column.
    expect(await pane.find({ type: 'Box', key: 'wall-grid' })).toBeUndefined()
    await pane.unmount()
  }
})
