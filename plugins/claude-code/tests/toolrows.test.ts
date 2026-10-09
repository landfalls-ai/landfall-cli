import { expect, mock, test } from 'claude-code/testing'
import { labelIn } from './_label'

// Landfall tool calls drawn as Landfall rows. Drawing only: the stored result
// the model read is never touched.

const BRIEF = [
  'Landfall 168 · cloudfront-5xx-high · SEV2 · investigating',
  'Alert source: datadog',
  '',
  'Established:',
  '  #201 5xx over 2% for 5 minutes — Datadog',
  '     admitted by carol',
  '  #214 eu-west-1 explains the ~6% alert — bob',
  '',
  'Open:',
  '  #220 why origin-b — alice',
  '  #221 rollback ready? — carol',
  '  #222 eu-west-1 pool size — bob',
  '',
  'Participants: alice, bob, carol, dave',
  '',
  'Focus: web-edge (asked by carol)',
  '',
  'As of seq 222 (3s stale).',
].join('\n')

const SIGNALS = [
  'cloudwatch getMetricData: 1 series, 72 points, 2026-10-08 15:00:00Z to 21:00:00Z.',
  '5xxErrorRate (Percent): 72 points; min 0.1, max 6.1, last 0.2 at 21:00:00Z',
  '  15:00:00Z 0.1',
  '  15:05:00Z 0.2',
  '  15:10:00Z 0.1',
  '  … 66 points not shown …',
  '  20:50:00Z 4.2',
  '  20:55:00Z 1.1',
  '  21:00:00Z 0.2',
  'Trimmed: showing 6 of 72 points.',
].join('\n')

function use(tool: string, id: string, input: unknown, extra: Record<string, unknown> = {}) {
  return {
    plugin: 'landfall',
    component: 'ToolUse',
    requestId: id,
    viewport: { columns: 100, rows: 30 },
    props: { tool_use_id: id, tool, input, isRunning: false, isErrored: false, isInterrupted: false, ...extra },
  }
}

function result(tool: string, id: string, text: string, extra: Record<string, unknown> = {}) {
  return {
    plugin: 'landfall',
    component: 'ToolResult',
    requestId: id,
    viewport: { columns: 100, rows: 30 },
    props: { tool_use_id: id, tool, output: { content: [{ type: 'text', text }] }, isErrored: false, ...extra },
  }
}

function engineRow(on: any) {
  on('ui.render', () => ({ type: 'Text', props: {}, children: ['engine row'] }))
}

