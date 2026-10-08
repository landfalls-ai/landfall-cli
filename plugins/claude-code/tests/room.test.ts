import { expect, mock, test } from 'claude-code/testing'
import { labelIn } from './_label'

// The band (spec §5.1) drawn for each surface: the terminal as text and keys, the desktop Code
// tab as one card with the mark, labels, avatars and real buttons; and the room at a glance in
// the console's Home.

const ROOM = {
  roomKey: 'k1',
  incidentId: 'i1',
  displayId: 'Landfall 168',
  title: 'cloudfront-5xx-high',
  slug: 'acme',
  connection: 'live',
  count: 3,
  addressed: 0,
  votesAwaited: 1,
  maxSeq: 214,
  digest: ['#213 chat.message [bob@acme.com] — eu-west-1 peaks at 6.1%', '#214 claim.admitted — eu-west-1 explains the ~6% alert'],
  status: {
    status: 'investigating',
    severity: 'SEV2',
    beacon: 'investigating',
    theory: 'eu-west-1 drives the alert',
    people: [
      { name: 'alice', you: true, here: true, agents: [{ tool: 'Claude Code', label: 'alice-claude-code', here: true }] },
      { name: 'bob', here: true, agents: [{ tool: 'Claude Code', label: 'bob-claude-code', here: true, doing: 'comparing 5xx by region' }] },
      { name: 'carol', here: true, browser: true },
      { name: 'dave', here: false, agents: [{ tool: 'Codex', label: 'dave-codex', here: false }] },
    ],
  },
}
const SNAPSHOT = JSON.stringify({ type: 'rooms', line: '🔴 Landfall 168 · 3 new', rooms: [ROOM] }) + '\n'

const BAND = {
  plugin: 'landfall',
  component: 'AbovePrompt',
  requestId: 'band',
  viewport: { columns: 120, rows: 40 },
  props: { hasSurvey: false, isWorking: false, maxRows: 10, bodyColumns: 110, scroll: { offset: 0, bodyRows: 10 }, view: {} },
} as const

const PANE = {
  plugin: 'landfall',
  component: 'Pane',
  requestId: 'landfall',
  viewport: { columns: 168, rows: 52, isFullscreen: true },
  props: { title: 'Landfall', isFocused: true, bodyColumns: 84, placement: 'dock', scroll: { offset: 0, bodyRows: 48 }, view: {} },
} as const

const HINT = {
  plugin: 'landfall',
  component: 'PromptHint',
  requestId: 'hint',
  viewport: { columns: 120, rows: 30 },
  props: { isDraft: false, isWorking: false, hint: '? for shortcuts' },
} as const

async function startWith($: any, on: any, surface: string, snapshot = SNAPSHOT, placed = true) {
  const filled: string[] = []
  const opened: string[] = []
  const closed: string[] = []
  const drawn: { props?: any } = {}
  mock.clock(on)
  on('ui.render', ($: any, e: any) => {
    drawn.props = e.props
    return { type: 'Text', props: {}, children: [''] }
  })
  on('env.get', () => ({ value: undefined }))
  on('env.set', () => ({ value: undefined }))
  on('command.register', () => ({ value: undefined }))
  on('config.list', () => ({ value: [] }))
  on('process.spawn', async function* () {
    yield { stream: 'stdout', text: snapshot }
    return { value: { code: 0, signal: null } }
  })
  let statuses = 0
  on('ui.status', () => {
    statuses += 1
    return { value: undefined }
  })
  on('ui.toast', () => ({ value: undefined }))
  on('ui.open', ($: any, e: any) => {
    opened.push(e.id)
    return { value: placed ? { isPlaced: true } : { isPlaced: false, reason: "headless" } }
  })
  on('ui.close', ($: any, e: any) => {
    closed.push(e.id)
    return { value: undefined }
  })
  on('prompt.fill', ($: any, e: any) => {
    filled.push(e.text)
    return { isFilled: true }
  })
  on('session.start', () => ({ cwd: '/work' }))
  await $.session.start({ surface, isInteractive: true, cwd: '/work' } as never)
  for (let i = 0; i < 20 && statuses === 0; i++) await new Promise((r) => setTimeout(r, 5))
  return { filled, opened, closed, drawn }
}


