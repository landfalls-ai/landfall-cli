import { expect, test } from 'claude-code/testing'

// `landfall incidents` as the contract (cli-json.md §3, review #6) has it:
// here/hereCount/beacon only for the rooms this session is in.
const LIST = {
  ok: true,
  org: 'acme',
  incidents: [
    { incidentId: 'i165', displayId: '165', title: 'cdn-cert-renewal', severity: 'sev4', status: 'mitigated', ageMs: 7200000, joined: false, roomKey: '', webUrl: 'https://app.landfalls.ai/o/acme/incidents/i165' },
    { incidentId: 'i171', displayId: '171', title: 'checkout-latency-p99', severity: 'sev3', status: 'identified', ageMs: 1080000, joined: false, roomKey: '', webUrl: 'https://app.landfalls.ai/o/acme/incidents/i171' },
    { incidentId: 'i172', displayId: '172', title: 'orders-db-replica-lag', severity: 'sev1', status: 'open', ageMs: 180000, joined: false, roomKey: '', webUrl: 'https://app.landfalls.ai/o/acme/incidents/i172' },
    { incidentId: 'i168', displayId: '168', title: 'cloudfront-5xx-high', severity: 'sev2', status: 'investigating', ageMs: 2520000, joined: true, roomKey: 'k1', here: ['carol', 'bob', 'alice', 'you'], hereCount: 4, beacon: 'running', webUrl: 'https://app.landfalls.ai/o/acme/incidents/i168' },
  ],
}

const PANE = {
  plugin: 'landfall',
  component: 'Pane',
  requestId: 'landfall-incidents',
  viewport: { columns: 120, rows: 40 },
  props: { bodyColumns: 110, bodyRows: 30, view: {} },
} as const

