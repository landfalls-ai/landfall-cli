import { expect, mock, test } from 'claude-code/testing'

// The room at a glance, as `landfall watch` sends it (narrate.RoomStatus).
function room(status: string, beacon: string) {
  return {
    roomKey: 'k1',
    incidentId: 'i1',
    displayId: 'Acme 82',
    title: 'cloudfront-5xx-high',
    slug: 'acme',
    connection: 'live',
    count: 2,
    addressed: 0,
    votesAwaited: 0,
    maxSeq: 60,
    digest: ['#59 chat.message [carol@acme.com] — rolling back web-edge now', '#60 claim.admitted — #55 corroborated and admitted'],
    status: {
      status,
      severity: 'SEV2',
      beacon,
      theory: 'the 14:32 web-edge deploy dropped healthy hosts in eu-west-1',
      people: [
        { name: 'alice', you: true, here: true, agents: [{ tool: 'Claude Code', label: 'alice-claude-code', here: true }] },
        { name: 'bob', here: true, agents: [{ tool: 'Claude Code', label: 'bob-claude-code', here: true, doing: 'querying ALB healthy hosts' }] },
        { name: 'carol', here: true, browser: true },
        { name: 'dave', here: false, agents: [{ tool: 'Codex', label: 'dave-codex', here: false }] },
      ],
    },
  }
}

function line(status: string, beacon: string) {
  return JSON.stringify({ type: 'rooms', line: '🔴 Acme 82 · 2 new', rooms: [room(status, beacon)] }) + '\n'
}

const HINT = {
  plugin: 'landfall',
  component: 'PromptHint',
  requestId: 'hint',
  viewport: { columns: 120, rows: 30 },
  props: { isDraft: false, isWorking: false, hint: '? for shortcuts' },
} as const

const PANE = {
  plugin: 'landfall',
  component: 'Pane',
  requestId: 'landfall-room',
  viewport: { columns: 120, rows: 40 },
  props: { bodyColumns: 110, bodyRows: 30, view: {} },
} as const

async function startWith($: any, on: any, lines: string[]) {
  const statuses: (string | undefined)[] = []
  const toasts: string[] = []
  // What the engine was asked to draw last, beneath the mod's rewrite.
  const drawn: { props?: any } = {}
  mock.clock(on)
  on('ui.render', ($: any, e: any) => {
    drawn.props = e.props
    return { type: 'Text', props: {}, children: [''] }
  })
  on('env.get', () => ({ value: undefined }))
  on('env.set', () => ({ value: undefined }))
  on('command.register', () => ({ value: undefined }))
  on('process.spawn', async function* () {
    for (const l of lines) yield { stream: 'stdout', text: l }
    return { code: 0, signal: null }
  })
  on('ui.status', ($: any, e: any) => {
    statuses.push(e.text)
    return { value: undefined }
  })
  on('ui.toast', ($: any, e: any) => {
    toasts.push(e.text)
    return { value: undefined }
  })
  on('session.start', () => ({ cwd: '/work' }))
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' } as never)
  for (let i = 0; i < 20 && statuses.length < lines.length; i++) await new Promise((r) => setTimeout(r, 5))
  return { statuses, toasts, drawn }
}

test('the status line says the status, severity, who is here and Beacon', async ($, on) => {
  const { statuses } = await startWith($, on, [line('investigating', 'investigating')])
  expect(statuses.at(-1)).toBe('🔴 Acme 82 · investigating · SEV2 · 3 here · Beacon investigating · 2 new')
})

test('a mitigated incident turns the dot yellow, a resolved one green', async ($, on) => {
  const { statuses } = await startWith($, on, [line('mitigated', ''), line('resolved', 'concluded')])
  expect(statuses[0]).toBe('🟡 Acme 82 · mitigated · SEV2 · 3 here · 2 new')
  expect(statuses[1]).toBe('🟢 Acme 82 · resolved · SEV2 · 3 here · Beacon concluded · 2 new')
})

test('a status change and Beacon ending are told once, the first sight is not', async ($, on) => {
  const { toasts } = await startWith($, on, [
    line('investigating', 'investigating'),
    line('investigating', 'investigating'),
    line('resolved', 'concluded'),
    line('resolved', 'concluded'),
  ])
  expect(toasts).toEqual(['Acme 82 · cloudfront-5xx-high is now resolved', 'Acme 82 · cloudfront-5xx-high: Beacon concluded'])
})

test('a mention is toasted once, however many events follow it', async ($, on) => {
  const mention = '#42 chat.message [carol@acme.com] — @alice does the edge jump at 14:32?  ← addressed to a person'
  const withMention = (maxSeq: number, extra: string[]) =>
    JSON.stringify({ type: 'rooms', line: '🔴 Acme 82 · 2 new', rooms: [{ ...room('investigating', ''), addressed: 1, maxSeq, digest: [mention, ...extra] }] }) + '\n'
  const { toasts } = await startWith($, on, [
    withMention(42, []),
    withMention(43, ['#43 claim.admitted — #41 corroborated and admitted']),
    withMention(44, ['#43 claim.admitted — #41 corroborated and admitted', '#44 chat.message [bob@acme.com] — on it']),
  ])
  expect(toasts).toEqual(['Acme 82 · cloudfront-5xx-high: carol: @alice does the edge jump at 14:32?'])
})

test('under the prompt: the others who are here, and where', async ($, on) => {
  const { drawn } = await startWith($, on, [line('investigating', 'investigating')])
  const hint = await $.ui.mount({ ...HINT, surface: 'terminal' } as never)
  expect(drawn.props.tail).toBe('Here: bob (Claude Code) · carol (war room)')
  await hint.unmount()

  // While the person types, the line is theirs.
  const typing = await $.ui.mount({ ...HINT, props: { ...HINT.props, isDraft: true }, surface: 'terminal' } as never)
  expect(drawn.props.tail).toBeUndefined()
  await typing.unmount()
})

test('/room opens on the room at a glance, everyone in it, then the news', async ($, on) => {
  await startWith($, on, [line('investigating', 'investigating')])
  const pane = await $.ui.mount({ ...PANE, surface: 'terminal' } as never)
  expect(await pane.find({ type: 'Text', text: 'investigating · SEV2 · Beacon investigating' })).toBeDefined()
  expect(await pane.find({ type: 'Text', text: 'Leading theory: the 14:32 web-edge deploy dropped healthy hosts in eu-west-1' })).toBeDefined()
  expect(await pane.find({ type: 'Text', text: 'In the room (3 here)' })).toBeDefined()
  expect(await pane.find({ type: 'Text', text: '● alice (you) · Claude Code' })).toBeDefined()
  expect(await pane.find({ type: 'Text', text: '● bob · Claude Code: querying ALB healthy hosts' })).toBeDefined()
  expect(await pane.find({ type: 'Text', text: '● carol · war room' })).toBeDefined()
  expect(await pane.find({ type: 'Text', text: '○ dave · Codex (away) · away' })).toBeDefined()
  expect(await pane.find({ type: 'Text', text: 'News · 2 new' })).toBeDefined()
})
