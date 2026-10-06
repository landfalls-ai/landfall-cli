import { expect, test } from 'claude-code/testing'

const BAND = {
  plugin: 'landfall',
  component: 'AbovePrompt',
  requestId: 'band',
  viewport: { columns: 100, rows: 30 },
  props: { hasSurvey: false, isWorking: false, maxRows: 6, bodyColumns: 90, scroll: { offset: 0, bodyRows: 6 }, view: {} },
} as const

test("an agent's metric read becomes a chart in the room with one key, no turn", async ($, on) => {
  const calls: unknown[] = []
  const toasts: string[] = []
  on('tool.call', () => ({
    result: { content: [{ type: 'text', text: 'cloudwatch getMetricData: 1 series, 60 points, 17:00 to 18:00Z.' }] },
  }))
  on('mcp.call', ($, e) => {
    calls.push(e)
    return { value: { content: [{ type: 'text', text: 'shared' }], isError: false } }
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

  const sent = JSON.stringify(calls[0])
  expect(sent).toContain('share_with_room')
  expect(sent).toContain('"fromQuery":{"source":"cloudwatch","operation":"getMetricData"')
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

test('a refused chart says why', async ($, on) => {
  const toasts: string[] = []
  on('tool.call', () => ({ result: { content: [{ type: 'text', text: 'cloudwatch getMetricStatistics: 1 series, 5 points, 17:00 to 17:05Z.' }] } }))
  on('mcp.call', () => ({ value: { content: [{ type: 'text', text: 'not joined to a war room yet' }], isError: true } }))
  on('ui.toast', ($, e) => {
    toasts.push(e.text)
    return { value: undefined }
  })
  on('command.register', () => ({ value: undefined }))
  await $.tool.call({ tool: 'mcp__landfall__query_signals', tool_use_id: 't3', source: 'cloudwatch', operation: 'getMetricStatistics', params: { MetricName: 'CacheHitRate' } } as never)
  await $.command.run({ command: 'chart', args: '' })
  expect(toasts).toEqual(['Chart not added: not joined to a war room yet'])
})

test("the mod never approves a share it did not raise: the agent's own share_with_room still asks", async ($, on) => {
  on('tool.check', () => ({ decision: 'ask' }))
  const verdict = await $.tool.check({
    tool: 'mcp__landfall__share_with_room',
    input: { text: 'x', kind: 'widget', fromQuery: { source: 'cloudwatch', operation: 'getMetricData' } },
  } as never)
  expect(verdict.decision).toBe('ask')
})

test('pressing 4 needs no second approval: the mod allows exactly its own share while it is in flight', async ($, on) => {
  const verdicts: string[] = []
  on('tool.call', () => ({ result: { content: [{ type: 'text', text: 'cloudwatch getMetricData: 1 series, 60 points, 17:00 to 18:00Z.' }] } }))
  on('tool.check', () => ({ decision: 'ask' }))
  on('ui.toast', () => ({ value: undefined }))
  on('ui.render', () => ({ type: 'Text', props: {}, children: [''] }))
  on('mcp.call', async ($m, e) => {
    // Claude Code asks tool.check about the plugin's call before running it.
    const args = (e as { args?: Record<string, unknown> }).args ?? (e as Record<string, unknown>)
    const v = await $.tool.check({ tool: 'mcp__landfall__share_with_room', input: args } as never)
    verdicts.push(v.decision)
    return { value: { content: [{ type: 'text', text: 'shared' }], isError: false } }
  })
  await $.tool.call({ tool: 'mcp__landfall__query_signals', tool_use_id: 't4', source: 'cloudwatch', operation: 'getMetricData', params: { MetricName: 'DbCpuUtilization' } } as never)
  const band = await $.ui.mount({ ...BAND, surface: 'terminal' })
  await band.press({ key: 'pin' })
  expect(verdicts).toEqual(['allow'])
})
