import { expect, mock, test } from 'claude-code/testing'

// The room drawn for each surface: the terminal as text and keys (its exact
// lines are pinned in landfall.test.ts and status.test.ts), the desktop Code
// tab as a card with the mark, pills, avatars and real buttons.

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
  requestId: 'landfall-room',
  viewport: { columns: 120, rows: 40 },
  props: { bodyColumns: 110, bodyRows: 30, view: {} },
} as const

const HINT = {
  plugin: 'landfall',
  component: 'PromptHint',
  requestId: 'hint',
  viewport: { columns: 120, rows: 30 },
  props: { isDraft: false, isWorking: false, hint: '? for shortcuts' },
} as const

async function startWith($: any, on: any, surface: string) {
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
    yield { stream: 'stdout', text: SNAPSHOT }
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
    return { value: { isPlaced: true } }
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

for (const surface of ['terminal', 'desktop'] as const) {
  test(`${surface}: the band names the room, its news and three keys that draft, open and set aside`, async ($, on) => {
    const { filled, opened } = await startWith($, on, surface)
    const band = await $.ui.mount({ ...BAND, surface } as never)
    expect(await band.find({ type: 'Text', text: /Landfall 168 · cloudfront-5xx-high/ })).toBeDefined()
    expect(await band.find({ type: 'Text', text: /eu-west-1 explains the ~6% alert/ })).toBeDefined()
    expect(await band.find({ type: 'Button', key: 'catch-up' })).toBeDefined()
    expect(await band.find({ type: 'Button', key: 'show' })).toBeDefined()
    await band.press({ key: 'catch-up' })
    expect(filled).toEqual(['Catch me up on what changed in the war room.'])
    await band.press({ key: 'show' })
    expect(opened).toEqual(['landfall-room'])
    if (surface === 'terminal') {
      // Unchanged: text and keys, no pictures.
      expect(await band.find({ type: 'Text', text: 'Landfall · Landfall 168 · cloudfront-5xx-high · 3 new · 1 vote awaited' })).toBeDefined()
    }
    await band.press({ key: 'later' })
    await band.unmount()
    const again = await $.ui.mount({ ...BAND, surface } as never)
    expect(await again.find({ type: 'Button', key: 'catch-up' })).toBeUndefined()
  })

  test(`${surface}: /room shows the room at a glance, everyone in it, then the news`, async ($, on) => {
    const { filled, closed } = await startWith($, on, surface)
    const pane = await $.ui.mount({ ...PANE, surface } as never)
    expect(await pane.find({ type: 'Text', text: /Leading theory: eu-west-1 drives the alert/ })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: 'In the room (3 here)' })).toBeDefined()
    expect(await pane.find({ text: /bob/ })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: /comparing 5xx by region/ })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: 'News · 3 new' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: /eu-west-1 peaks at 6.1%/ })).toBeDefined()
    await pane.press({ key: 'pane-catch-up' })
    expect(closed).toEqual(['landfall-room'])
    expect(filled).toEqual(['Catch me up on what changed in the war room.'])
  })

  test(`${surface}: under the prompt, the others who are here`, async ($, on) => {
    const { drawn } = await startWith($, on, surface)
    const hint = await $.ui.mount({ ...HINT, surface } as never)
    if (surface === 'terminal') {
      expect(drawn.props.tail).toBe('Here: bob (Claude Code) · carol (war room)')
    } else {
      // The desktop draws no `tail`: the mod draws the line itself.
      expect(await hint.find({ type: 'Text', text: '? for shortcuts' })).toBeDefined()
      expect(await hint.find({ type: 'Text', text: 'bob (Claude Code)' })).toBeDefined()
      expect(await hint.find({ type: 'Text', text: 'carol (war room)' })).toBeDefined()
    }
  })
}

test('desktop: the band is a card with the Beacon mark, pills, avatars and a primary Catch up', async ($, on) => {
  await startWith($, on, 'desktop')
  const band = await $.ui.mount({ ...BAND, surface: 'desktop' } as never)
  // An Svg has no key: the mark is the first picture, named by its alt.
  const pictures = await band.findAll({ type: 'Svg' })
  expect(pictures[0]?.props.alt).toBe('Landfall')
  expect(await band.find({ type: 'Text', text: '● SEV2' })).toBeDefined()
  expect(await band.find({ type: 'Text', text: '● investigating' })).toBeDefined()
  expect(await band.find({ type: 'Text', text: '● 3 new' })).toBeDefined()
  const avatars = await band.findAll({ type: 'Svg' })
  expect(avatars.map((a) => a.props.alt).filter((a) => a !== 'Landfall')).toEqual(['alice (you), here', 'bob, here', 'carol, here'])
  const catchUp = await band.find({ type: 'Button', key: 'catch-up' })
  expect(catchUp?.props).toMatchObject({ label: 'Catch up', variant: 'primary', hotkey: '1' })
  expect(await band.find({ type: 'Text', text: /1 vote awaited · Beacon investigating/ })).toBeDefined()
})

test('desktop: /room draws each person with an avatar and Close as the dismiss control', async ($, on) => {
  await startWith($, on, 'desktop')
  const pane = await $.ui.mount({ ...PANE, surface: 'desktop' } as never)
  const alts = (await pane.findAll({ type: 'Svg' })).map((a) => a.props.alt)
  expect(alts).toContain('dave, away')
  expect(alts).toContain('alice (you), here')
  expect((await pane.find({ type: 'Button', key: 'pane-close' }))?.props).toMatchObject({ role: 'dismiss' })
})

test('mobile: the band is compact, with a count of who is here and no pane key', async ($, on) => {
  const { filled } = await startWith($, on, 'mobile')
  const band = await $.ui.mount({ ...BAND, surface: 'mobile' } as never)
  expect(await band.find({ type: 'Text', text: '3 here' })).toBeDefined()
  expect(await band.find({ type: 'Button', key: 'show' })).toBeUndefined()
  await band.press({ key: 'catch-up' })
  expect(filled).toEqual(['Catch me up on what changed in the war room.'])
})

test('vscode: the band draws as on the desktop', async ($, on) => {
  await startWith($, on, 'vscode')
  const band = await $.ui.mount({ ...BAND, surface: 'vscode' } as never)
  expect((await band.findAll({ type: 'Svg' }))[0]?.props.alt).toBe('Landfall')
  expect(await band.find({ type: 'Button', key: 'catch-up' })).toBeDefined()
})
