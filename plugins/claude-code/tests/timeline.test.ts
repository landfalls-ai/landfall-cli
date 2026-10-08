import { expect, test } from 'claude-code/testing'

// `landfall timeline` as the Go CLI prints it (internal/cli/timeline.go
// timelineRow): oldest first, each row with its server `type`, and the page's
// `oldestSeq` beside `hasMore`.
const EVENTS = [
  { seq: 201, at: '2026-10-08T15:52:00Z', kind: 'status', type: 'incident.triggered', glyph: '▲', tone: 'critical', text: 'Triggered by Datadog: 5xx over 2% for 5 minutes', who: 'Datadog', detail: '' },
  { seq: 203, at: '2026-10-08T15:53:00Z', kind: 'status', type: 'status.changed', glyph: '●', tone: 'warning', text: 'Status set to investigating', who: 'carol', detail: 'war room' },
  { seq: 204, at: '2026-10-08T15:53:20Z', kind: 'beacon', type: 'agent.run.started', glyph: '◆', tone: 'violet', text: 'Beacon run 1 started', who: 'Beacon', detail: 'focus web-edge' },
  { seq: 206, at: '2026-10-08T15:58:00Z', kind: 'findings', type: 'claim.staged', glyph: '◇', tone: 'info', text: 'alice staged #206 origin pool exhausted', who: 'alice', detail: '' },
  { seq: 209, at: '2026-10-08T16:06:00Z', kind: 'findings', type: 'claim.admitted', glyph: '✓', tone: 'good', text: '#209 admitted: rollback ready', who: 'carol', detail: 'proposed by carol, accepted by bob and dave' },
  { seq: 212, at: '2026-10-08T16:09:00Z', kind: 'findings', type: 'claim.staged', glyph: '◇', tone: 'info', text: 'bob staged #212 5xx matches v2.3.1', who: 'bob', detail: '' },
  { seq: 214, at: '2026-10-08T16:10:00Z', kind: 'people', type: 'edge.participant.joined', glyph: '·', tone: 'neutral', text: 'dave joined from Claude Code', who: 'dave', detail: '' },
]
const OLDER = [{ seq: 150, at: '2026-10-08T15:40:00Z', kind: 'findings', type: 'edge.finding', glyph: '◇', tone: 'info', text: 'carol noted the CDN alarm', who: 'carol', detail: '' }]
const FINDINGS = EVENTS.filter((e) => e.kind === 'findings')

const PANE = { title: 'Timeline', isFocused: true, bodyColumns: 100, placement: 'dock', scroll: { offset: 0, bodyRows: 60 }, view: {} } as const
const VIEW = { columns: 140, rows: 50, isFullscreen: true }