function ran(stdout: string) {
  return { value: { exitCode: 0, stdout, stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
}

function world(on: any, answers: Record<string, unknown>, placed = true) {
  const runs: string[][] = []
  const toasts: string[] = []
  const filled: string[] = []
  const opened: string[] = []
  const copied: string[] = []
  let mcpCalls = 0
  on('command.register', () => ({ value: undefined }))
  on('process.run', ($: any, e: any) => {
    runs.push([...e.argv])
    const answer = answers[e.argv[1]] ?? { ok: false, error: 'unexpected' }
    return ran('some log line\n' + JSON.stringify(answer) + '\n')
  })
  on('mcp.call', () => {
    mcpCalls += 1
    return { value: { content: [], isError: false } }
  })
  on('ui.open', ($: any, e: any) => {
    opened.push(e.id)
    return { value: placed ? { isPlaced: true } : { isPlaced: false, reason: 'headless' } }
  })
  on('ui.close', () => ({ value: undefined }))
  on('ui.toast', ($: any, e: any) => {
    toasts.push(e.text)
    return { value: undefined }
  })
  on('ui.copy', ($: any, e: any) => {
    copied.push(e.text)
    return { value: { isCopied: true } }
  })
  on('prompt.fill', ($: any, e: any) => {
    filled.push(e.text)
    return { isFilled: true }
  })
  return { runs, toasts, filled, opened, copied, mcp: () => mcpCalls }
}

for (const surface of ['terminal', 'desktop'] as const) {
  test(`/incidents lists the rooms you are in first, then the unjoined SEV1, and Enter joins through the CLI (${surface})`, async ($, on) => {
    const w = world(on, { incidents: LIST, join: { ok: true, roomKey: 'k2', displayId: '172', title: 'orders-db-replica-lag' } })
    await $.command.run({ command: 'incidents', args: '' })
    expect(w.opened).toEqual(['landfall-incidents'])
    expect(w.runs[0]).toEqual(['landfall', 'incidents', '--host', 'claude-code'])

    const pane = await $.ui.mount({ ...PANE, surface } as never)
    const buttons = (await pane.findAll({ type: 'Button' })).map((b) => b.key).filter((k) => k?.startsWith('inc-'))
    // Joined first, the SEV1 nobody here has joined next, then by severity.
    expect(buttons).toEqual(['inc-i168', 'inc-i172', 'inc-i171', 'inc-i165'])
    expect(await pane.find({ text: /you are in it · 4 here: carol, bob, alice, you · Beacon running/ })).toBeDefined()
    expect(await pane.find({ text: 'SEV1' })).toBeDefined()

    await pane.press({ key: 'inc-i172' })
    expect(w.runs[1]).toEqual(['landfall', 'join', '--incident', 'i172', '--host', 'claude-code'])
    expect(w.toasts).toEqual(['Joined 172 orders-db-replica-lag. Room news reaches this session from now on.'])
    expect(w.mcp()).toBe(0)
  })

  test(`/incidents: brief only drafts a prompt and never sends it (${surface})`, async ($, on) => {
    const w = world(on, { incidents: LIST })
    await $.command.run({ command: 'incidents', args: '' })
    const pane = await $.ui.mount({ ...PANE, surface } as never)
    await pane.press({ key: surface === 'terminal' ? 'brief' : 'brief-i172' })
    // On the terminal `b` acts on the focused incident: the first one, until the ring moves.
    expect(w.filled).toEqual([surface === 'terminal' ? 'Give me the brief for 168' : 'Give me the brief for 172'])
    expect(w.runs.length).toBe(1)
  })

  test(`/incidents shows the CLI's sentence when it cannot list (${surface})`, async ($, on) => {
    world(on, { incidents: { ok: false, error: 'Sign in to list incidents: run landfall login.' } })
    await $.command.run({ command: 'incidents', args: '' })
    const pane = await $.ui.mount({ ...PANE, surface } as never)
    expect(await pane.find({ type: 'Text', text: 'Sign in to list incidents: run landfall login.' })).toBeDefined()
  })
}

test('/incidents on the terminal: o copies the link of the focused incident, r reads again', async ($, on) => {
  const w = world(on, { incidents: LIST })
  await $.command.run({ command: 'incidents', args: '' })
  const pane = await $.ui.mount({ ...PANE, surface: 'terminal' } as never)
  await pane.press({ key: 'open' })
  expect(w.copied).toEqual(['https://app.landfalls.ai/o/acme/incidents/i168'])
  expect(await pane.find({ type: 'Link' })).toBeDefined()
  await pane.press({ key: 'refresh' })
  expect(w.runs.filter((r) => r[1] === 'incidents').length).toBe(2)
})

test('/incidents on the desktop: a joined room opens the room pane rather than joining again', async ($, on) => {
  const w = world(on, { incidents: LIST })
  await $.command.run({ command: 'incidents', args: '' })
  const pane = await $.ui.mount({ ...PANE, surface: 'desktop' } as never)
  expect((await pane.find({ key: 'inc-i168' }))?.props.label).toBe('Open the room')
  expect((await pane.find({ key: 'inc-i172' }))?.props.variant).toBe('primary')
  await pane.press({ key: 'inc-i168' })
  expect(w.opened).toEqual(['landfall-incidents', 'landfall-room'])
  expect(w.runs.length).toBe(1)
})

test('/incidents answers in text where no pane can be drawn', async ($, on) => {
  world(on, { incidents: LIST }, false)
  const answer = await $.command.run({ command: 'incidents', args: '' })
  expect(answer.text).toBe(
    [
      'Open incidents · acme',
      '  168 cloudfront-5xx-high · SEV2 · investigating · 42m · you are in it · 4 here: carol, bob, alice, you · Beacon running',
      '  172 orders-db-replica-lag · SEV1 · open · 3m · not joined',
      '  171 checkout-latency-p99 · SEV3 · identified · 18m · not joined',
      '  165 cdn-cert-renewal · SEV4 · mitigated · 2h · not joined',
    ].join('\n'),
  )
})

test('a failed join says why', async ($, on) => {
  const w = world(on, { incidents: LIST, join: { ok: false, error: 'Sign in to join: run landfall login.' } })
  await $.command.run({ command: 'incidents', args: '' })
  const pane = await $.ui.mount({ ...PANE, surface: 'terminal' } as never)
  await pane.press({ key: 'inc-i172' })
  expect(w.toasts).toEqual(['Not joined: Sign in to join: run landfall login.'])
})
