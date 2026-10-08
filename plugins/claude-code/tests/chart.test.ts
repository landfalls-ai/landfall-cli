import { expect, test } from 'claude-code/testing'

const BAND = {
  plugin: 'landfall',
  component: 'AbovePrompt',
  requestId: 'band',
  viewport: { columns: 100, rows: 30 },
  props: { hasSurvey: false, isWorking: false, maxRows: 6, bodyColumns: 90, scroll: { offset: 0, bodyRows: 6 }, view: {} },
} as const

function ran(stdout: string) {
  return { value: { exitCode: 0, stdout, stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
}

test("an agent's metric read becomes a chart in the room with one key: a local command, no MCP call, no turn", async ($, on) => {
  const runs: Array<readonly string[]> = []
  const toasts: string[] = []
  let mcpCalls = 0
  on('tool.call', () => ({
    result: { content: [{ type: 'text', text: 'cloudwatch getMetricData: 1 series, 60 points, 17:00 to 18:00Z.' }] },
  }))
  on('process.run', ($, e) => {
    runs.push(e.argv)
    return ran('{"ok":true,"title":"DbCpuUtilization"}\n')
  })
  on('mcp.call', () => {
    mcpCalls += 1
    return { value: { content: [], isError: false } }
  })
  on('ui.toast', ($, e) => {
    toasts.push(e.text)
    return { value: undefined }
  })
  on('ui.render', () => ({ type: 'Text', props: {}, children: [''] }))

  const params = { MetricDataQueries: [{ Id: 'cpu', MetricStat: { Metric: { MetricName: 'DbCpuUtilization' } } }] }
  await $.tool.call({ tool: 'mcp__landfall__query_signals', tool_use_id: 't1', source: 'cloudwatch', operation: 'getMetricData', params } as never)

  const band = await $.ui.mount({ ...BAND, surface: 'terminal' })
  expect(await band.find({ type: 'Text', text: 'Chart ready: DbCpuUtilization' })).toBeDefined()
  await band.press({ key: 'pin' })

  expect(runs[0].slice(0, 4)).toEqual(['landfall', 'chart', '--host', 'claude-code'])
  const query = JSON.parse(runs[0][5] as string)
  expect(query).toEqual({ source: 'cloudwatch', operation: 'getMetricData', params, title: 'DbCpuUtilization' })
  expect(mcpCalls).toBe(0)
  expect(toasts).toEqual(['Chart added to your dashboard in the room: DbCpuUtilization'])
  await band.unmount()
  // Added once: the offer goes away.
  const again = await $.ui.mount({ ...BAND, surface: 'terminal' })
  expect(await again.find({ type: 'Text', text: /^Chart ready/ })).toBeUndefined()
})

test('a log read offers no chart', async ($, on) => {
  on('tool.call', () => ({ result: { content: [{ type: 'text', text: 'cloudwatch filterLogEvents: 12 events, 17:00 to 18:00Z.' }] } }))
  on('ui.render', () => ({ type: 'Text', props: {}, children: [''] }))
  await $.tool.call({ tool: 'mcp__landfall__query_signals', tool_use_id: 't2', source: 'cloudwatch', operation: 'filterLogEvents' } as never)
  const band = await $.ui.mount({ ...BAND, surface: 'terminal' })
  expect(await band.find({ type: 'Text', text: /^Chart ready/ })).toBeUndefined()
})

test('a refused chart says why, from /chart too', async ($, on) => {
  const toasts: string[] = []
  on('tool.call', () => ({ result: { content: [{ type: 'text', text: 'cloudwatch getMetricStatistics: 1 series, 5 points, 17:00 to 17:05Z.' }] } }))
  on('process.run', () => ran('{"ok":false,"error":"this checkout is not reading any room right now"}\n'))
  on('ui.toast', ($, e) => {
    toasts.push(e.text)
    return { value: undefined }
  })
  on('command.register', () => ({ value: undefined }))
  await $.tool.call({ tool: 'mcp__landfall__query_signals', tool_use_id: 't3', source: 'cloudwatch', operation: 'getMetricStatistics', params: { MetricName: 'CacheHitRate' } } as never)
  await $.command.run({ command: 'chart', args: '' })
  expect(toasts).toEqual(['Chart not added: this checkout is not reading any room right now'])
})

for (const surface of ['terminal', 'desktop'] as const) {
  test(`${surface}: key 4 adds the chart, a real button naming the metric off the terminal`, async ($, on) => {
    const runs: Array<readonly string[]> = []
    on('tool.call', () => ({ result: { content: [{ type: 'text', text: 'cloudwatch getMetricData: 1 series, 60 points, 17:00 to 18:00Z.' }] } }))
    on('process.run', ($, e) => {
      runs.push(e.argv)
      return ran('{"ok":true,"title":"5xxErrorRate"}\n')
    })
    on('ui.toast', () => ({ value: undefined }))
    on('ui.render', () => ({ type: 'Text', props: {}, children: [''] }))
    await $.tool.call({ tool: 'mcp__landfall__query_signals', tool_use_id: 'd1', source: 'cloudwatch', operation: 'getMetricData', params: { MetricName: '5xxErrorRate' } } as never)
    const band = await $.ui.mount({ ...BAND, surface })
    const pin = await band.find({ type: 'Button', key: 'pin' })
    expect(pin?.props.hotkey).toBe('4')
    if (surface === 'terminal') {
      expect(await band.find({ type: 'Text', text: 'Chart ready: 5xxErrorRate' })).toBeDefined()
      expect(pin?.props.label).toBe('add it to the room')
    } else {
      expect(pin?.props.label).toBe('Add chart: 5xxErrorRate')
      expect((await band.findAll({ type: 'Svg' }))[0]?.props.alt).toBe('Landfall')
    }
    await band.press({ key: 'pin' })
    expect(runs[0].slice(0, 4)).toEqual(['landfall', 'chart', '--host', 'claude-code'])
  })
}