function ran(stdout: string) {
  return { value: { exitCode: 0, stdout, stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
}

// The CLI as the tests want it: kind and --before honoured, as the contract says.
function answer(argv: readonly string[]) {
  const at = (flag: string) => {
    const i = argv.indexOf(flag)
    return i >= 0 ? argv[i + 1] : undefined
  }
  if (at('--before')) return { ok: true, events: OLDER, hasMore: false, oldestSeq: 150 }
  if (at('--kind') === 'findings') return { ok: true, events: FINDINGS, hasMore: false, oldestSeq: 206 }
  if (at('--kind') === 'other') return { ok: true, events: [], hasMore: false }
  return { ok: true, events: EVENTS, hasMore: true, oldestSeq: 201 }
}

test('/timeline draws one row per event, oldest first, glyphs in their tone', async ($, on) => {
  const runs: Array<readonly string[]> = []
  on('process.run', ($, e) => {
    runs.push(e.argv)
    return ran(JSON.stringify(answer(e.argv)) + '\n')
  })
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('command.register', () => ({ value: undefined }))
  for (const surface of ['terminal', 'desktop'] as const) {
    await $.command.run({ command: 'timeline', args: '' })
    expect(runs.at(-1)).toEqual(['landfall', 'timeline', '--host', 'claude-code', '--limit', '50'])
    const pane = await $.ui.mount({ plugin: 'landfall', surface, component: 'Pane', requestId: 'landfall-timeline', viewport: VIEW, props: PANE })
    expect(await pane.find({ type: 'Text', text: 'Timeline' })).toBeDefined()
    // The chips, "all" chosen.
    for (const key of ['kind-all', 'kind-findings', 'kind-status', 'kind-beacon', 'kind-people', 'kind-other']) expect(await pane.find({ type: 'Button', key })).toBeDefined()
    expect((await pane.find({ type: 'Button', key: 'kind-all' }))?.text).toBe('● all')
    // Rows sorted by seq, each a Button.
    const rows = (await pane.findAll({ type: 'Button' })).filter((b) => /^ev-/.test(String(b.key)))
    expect(rows.map((b) => b.key)).toEqual(['ev-201', 'ev-203', 'ev-204', 'ev-206', 'ev-209', 'ev-212', 'ev-214'])
    expect(rows[0].text).toBe('Triggered by Datadog: 5xx over 2% for 5 minutes')
    expect(rows[1].text).toBe('Status set to investigating · carol')
    expect((await pane.find({ type: 'Text', text: '▲' }))?.props.color).toBe('#d03b3b')
    expect((await pane.find({ type: 'Text', text: '✓' }))?.props.color).toBe('#0ca30c')
    expect(await pane.find({ type: 'Text', text: '15:52' })).toBeDefined()
    // More is offered while the CLI says there is more.
    expect(await pane.find({ type: 'Button', key: 'tl-more' })).toBeDefined()
    // The detail line shows where it draws.
    const detail = await pane.find({ type: 'Text', text: 'proposed by carol, accepted by bob and dave' })
    if (surface === 'terminal') expect(detail).toBeUndefined()
    else expect(detail).toBeDefined()
    await pane.unmount()
  }
})

test('a chip filters by kind, m reads further back, a press quotes the row as a draft', async ($, on) => {
  const runs: Array<readonly string[]> = []
  const fills: Array<{ text: string; mode: string }> = []
  let submitted = 0
  on('process.run', ($, e) => {
    runs.push(e.argv)
    return ran(JSON.stringify(answer(e.argv)) + '\n')
  })
  on('prompt.fill', ($, e) => {
    fills.push({ text: e.text, mode: e.mode })
    return { isFilled: true }
  })
  on('prompt.submit', () => {
    submitted += 1
    return { text: '' }
  })
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('command.register', () => ({ value: undefined }))
  for (const surface of ['terminal', 'desktop'] as const) {
    runs.length = 0
    fills.length = 0
    await $.command.run({ command: 'timeline', args: '' })
    const pane = await $.ui.mount({ plugin: 'landfall', surface, component: 'Pane', requestId: 'landfall-timeline', viewport: VIEW, props: PANE })

    await pane.press({ key: 'ev-201' })
    expect(fills).toEqual([{ text: '> 15:52 Triggered by Datadog: 5xx over 2% for 5 minutes\n\n', mode: 'insert' }])

    await pane.press({ key: 'tl-more' })
    expect(runs.at(-1)).toEqual(['landfall', 'timeline', '--host', 'claude-code', '--limit', '50', '--before', '201'])
    let keys = (await pane.findAll({ type: 'Button' })).filter((b) => /^ev-/.test(String(b.key))).map((b) => b.key)
    expect(keys[0]).toBe('ev-150')
    expect(keys).toHaveLength(8)
    expect(await pane.find({ type: 'Button', key: 'tl-more' })).toBeUndefined()

    await pane.press({ key: 'kind-findings' })
    expect(runs.at(-1)).toEqual(['landfall', 'timeline', '--host', 'claude-code', '--limit', '50', '--kind', 'findings'])
    keys = (await pane.findAll({ type: 'Button' })).filter((b) => /^ev-/.test(String(b.key))).map((b) => b.key)
    expect(keys).toEqual(['ev-206', 'ev-209', 'ev-212'])
    expect((await pane.find({ type: 'Button', key: 'kind-findings' }))?.text).toBe('● findings')

    await pane.press({ key: 'kind-all' })
    expect(submitted).toBe(0)
    await pane.unmount()
  }
})

test('/timeline findings starts on that kind, and answers in text where no pane can be placed', async ($, on) => {
  const runs: Array<readonly string[]> = []
  on('process.run', ($, e) => {
    runs.push(e.argv)
    return ran(JSON.stringify(answer(e.argv)) + '\n')
  })
  on('ui.open', () => ({ value: { isPlaced: false, reason: 'headless' } }))
  on('command.register', () => ({ value: undefined }))
  const out = await $.command.run({ command: 'timeline', args: 'findings' })
  expect(runs[0]).toEqual(['landfall', 'timeline', '--host', 'claude-code', '--limit', '50', '--kind', 'findings'])
  expect(out.text).toBe(
    [
      'Timeline · findings',
      '  15:58 ◇ alice staged #206 origin pool exhausted',
      '  16:06 ✓ #209 admitted: rollback ready · carol',
      '  16:09 ◇ bob staged #212 5xx matches v2.3.1',
    ].join('\n'),
  )
})

test('a failed read shows its sentence, in the pane and in text', async ($, on) => {
  let placed = true
  on('process.run', () => ran('{"ok":false,"error":"Your sign-in expired. Run landfall login."}\n'))
  on('ui.open', () => ({ value: placed ? { isPlaced: true } : { isPlaced: false, reason: 'headless' } }))
  on('command.register', () => ({ value: undefined }))
  await $.command.run({ command: 'timeline', args: '' })
  for (const surface of ['terminal', 'desktop'] as const) {
    const pane = await $.ui.mount({ plugin: 'landfall', surface, component: 'Pane', requestId: 'landfall-timeline', viewport: VIEW, props: PANE })
    expect(await pane.find({ type: 'Text', text: 'Your sign-in expired. Run landfall login.' })).toBeDefined()
    await pane.unmount()
  }
  placed = false
  const out = await $.command.run({ command: 'timeline', args: '' })
  expect(out.text).toBe('Your sign-in expired. Run landfall login.')
})

test('the timeline draws on vscode and mobile too', async ($, on) => {
  on('process.run', ($, e) => ran(JSON.stringify(answer(e.argv)) + '\n'))
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('command.register', () => ({ value: undefined }))
  await $.command.run({ command: 'timeline', args: '' })
  for (const surface of ['vscode', 'mobile'] as const) {
    const pane = await $.ui.mount({ plugin: 'landfall', surface, component: 'Pane', requestId: 'landfall-timeline', viewport: { columns: 60, rows: 40 }, props: { ...PANE, bodyColumns: 50 } })
    expect(await pane.find({ type: 'Button', key: 'ev-201' })).toBeDefined()
    await pane.unmount()
  }
})

test('the other chip asks the CLI for the rows no other chip covers', async ($, on) => {
  const runs: Array<readonly string[]> = []
  on('process.run', ($, e) => {
    runs.push(e.argv)
    return ran(JSON.stringify(answer(e.argv)) + '\n')
  })
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('command.register', () => ({ value: undefined }))
  await $.command.run({ command: 'timeline', args: '' })
  const pane = await $.ui.mount({ plugin: 'landfall', surface: 'terminal', component: 'Pane', requestId: 'landfall-timeline', viewport: VIEW, props: PANE })
  await pane.press({ key: 'kind-other' })
  expect(runs.at(-1)).toEqual(['landfall', 'timeline', '--host', 'claude-code', '--limit', '50', '--kind', 'other'])
  expect(await pane.find({ type: 'Text', text: 'Nothing of this kind yet.' })).toBeDefined()
})
