import { expect, mock, test } from 'claude-code/testing'

// One `landfall watch` room with people[].latest (cli-json.md §1, review #7).
const ROOM = {
  roomKey: 'k1',
  incidentId: 'i168',
  displayId: '168',
  title: 'cloudfront-5xx-high',
  slug: 'acme',
  connection: 'live',
  count: 0,
  addressed: 0,
  votesAwaited: 0,
  maxSeq: 233,
  digest: [],
  status: {
    status: 'investigating',
    severity: 'SEV2',
    people: [
      { name: 'alex', you: true, here: true, agents: [{ tool: 'Claude Code', label: 'alex-claude-code', here: true }] },
      { name: 'carol', here: true, browser: true, latest: { seq: 205, text: 'Rollback of web-edge v2.3.1 is ready; holding until eu-west-1 is read.', state: 'admitted', ageMs: 180000 } },
      { name: 'bob', here: true, agents: [{ tool: 'Claude Code', label: 'bob-claude-code', here: true, doing: 'comparing 5xx by region' }], latest: { seq: 212, text: 'The 5xx rise starts at 15:45Z, the same bucket as the deploy.', state: 'staged', ageMs: 120000 } },
      { name: 'alice', here: true, agents: [{ tool: 'Codex', label: 'alice-codex', here: true }], latest: { seq: 198, text: 'Origin pool exhausted in us-east-1.', state: 'contested', ageMs: 540000 } },
      { name: 'dave', here: false, agents: [{ tool: 'Codex', label: 'dave-codex', here: false }] },
    ],
  },
}

const PANE = {
  plugin: 'landfall',
  component: 'Pane',
  requestId: 'landfall-who',
  viewport: { columns: 120, rows: 40 },
  props: { bodyColumns: 110, bodyRows: 30, view: {} },
} as const

async function startWith($: any, on: any, placed = true) {
  const filled: string[] = []
  const opened: string[] = []
  let statuses = 0
  mock.clock(on)
  on('ui.render', () => ({ type: 'Text', props: {}, children: [''] }))
  on('env.get', () => ({ value: undefined }))
  on('env.set', () => ({ value: undefined }))
  on('command.register', () => ({ value: undefined }))
  on('process.spawn', async function* () {
    yield { stream: 'stdout', text: JSON.stringify({ type: 'rooms', line: '🔴 168 · 0 new', rooms: [ROOM] }) + '\n' }
    return { value: { code: 0, signal: null } }
  })
  on('ui.status', () => {
    statuses += 1
    return { value: undefined }
  })
  on('ui.toast', () => ({ value: undefined }))
  on('ui.close', () => ({ value: undefined }))
  on('ui.open', ($: any, e: any) => {
    opened.push(e.id)
    return { value: placed ? { isPlaced: true } : { isPlaced: false, reason: 'headless' } }
  })
  on('prompt.fill', ($: any, e: any) => {
    filled.push(e.text)
    return { isFilled: true }
  })
  on('session.start', () => ({ cwd: '/work' }))
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' } as never)
  for (let i = 0; i < 20 && statuses === 0; i++) await new Promise((r) => setTimeout(r, 5))
  return { filled, opened }
}

for (const surface of ['terminal', 'desktop'] as const) {
  test(`/who lists each person, where they work and their latest contribution with its state (${surface})`, async ($, on) => {
    const { opened } = await startWith($, on)
    await $.command.run({ command: 'who', args: '' })
    expect(opened).toEqual(['landfall-who'])
    const pane = await $.ui.mount({ ...PANE, surface } as never)
    expect(await pane.find({ type: 'Text', text: '● 4 here' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: 'carol' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: 'war room' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: 'Claude Code · comparing 5xx by region' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: '● admitted' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: '● staged' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: '● contested' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: '#205 · 3m' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: 'Nothing shared to the context yet' })).toBeDefined()
    // The pill carries the state's tone: contested is critical.
    expect((await pane.find({ type: 'Text', text: '● contested' }))?.props.color).toBe('#d03b3b')
    if (surface === 'desktop') expect(await pane.findAll({ type: 'Svg' })).not.toHaveLength(0)
  })

  test(`/who: a press quotes a contribution into the prompt as a draft (${surface})`, async ($, on) => {
    const { filled } = await startWith($, on)
    await $.command.run({ command: 'who', args: '' })
    const pane = await $.ui.mount({ ...PANE, surface } as never)
    // Here first by name: alice, bob, carol, then alex (you), then dave who is away.
    await pane.press({ key: 'quote-0' })
    expect(filled).toEqual(['> Origin pool exhausted in us-east-1.\n\n'])
  })
}

test('/who answers in text where no pane can be drawn', async ($, on) => {
  await startWith($, on, false)
  const answer = await $.command.run({ command: 'who', args: '' })
  expect(answer.text).toBe(
    [
      '168 · cloudfront-5xx-high · 4 here',
      '  ● alice · Codex',
      '    contested · #198 · 9m: "Origin pool exhausted in us-east-1."',
      '  ● bob · Claude Code · comparing 5xx by region',
      '    staged · #212 · 2m: "The 5xx rise starts at 15:45Z, the same bucket as the deploy."',
      '  ● carol · war room',
      '    admitted · #205 · 3m: "Rollback of web-edge v2.3.1 is ready; holding until eu-west-1 is read."',
      '  ● alex (you) · Claude Code',
      '    Nothing shared to the context yet',
      '  ○ dave · away',
      '    Nothing shared to the context yet',
    ].join('\n'),
  )
})

test('/who outside a room says how to get into one', async ($, on) => {
  on('command.register', () => ({ value: undefined }))
  const answer = await $.command.run({ command: 'who', args: '' })
  expect(answer.text).toBe('This folder is not in a war room. Open a share link from the room, or run /incidents to join one.')
})
