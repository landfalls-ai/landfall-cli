import { expect, test } from 'claude-code/testing'

// `landfall wall` as contracts/cli-json.md §5 prints it, with a graph widget:
// CloudFront to the ALB to three zones, us-east-1c unhealthy.
const GRAPH = {
  id: 'w4',
  type: 'graph',
  title: 'web-edge topology',
  nodes: [
    { id: 'cf', label: 'cloudfront', tone: 'warning' },
    { id: 'alb', label: 'web-edge-alb', tone: 'warning' },
    { id: 'a', label: 'us-east-1a', tone: 'neutral' },
    { id: 'b', label: 'us-east-1b', tone: 'neutral' },
    { id: 'c', label: 'us-east-1c', tone: 'critical' },
  ],
  edges: [
    { from: 'cf', to: 'alb', trust: 'confirmed' },
    { from: 'alb', to: 'a', trust: 'confirmed' },
    { from: 'alb', to: 'b', trust: 'established' },
    { from: 'alb', to: 'c', trust: 'inferred' },
  ],
}
const WALL = {
  ok: true,
  sharedBy: 'carol',
  windowMs: 21600000,
  widgets: [{ id: 'w2', type: 'stat', title: 'Healthy origins', tone: 'warning', value: '4', unit: '/6', spark: [6, 6, 5, 4] }, GRAPH],
  unavailable: [],
}

const PANE = { title: 'Topology', isFocused: true, bodyColumns: 100, placement: 'dock', scroll: { offset: 0, bodyRows: 60 }, view: {} } as const
const VIEW = { columns: 140, rows: 50, isFullscreen: true }

function ran(stdout: string) {
  return { value: { exitCode: 0, stdout, stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
}

test('/topology draws the wall’s graph: trust as line style on the desktop, a tree with trust words on the terminal', async ($, on) => {
  const runs: Array<readonly string[]> = []
  on('process.run', ($, e) => {
    runs.push(e.argv)
    return ran(JSON.stringify(WALL) + '\n')
  })
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('command.register', () => ({ value: undefined }))
  for (const surface of ['terminal', 'desktop'] as const) {
    await $.command.run({ command: 'topology', args: '' })
    expect(runs.at(-1)).toEqual(['landfall', 'wall', '--host', 'claude-code'])
    const pane = await $.ui.mount({ plugin: 'landfall', surface, component: 'Pane', requestId: 'landfall-topology', viewport: VIEW, props: PANE })
    expect(await pane.find({ type: 'Text', text: 'web-edge topology' })).toBeDefined()
    if (surface === 'desktop') {
      const svg = (await pane.findAll({ type: 'Svg' })).find((x) => String(x.props.alt).startsWith('web-edge topology:'))
      expect(svg?.props.alt).toBe('web-edge topology: 5 nodes, 4 links; cloudfront warning, web-edge-alb warning, us-east-1c critical')
      expect(svg?.props.isInteractive).toBe(true)
      const src = String(svg?.props.source)
      // confirmed solid, established dashed, inferred dotted.
      expect(src).toMatch(/<line [^>]*stroke-width="1.4"><title>cloudfront to web-edge-alb: confirmed<\/title>/)
      expect(src).toMatch(/stroke-dasharray="6 3"><title>web-edge-alb to us-east-1b: established<\/title>/)
      expect(src).toMatch(/stroke-dasharray="2 4"><title>web-edge-alb to us-east-1c: inferred<\/title>/)
      // Unhealthy nodes in their tone.
      expect(src).toContain('stroke="#d03b3b" stroke-width="2"')
      expect(src).toContain('solid confirmed · dashed established · dotted inferred')
    } else {
      expect(await pane.findAll({ type: 'Svg' })).toHaveLength(0)
      expect((await pane.find({ type: 'Text', text: 'cloudfront' }))?.props.bold).toBe(true)
      expect((await pane.find({ type: 'Text', text: 'us-east-1c' }))?.props.color).toBe('#d03b3b')
      expect(await pane.findAll({ type: 'Text', text: /^confirmed$/ })).toHaveLength(2)
      expect(await pane.find({ type: 'Text', text: 'established' })).toBeDefined()
      expect(await pane.find({ type: 'Text', text: 'inferred' })).toBeDefined()
      expect(await pane.find({ type: 'Text', text: '   └─' })).toBeDefined()
    }
    await pane.unmount()
  }
})

test('/topology answers in text where no pane can be placed, and says so when the wall has no graph', async ($, on) => {
  let out: unknown = WALL
  on('process.run', () => ran(JSON.stringify(out) + '\n'))
  on('ui.open', () => ({ value: { isPlaced: false, reason: 'headless' } }))
  on('command.register', () => ({ value: undefined }))
  const answer = await $.command.run({ command: 'topology', args: '' })
  expect(answer.text).toBe(
    [
      'web-edge topology',
      '  cloudfront  warning',
      '  └─ web-edge-alb  confirmed  warning',
      '     ├─ us-east-1a  confirmed',
      '     ├─ us-east-1b  established',
      '     └─ us-east-1c  inferred  critical',
    ].join('\n'),
  )

  out = { ...WALL, widgets: [WALL.widgets[0]] }
  const none = await $.command.run({ command: 'topology', args: '' })
  expect(none.text).toBe('The wall has no topology yet. Ask your agent or Beacon to map the services this incident touches.')

  out = { ok: false, error: 'Your sign-in expired. Run landfall login.' }
  const failed = await $.command.run({ command: 'topology', args: '' })
  expect(failed.text).toBe('Your sign-in expired. Run landfall login.')
})

test('the pane says so when the wall has no graph', async ($, on) => {
  on('process.run', () => ran(JSON.stringify({ ...WALL, widgets: [WALL.widgets[0]] }) + '\n'))
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('command.register', () => ({ value: undefined }))
  await $.command.run({ command: 'topology', args: '' })
  for (const surface of ['terminal', 'desktop'] as const) {
    const pane = await $.ui.mount({ plugin: 'landfall', surface, component: 'Pane', requestId: 'landfall-topology', viewport: VIEW, props: PANE })
    expect(await pane.find({ type: 'Text', text: /^The wall has no topology yet\./ })).toBeDefined()
    await pane.unmount()
  }
})

test('the topology draws on vscode and mobile too', async ($, on) => {
  on('process.run', () => ran(JSON.stringify(WALL) + '\n'))
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('command.register', () => ({ value: undefined }))
  await $.command.run({ command: 'topology', args: '' })
  for (const surface of ['vscode', 'mobile'] as const) {
    const pane = await $.ui.mount({ plugin: 'landfall', surface, component: 'Pane', requestId: 'landfall-topology', viewport: { columns: 60, rows: 40 }, props: { ...PANE, bodyColumns: 50 } })
    expect((await pane.findAll({ type: 'Svg' })).some((x) => String(x.props.alt).startsWith('web-edge topology:'))).toBe(true)
    await pane.unmount()
  }
})