for (const surface of ['terminal', 'desktop'] as const) {
  test(`${surface}: get_brief draws as the incident header and what the room holds`, async ($, on) => {
    engineRow(on)
    const call = await $.ui.mount({ ...use('mcp__landfall__get_brief', 'b1', {}), surface } as never)
    expect(await call.find({ type: 'Text', text: 'Landfall' })).toBeDefined()
    expect(await call.find({ type: 'Text', text: 'brief' })).toBeDefined()
    const row = await $.ui.mount({ ...result('mcp__landfall__get_brief', 'b1', BRIEF), surface } as never)
    expect(await row.find({ type: 'Text', text: 'Landfall 168 cloudfront-5xx-high' })).toBeDefined()
    expect(labelIn(await row.drawn(), 'SEV2')).toBeDefined()
    expect(labelIn(await row.drawn(), 'investigating')).toBeDefined()
    expect(await row.find({ type: 'Text', text: 'established 2 · open 3 · 4 people · focus: web-edge' })).toBeDefined()
    expect(await row.find({ type: 'Text', text: 'engine row' })).toBeUndefined()
  })

  test(`${surface}: query_signals draws the read, a sparkline and the chart key it offers`, async ($, on) => {
    const runs: Array<readonly string[]> = []
    on('tool.call', () => ({ result: { content: [{ type: 'text', text: SIGNALS }] } }))
    on('process.run', ($: any, e: any) => {
      runs.push(e.argv)
      return { value: { exitCode: 0, stdout: '{"ok":true,"title":"5xxErrorRate"}\n', stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
    })
    on('ui.toast', () => ({ value: undefined }))
    engineRow(on)
    const input = { source: 'cloudwatch', operation: 'getMetricData', params: { MetricDataQueries: [{ MetricStat: { Metric: { MetricName: '5xxErrorRate' } } }] } }
    await $.tool.call({ tool: 'mcp__landfall__query_signals', tool_use_id: 'q1', ...input } as never)

    const call = await $.ui.mount({ ...use('mcp__landfall__query_signals', 'q1', input), surface } as never)
    expect(await call.find({ type: 'Text', text: 'query' })).toBeDefined()
    expect(await call.find({ type: 'Text', text: 'cloudwatch 5xxErrorRate' })).toBeDefined()

    const row = await $.ui.mount({ ...result('mcp__landfall__query_signals', 'q1', SIGNALS), surface } as never)
    if (surface === 'terminal') {
      expect(await row.find({ type: 'Raster' })).toBeDefined()
      expect(await row.find({ type: 'Text', text: '6.1% · last 0.2% · 72 points' })).toBeDefined()
      expect(await row.find({ type: 'Text', text: '4 add' })).toBeDefined()
    } else {
      expect((await row.find({ type: 'Svg' }))?.props.alt).toBe('5xxErrorRate')
      expect(await row.find({ type: 'Text', text: 'peak 6.1% · last 0.2% · 72 points' })).toBeDefined()
      await row.press({ key: 'sig-add' })
      expect(runs[0].slice(0, 4)).toEqual(['landfall', 'chart', '--host', 'claude-code'])
    }
  })

  test(`${surface}: share_with_room says what became of the share`, async ($, on) => {
    engineRow(on)
    const said = { kind: 'finding', text: 'eu-west-1 explains the ~6% alert' }
    const call = await $.ui.mount({ ...use('mcp__landfall__share_with_room', 's1', said), surface } as never)
    expect(await call.find({ type: 'Text', text: '"eu-west-1 explains the ~6% alert"' })).toBeDefined()

    const sent = await $.ui.mount({ ...result('mcp__landfall__share_with_room', 's1', 'shared — the room will have this shortly. Carry on; nothing to follow up.'), surface } as never)
    expect(await sent.find({ text: /sent to the room/ })).toBeDefined()

    const held = await $.ui.mount({
      ...result('mcp__landfall__share_with_room', 's2', 'held — nothing left this machine. This names the person\'s working directory (shop), and they have not allowed it.'),
      surface,
    } as never)
    expect(await held.find({ text: /held on this machine/ })).toBeDefined()
    expect(await held.find({ type: 'Text', text: /landfall allow-cwd/ })).toBeDefined()

    const gated = await $.ui.mount({ ...result('mcp__landfall__share_with_room', 's3', 'Staged as #214: held for review, needs 1 more position.'), surface } as never)
    expect(await gated.find({ text: /held for review/ })).toBeDefined()
    expect(await gated.find({ type: 'Text', text: 'needs 1 more position · #214' })).toBeDefined()
  })

  test(`${surface}: other Landfall tools draw one compact row; other tools are left alone`, async ($, on) => {
    engineRow(on)
    const call = await $.ui.mount({ ...use('mcp__landfall__join_war_room', 'j1', { link: 'https://app.landfalls.ai/j/abc' }), surface } as never)
    expect(await call.find({ type: 'Text', text: 'join' })).toBeDefined()
    const row = await $.ui.mount({ ...result('mcp__landfall__join_war_room', 'j1', 'Joined Landfall 168 · cloudfront-5xx-high.\nCall get_brief next.'), surface } as never)
    expect(await row.find({ type: 'Text', text: 'Joined Landfall 168 · cloudfront-5xx-high.' })).toBeDefined()

    const bash = await $.ui.mount({ ...use('Bash', 'x1', { command: 'ls' }), surface } as never)
    expect(await bash.find({ type: 'Text', text: 'engine row' })).toBeDefined()
  })

  test(`${surface}: an expanded group, an error and an interrupted call keep the engine's rows`, async ($, on) => {
    engineRow(on)
    const calls = [
      { tool_use_id: 'g1', tool: 'mcp__landfall__get_brief', input: {}, isRunning: false, isErrored: false, isInterrupted: false },
      { tool_use_id: 'g2', tool: 'mcp__landfall__get_updates', input: {}, isRunning: false, isErrored: false, isInterrupted: false },
    ]
    const group = (isExpanded: boolean) => ({
      plugin: 'landfall',
      component: 'ToolGroup',
      requestId: 'grp' + String(isExpanded),
      viewport: { columns: 100, rows: 30 },
      props: { calls, isActive: false, isExpanded },
      surface,
    })
    const folded = await $.ui.mount(group(false) as never)
    expect(await folded.find({ type: 'Text', text: 'brief, updates' })).toBeDefined()

    const open = await $.ui.mount(group(true) as never)
    expect(await open.find({ type: 'Text', text: 'engine row' })).toBeDefined()
    const inside = await $.ui.mount({ ...use('mcp__landfall__get_brief', 'g1', {}), surface } as never)
    expect(await inside.find({ type: 'Text', text: 'engine row' })).toBeDefined()

    const failed = await $.ui.mount({ ...use('mcp__landfall__get_brief', 'e1', {}, { isErrored: true }), surface } as never)
    expect(await failed.find({ type: 'Text', text: 'engine row' })).toBeDefined()
    const cut = await $.ui.mount({ ...use('mcp__landfall__get_brief', 'e2', {}, { isInterrupted: true }), surface } as never)
    expect(await cut.find({ type: 'Text', text: 'engine row' })).toBeDefined()
    const failedResult = await $.ui.mount({ ...result('mcp__landfall__get_brief', 'e3', 'not joined', { isErrored: true }), surface } as never)
    expect(await failedResult.find({ type: 'Text', text: 'engine row' })).toBeDefined()
  })
}

test('a share the room has gated reads its state and the votes it needs from the watch stream', async ($, on) => {
  const snap = {
    type: 'rooms',
    line: '🔴 Landfall 168 · 1 new',
    rooms: [
      {
        roomKey: 'k1',
        displayId: 'Landfall 168',
        title: 'cloudfront-5xx-high',
        count: 1,
        addressed: 0,
        votesAwaited: 0,
        maxSeq: 214,
        digest: [],
        votes: [{ claimSeq: 214, statement: 'eu-west-1 explains the ~6% alert', mine: true, positionsSoFar: 1, needed: 2, shortfall: 1 }],
        status: {
          status: 'investigating',
          severity: 'SEV2',
          people: [{ name: 'alice', you: true, here: true, latest: { seq: 214, text: 'eu-west-1 explains the ~6% alert', state: 'staged', ageMs: 30000 } }],
        },
      },
    ],
  }
  mock.clock(on)
  on('env.get', () => ({ value: undefined }))
  on('env.set', () => ({ value: undefined }))
  on('command.register', () => ({ value: undefined }))
  on('config.list', () => ({ value: [] }))
  let statuses = 0
  on('ui.status', () => {
    statuses += 1
    return { value: undefined }
  })
  on('ui.toast', () => ({ value: undefined }))
  on('process.spawn', async function* () {
    yield { stream: 'stdout', text: JSON.stringify(snap) + '\n' }
    return { value: { code: 0, signal: null } }
  })
  on('session.start', () => ({ cwd: '/work' }))
  engineRow(on)
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' } as never)
  for (let i = 0; i < 20 && statuses === 0; i++) await new Promise((r) => setTimeout(r, 5))

  for (const surface of ['terminal', 'desktop'] as const) {
    await $.ui.mount({ ...use('mcp__landfall__share_with_room', 'r' + surface, { text: 'eu-west-1 explains the ~6% alert' }), surface } as never)
    const row = await $.ui.mount({ ...result('mcp__landfall__share_with_room', 'r' + surface, 'shared — the room will have this shortly.'), surface } as never)
    expect(await row.find({ text: /held for review/ })).toBeDefined()
    expect(await row.find({ type: 'Text', text: 'needs 1 more position · #214' })).toBeDefined()
  }
})