const VOTE = { claimSeq: 212, statement: 'The 5xx rise starts at 15:45Z, in the same 5-minute bucket as the web-edge deploy', authorIsAgent: true, authorHuman: 'bob', positionsSoFar: 1, needed: 2, shortfall: 1, expiresInMs: 250000, mine: false }
const WITH_VOTE = JSON.stringify({ type: 'rooms', line: '🔴 Landfall 168 · 3 new', rooms: [{ ...ROOM, votes: [VOTE] }] }) + '\n'

for (const surface of ['terminal', 'desktop'] as const) {
  test(`${surface}: the band names the room and its news, then catch up, the console and later`, async ($, on) => {
    const { filled, opened } = await startWith($, on, surface)
    const band = await $.ui.mount({ ...BAND, surface } as never)
    if (surface === 'terminal') expect(await band.find({ type: 'Text', text: '◆ Landfall 168 · cloudfront-5xx-high · 3 new' })).toBeDefined()
    else expect(await band.find({ type: 'Text', text: 'Landfall 168 · cloudfront-5xx-high' })).toBeDefined()
    expect(await band.find({ type: 'Text', text: /eu-west-1 explains the ~6% alert/ })).toBeDefined()
    expect((await band.find({ type: 'Button', key: 'catch-up' }))?.props.hotkey).toBe('1')
    expect((await band.find({ type: 'Button', key: 'console' }))?.props).toMatchObject({ hotkey: '2', label: surface === 'terminal' ? 'open the console' : 'Open the console' })
    expect((await band.find({ type: 'Button', key: 'later' }))?.props.hotkey).toBe('3')
    // The lines row left the band.
    expect(await band.find({ type: 'Text', text: /^Lines:/ })).toBeUndefined()
    await band.press({ key: 'catch-up' })
    expect(filled).toEqual(['Catch me up on what changed in the war room.'])
    await band.press({ key: 'console' })
    expect(opened).toEqual(['landfall'])
    await band.press({ key: 'later' })
    await band.unmount()
    // Set aside: the news rows go; the band has nothing left to say.
    const again = await $.ui.mount({ ...BAND, surface } as never)
    expect(await again.find({ type: 'Button', key: 'catch-up' })).toBeUndefined()
    expect(await again.find({ type: 'Button', key: 'console' })).toBeUndefined()
  })

  test(`${surface}: a waiting vote is a label on the head, the statement and its keys under it`, async ($, on) => {
    await startWith($, on, surface, WITH_VOTE)
    const band = await $.ui.mount({ ...BAND, surface } as never)
    expect(labelIn(await band.drawn(), 'vote waiting 4m 10s')).toBeDefined()
    expect(await band.find({ type: 'Text', text: /^bob's agent: “The 5xx rise starts at 15:45Z/ })).toBeDefined()
    // The vote's own head row is not drawn twice.
    expect(await band.find({ type: 'Text', text: /vote waiting · / })).toBeUndefined()
    if (surface === 'desktop') {
      // Catch up is primary only when no vote waits; the news line gives way to the vote.
      expect((await band.find({ type: 'Button', key: 'catch-up' }))?.props.variant).toBeUndefined()
      expect(await band.find({ type: 'Text', text: /eu-west-1 explains the ~6% alert/ })).toBeUndefined()
    }
  })

  test(`${surface}: the console's Home shows the room at a glance, everyone in it`, async ($, on) => {
    await startWith($, on, surface)
    await $.command.run({ command: 'landfall', args: '' })
    const pane = await $.ui.mount({ ...PANE, surface } as never)
    expect(await pane.find({ type: 'Text', text: surface === 'terminal' ? '◆ Landfall 168 · cloudfront-5xx-high' : 'Landfall 168 · cloudfront-5xx-high' })).toBeDefined()
    expect(labelIn(await pane.drawn(), 'SEV2')).toBeDefined()
    expect(await pane.find({ type: 'Text', text: 'Here · 3' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: /bob/ })).toBeDefined()
  })
}

test('terminal: under the prompt, the others who are here', async ($, on) => {
  const { drawn } = await startWith($, on, 'terminal')
  await $.ui.mount({ ...HINT, surface: 'terminal' } as never)
  expect(drawn.props.tail).toBe('Here: bob (Claude Code) · carol (war room)')
})

test('desktop: no who-is-here line under the prompt; the header carries the avatars', async ($, on) => {
  const { drawn } = await startWith($, on, 'desktop')
  await $.ui.mount({ ...HINT, surface: 'desktop' } as never)
  expect(drawn.props.hint).toBe('? for shortcuts')
  expect(drawn.props.tail).toBeUndefined()
})

// Measured live: "collab-bob (you) · Claude · Claude: investigating · Claude:
// investigating". Two sessions of one tool are one entry, its doing said once.
const TWICE = JSON.stringify({
  type: 'rooms',
  line: '',
  rooms: [
    {
      ...ROOM,
      status: {
        ...ROOM.status,
        people: [
          {
            name: 'collab-bob',
            you: true,
            here: true,
            agents: [
              { tool: 'Claude', label: 'bob-claude-desktop', here: true, doing: 'investigating' },
              { tool: 'Claude', label: 'bob-claude-code', here: true, doing: 'investigating' },
              { tool: 'Codex', label: 'bob-codex', here: false },
            ],
          },
        ],
      },
    },
  ],
}) + '\n'

test("/landfall home in text says each of a person's tools once, with its doing once", async ($, on) => {
  await startWith($, on, 'terminal', TWICE, false)
  const answer = await $.command.run({ command: 'landfall', args: 'home' })
  expect(String(answer.text)).toContain('● collab-bob (you) · Claude: investigating · Codex (away)')
  expect(String(answer.text)).not.toMatch(/Claude · Claude|Claude: investigating.*investigating/)
})

test('desktop: the band is one card with the Beacon mark, labels, avatars and a primary Catch up', async ($, on) => {
  await startWith($, on, 'desktop')
  const band = await $.ui.mount({ ...BAND, surface: 'desktop' } as never)
  const pictures = await band.findAll({ type: 'Svg' })
  expect(pictures[0]?.props.alt).toBe('Landfall')
  expect(labelIn(await band.drawn(), 'SEV2')).toBeDefined()
  expect(labelIn(await band.drawn(), '3 new')).toBeDefined()
  expect(pictures.map((a) => a.props.alt).filter((a) => a !== 'Landfall')).toEqual(['alice (you), here', 'bob, here', 'carol, here'])
  expect((await band.find({ type: 'Button', key: 'catch-up' }))?.props).toMatchObject({ label: 'Catch up', variant: 'primary', hotkey: '1' })
  // A label is never pressable-looking: no border.
  const labels = (await band.findAll({ type: 'Box' })).filter((b) => b.props.backgroundColor)
  expect(labels.length).toBeGreaterThan(0)
  expect(labels.every((b) => !b.props.borderStyle)).toBe(true)
})

test('mobile: the band is compact, with a count of who is here, and still opens the console', async ($, on) => {
  const { filled } = await startWith($, on, 'mobile')
  const band = await $.ui.mount({ ...BAND, surface: 'mobile' } as never)
  expect(await band.find({ type: 'Text', text: '3 here' })).toBeDefined()
  expect(await band.find({ type: 'Button', key: 'console' })).toBeDefined()
  await band.press({ key: 'catch-up' })
  expect(filled).toEqual(['Catch me up on what changed in the war room.'])
})

test('vscode: the band draws as on the desktop', async ($, on) => {
  await startWith($, on, 'vscode')
  const band = await $.ui.mount({ ...BAND, surface: 'vscode' } as never)
  expect((await band.findAll({ type: 'Svg' }))[0]?.props.alt).toBe('Landfall')
  expect(await band.find({ type: 'Button', key: 'catch-up' })).toBeDefined()
})
