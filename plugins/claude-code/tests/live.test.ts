import { expect, mock, test } from 'claude-code/testing'

// Live panes and the dual connection (live.md FR-L2 to FR-L5), on the
// session's own clock: register.js ticks every 5 s ($.clock.every), and the
// mocked clock moves only when a test advances it. The watch stream is fed a
// line at a time and never ends, so no restart moves the clock either.

const ROOM = {
  roomKey: 'k168',
  incidentId: 'i168',
  displayId: 'Landfall 168',
  title: 'cloudfront-5xx-high',
  slug: 'acme',
  connection: 'live',
  count: 0,
  addressed: 0,
  votesAwaited: 0,
  maxSeq: 233,
  digest: [] as string[],
  widgetSeq: 231,
  newestWidget: { seq: 231, title: '5xx by target group', type: 'chart', by: 'bob' },
  status: { status: 'investigating', severity: 'SEV2', people: [] },
}

const WALL = {
  ok: true,
  sharedBy: 'carol',
  widgets: [{ id: 'w2', type: 'stat', title: 'Healthy origins', value: '4', unit: '/6', tone: 'warning' }],
  unavailable: [],
}
const WALL2 = { ...WALL, widgets: [{ id: 'w2', type: 'stat', title: 'Healthy origins', value: '3', unit: '/6', tone: 'serious' }] }
const EXPIRED = { ok: false, error: 'Your sign-in expired. Run landfall login.' }

const BAND = {
  plugin: 'landfall',
  component: 'AbovePrompt',
  requestId: 'band',
  viewport: { columns: 120, rows: 40 },
  props: { hasSurvey: false, isWorking: false, maxRows: 10, bodyColumns: 110, scroll: { offset: 0, bodyRows: 10 }, view: {} },
} as const
const VIEW = { columns: 140, rows: 50, isFullscreen: true }
const PANE_PROPS = { title: 'Pane', isFocused: true, bodyColumns: 100, placement: 'dock', scroll: { offset: 0, bodyRows: 60 }, view: {} } as const

function pane(requestId: string, surface: 'terminal' | 'desktop') {
  return { plugin: 'landfall', surface, component: 'Pane', requestId, viewport: VIEW, props: PANE_PROPS } as const
}

function line(rooms: unknown[], text = '🔴 Landfall 168 · 0 new') {
  return JSON.stringify({ type: 'rooms', line: text, rooms }) + '\n'
}

// session starts the mod with `first` on the watch stream; `answer` stands in
// for each `landfall <command>` run, by its argv.
async function session($: any, on: any, first: unknown[], answer: (argv: readonly string[]) => unknown) {
  const clock = mock.clock(on)
  const runs: string[][] = []
  const toasts: string[] = []
  const statuses: (string | undefined)[] = []
  const opened: string[] = []
  const closed: string[] = []
  const queue: string[] = [line(first)]
  let wake: (() => void) | null = null
  on('ui.render', () => ({ type: 'Text', props: {}, children: [''] }))
  on('env.get', () => ({ value: undefined }))
  on('env.set', () => ({ value: undefined }))
  on('command.register', () => ({ value: undefined }))
  on('config.list', () => ({ value: [] }))
  on('ui.status', ($: any, e: any) => {
    statuses.push(e.text)
    return { value: undefined }
  })
  on('ui.toast', ($: any, e: any) => {
    toasts.push(e.text)
    return { value: undefined }
  })
  on('ui.open', ($: any, e: any) => {
    opened.push(e.id)
    return { value: { isPlaced: true } }
  })
  on('ui.close', ($: any, e: any) => {
    closed.push(e.id)
    return { value: undefined }
  })
  on('ui.focus', () => ({ value: {} }))
  on('prompt.fill', () => ({ isFilled: true }))
  on('process.spawn', async function* () {
    for (;;) {
      while (queue.length > 0) yield { stream: 'stdout', text: queue.shift() as string }
      await new Promise<void>((r) => (wake = r))
    }
  })
  on('process.run', ($: any, e: any) => {
    runs.push([...e.argv])
    return { value: { exitCode: 0, stdout: JSON.stringify(answer(e.argv)) + '\n', stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
  })
  on('session.start', () => ({ cwd: '/work' }))
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' } as never)
  await clock.settle()
  for (let i = 0; i < 40 && statuses.length === 0; i++) await new Promise((r) => setTimeout(r, 5))
  const feed = async (rooms: unknown[], text?: string) => {
    queue.push(line(rooms, text))
    const w = wake
    wake = null
    w?.()
    await clock.settle()
  }
  const count = (cmd: string) => runs.filter((r) => r[1] === cmd).length
  return { clock, runs, toasts, statuses, opened, closed, feed, count }
}

for (const surface of ['terminal', 'desktop'] as const) {
  // The wall's own cadence (15 s while the console is open, a widget event at once, stale on a
  // failed read) is pinned in wall.test.ts, which drives the tab on a clock of its own.

  test(`${surface}: while the room reconnects the band, the panes and the status line say so`, async ($, on) => {
    const down = { ...ROOM, connection: 'disconnected' }
    const s = await session($, on, [down], () => WALL)
    expect(s.statuses.at(-1)).toBe('○ Landfall 168 · investigating · SEV2 · 0 new')
    let band = await $.ui.mount({ ...BAND, surface } as never)
    expect(await band.find({ type: 'Text', text: 'Reconnecting to the room…' })).toBeDefined()
    await band.unmount()

    const r = await $.ui.mount(pane('landfall-room', surface))
    expect(await r.find({ type: 'Text', text: 'Reconnecting to the room…' })).toBeDefined()
    await r.unmount()

    // Live again: the line clears, the dot fills, a band with no news is gone.
    await s.feed([ROOM])
    expect(s.statuses.at(-1)).toBe('🔴 Landfall 168 · investigating · SEV2 · 0 new')
    band = await $.ui.mount({ ...BAND, surface } as never)
    expect(await band.find({ type: 'Text', text: 'Reconnecting to the room…' })).toBeUndefined()
    await band.unmount()
  })

  test(`${surface}: a new widget is told once per 10 s, and the band offers w to open the wall`, async ($, on) => {
    const s = await session($, on, [ROOM], () => WALL)
    // The first sight of the room is not news.
    expect(s.toasts).toEqual([])

    await s.feed([{ ...ROOM, widgetSeq: 240, newestWidget: { seq: 240, title: 'p99 latency', type: 'chart', by: 'dana' } }])
    expect(s.toasts).toEqual(['New on the wall: p99 latency · by dana'])
    // A second within the gap waits for it.
    await s.feed([{ ...ROOM, widgetSeq: 241, newestWidget: { seq: 241, title: 'Replica lag', type: 'chart' } }])
    expect(s.toasts).toHaveLength(1)
    await s.clock.advance(5000)
    expect(s.toasts).toHaveLength(1)
    await s.clock.advance(5000)
    expect(s.toasts).toEqual(['New on the wall: p99 latency · by dana', 'New on the wall: Replica lag'])

    // The band offers the wall; w opens the console on it (which reads the wall when it draws it).
    const band = await $.ui.mount({ ...BAND, surface } as never)
    const key = await band.find({ type: 'Button', key: 'open-wall' })
    expect(key?.props.hotkey).toBe('w')
    if (surface === 'terminal') expect(key?.props.label).toBe('open the wall')
    await band.press({ key: 'open-wall' })
    expect(s.opened).toContain('landfall')
    await band.unmount()

    // A minute on, no offer.
    await s.feed([{ ...ROOM, widgetSeq: 241, newestWidget: { seq: 241, title: 'Replica lag', type: 'chart' } }])
    await s.clock.advance(65000)
    const later = await $.ui.mount({ ...BAND, surface } as never)
    expect(await later.find({ type: 'Button', key: 'open-wall' })).toBeUndefined()
  })

  test(`${surface}: both sides of the connection show, the session and the person's agent`, async ($, on) => {
    const withAgent = { ...ROOM, count: 1, maxSeq: 234, digest: ['#234 chat.message [bob@acme.com] — rolling back'], agent: { inRoom: true, label: 'claude-code', sinceMs: 60000 } }
    const s = await session($, on, [withAgent], () => WALL)
    let band = await $.ui.mount({ ...BAND, surface } as never)
    expect(await band.find({ type: 'Text', text: surface === 'terminal' ? 'agent ✓' : '● agent ✓' })).toBeDefined()
    await band.unmount()
    let r = await $.ui.mount(pane('landfall-room', surface))
    expect(await r.find({ type: 'Text', text: 'You: this session (Claude Code) · your agent: in the room' })).toBeDefined()
    await r.unmount()

    await s.feed([{ ...withAgent, agent: { inRoom: false } }])
    band = await $.ui.mount({ ...BAND, surface } as never)
    const out = await band.find({ type: 'Text', text: 'agent not in the room' })
    expect(out?.props.dimColor).toBe(true)
    await band.unmount()
    r = await $.ui.mount(pane('landfall-room', surface))
    expect(await r.find({ type: 'Text', text: 'You: this session (Claude Code) · your agent: not in the room yet' })).toBeDefined()
    await r.unmount()

    // An older CLI says nothing of the agent: neither word is drawn.
    const { agent, ...older } = withAgent
    await s.feed([older])
    band = await $.ui.mount({ ...BAND, surface } as never)
    expect(await band.find({ type: 'Text', text: /agent/ })).toBeUndefined()
  })
}

test('the load balancer board reads every 60 s, and the incident list and stakeholder updates every 30 s', async ($, on) => {
  const LB = { ok: true, account: '1', region: 'us-east-1', minutes: 5, fiveXxBy: 'targetGroup', loadBalancers: [] }
  const s = await session($, on, [ROOM], (argv) => {
    if (argv[1] === 'lb') return LB
    if (argv[1] === 'incidents') return { ok: true, org: 'acme', incidents: [] }
    if (argv[1] === 'comms') return { ok: true, messages: [] }
    return WALL
  })
  await $.command.run({ command: 'lb', args: '' })
  await $.command.run({ command: 'incidents', args: '' })
  await $.command.run({ command: 'comms', args: '' })
  for (const surface of ['terminal', 'desktop'] as const) {
    for (const id of ['landfall-lb', 'landfall-incidents', 'landfall-comms']) {
      const p = await $.ui.mount(pane(id, surface))
      expect(await p.find({ type: 'Text', text: 'live · updated 0s ago' })).toBeDefined()
      await p.unmount()
    }
  }
  expect([s.count('lb'), s.count('incidents'), s.count('comms')]).toEqual([1, 1, 1])
  await s.clock.advance(30000)
  expect([s.count('lb'), s.count('incidents'), s.count('comms')]).toEqual([1, 2, 2])
  await s.clock.advance(30000)
  expect([s.count('lb'), s.count('incidents'), s.count('comms')]).toEqual([2, 3, 3])
  // Closing one stops it alone.
  const inc = await $.ui.mount(pane('landfall-incidents', 'terminal'))
  await inc.press({ key: 'close' })
  await inc.unmount()
  await s.clock.advance(30000)
  expect([s.count('lb'), s.count('incidents'), s.count('comms')]).toEqual([2, 3, 4])
})

test('the timeline reads the newest page on a new event and lays it over the rows shown', async ($, on) => {
  const ev = (seq: number, text: string) => ({ seq, at: '2026-10-08T15:52:00Z', kind: 'other', type: 'chat.message', glyph: '·', tone: 'neutral', text, who: '', detail: '' })
  let page = [ev(201, 'Triggered'), ev(204, 'Beacon at step 3')]
  const s = await session($, on, [ROOM], (argv) => (argv[1] === 'timeline' ? { ok: true, events: page, hasMore: false, oldestSeq: page[0].seq } : WALL))
  await $.command.run({ command: 'timeline', args: '' })
  for (const surface of ['terminal', 'desktop'] as const) {
    const p = await $.ui.mount(pane('landfall-timeline', surface))
    expect(await p.find({ type: 'Button', key: 'ev-204' })).toBeDefined()
    expect(await p.find({ type: 'Text', text: 'live · updated 0s ago' })).toBeDefined()
    await p.unmount()
  }
  expect(s.count('timeline')).toBe(1)
  // Ticks with no new event read nothing.
  await s.clock.advance(20000)
  expect(s.count('timeline')).toBe(1)
  // A new event: the newest page again, its run folded anew into one row.
  page = [ev(201, 'Triggered'), ev(238, 'Beacon at step 6'), ev(240, 'bob: rolling back')]
  await s.feed([{ ...ROOM, maxSeq: 240 }])
  expect(s.count('timeline')).toBe(2)
  for (const surface of ['terminal', 'desktop'] as const) {
    const p = await $.ui.mount(pane('landfall-timeline', surface))
    const rows = (await p.findAll({ type: 'Button' })).filter((b) => /^ev-/.test(String(b.key)))
    expect(rows.map((b) => b.key)).toEqual(['ev-201', 'ev-238', 'ev-240'])
    await p.unmount()
  }
})

test('the topology reads the wall again on a widget event, not on every event', async ($, on) => {
  const s = await session($, on, [ROOM], () => WALL)
  await $.command.run({ command: 'topology', args: '' })
  const p = await $.ui.mount(pane('landfall-topology', 'terminal'))
  expect(s.count('wall')).toBe(1)
  await s.feed([{ ...ROOM, maxSeq: 236 }])
  await s.clock.advance(30000)
  expect(s.count('wall')).toBe(1)
  await s.feed([{ ...ROOM, maxSeq: 237, widgetSeq: 237 }])
  expect(s.count('wall')).toBe(2)
  expect(await p.find({ type: 'Text', text: 'live · updated 0s ago' })).toBeDefined()
})

test('mobile and vscode draw the reconnecting line, the agent word and the wall offer too', async ($, on) => {
  const down = { ...ROOM, connection: 'connecting', agent: { inRoom: true } }
  const s = await session($, on, [down], () => WALL)
  await s.feed([{ ...down, widgetSeq: 250, newestWidget: { seq: 250, title: 'Replica lag', type: 'chart' } }])
  expect(s.toasts).toEqual(['New on the wall: Replica lag'])
  for (const surface of ['mobile', 'vscode'] as const) {
    const band = await $.ui.mount({ ...BAND, surface } as never)
    expect(await band.find({ type: 'Text', text: 'Reconnecting to the room…' })).toBeDefined()
    expect(await band.find({ type: 'Text', text: '● agent ✓' })).toBeDefined()
    expect(await band.find({ type: 'Button', key: 'open-wall' })).toBeDefined()
    expect(await band.find({ type: 'Input' })).toBeUndefined()
    await band.unmount()
  }
})
