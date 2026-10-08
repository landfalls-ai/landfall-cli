import { expect, test } from 'claude-code/testing'
import { BAND, baseRoom, pane, startWith } from './_room'

// A live run in the Claude desktop app: the person's daemon still held an
// older, resolved room (166) from an earlier run beside the current one (168).
// The stream lists rooms by key, so 166 came first, and every pane read it.
// The mod acts on the room this session's agent is in, then an open room
// before a resolved one, then the newest; and names that room with --room.
const OLD = {
  ...baseRoom({ status: 'resolved', severity: 'SEV2' }),
  roomKey: 'k166',
  incidentId: 'i166',
  displayId: 'Landfall 166',
  title: 'last-weeks-outage',
  maxSeq: 900,
  count: 2,
  digest: ['#899 chat.message [dana@acme.com] — all clear', '#900 status.changed [dana@acme.com] — resolved'],
  agent: { inRoom: false },
}
const NEW = {
  ...baseRoom({ theory: 'eu-west-1 drives the alert' }),
  roomKey: 'k168',
  displayId: 'Landfall 168',
  maxSeq: 40,
  agent: { inRoom: true, label: 'claude-code' },
}

const WALL = {
  ok: true,
  widgets: [
    {
      id: 'g1',
      type: 'geo',
      title: '5xx Error Rate by Region',
      points: [
        { place: 'eu-west-1', label: 'eu-west-1 · 13.1%', value: 13.1, unit: '%', tone: 'critical' },
        { place: 'us-east-1', value: 0.28, unit: '%', tone: 'good' },
      ],
    },
  ],
  unavailable: [],
}

const ANSWERS = {
  wall: WALL,
  lb: { ok: true, loadBalancers: [] },
  timeline: { ok: true, events: [] },
  comms: { ok: true, messages: [] },
  brain: { ok: true, items: [] },
}

const CONSOLE = (surface: string) => ({ ...pane('landfall'), surface, props: { title: 'Landfall', isFocused: true, bodyColumns: 84, placement: 'dock', scroll: { offset: 0, bodyRows: 48 }, view: {} } })

for (const surface of ['terminal', 'desktop'] as const) {
  test(`${surface}: every tab reads the room this session's agent is in, named with --room`, async ($, on) => {
    const { runs } = await startWith($, on, { rooms: [OLD, NEW], answers: { ...ANSWERS, artifacts: { ok: true, artifacts: [] } } })
    await $.command.run({ command: 'landfall', args: '' })
    // Each tab reads when the console draws it on that tab.
    for (const tab of ['wall', 'lb', 'timeline', 'comms', 'brain', 'topology']) {
      await $.command.run({ command: 'landfall', args: tab })
      await (await $.ui.mount(CONSOLE(surface) as never)).unmount()
    }
    const reads = runs.filter((argv) => ['wall', 'lb', 'timeline', 'comms', 'brain'].includes(argv[1]))
    expect(new Set(reads.map((r) => r[1]))).toEqual(new Set(['wall', 'lb', 'timeline', 'comms', 'brain']))
    for (const argv of reads) {
      const at = argv.indexOf('--room')
      expect(at).toBeGreaterThan(0)
      expect(argv[at + 1]).toBe('k168')
    }
    expect(runs.some((argv) => argv.includes('k166'))).toBe(false)

    // The console's header names the current room.
    const c = await $.ui.mount(CONSOLE(surface) as never)
    expect(await c.find({ type: 'Text', text: /Landfall 168/ })).toBeDefined()
    expect(await c.find({ type: 'Text', text: /Landfall 166/ })).toBeUndefined()
  })

  test(`${surface}: Home is the current room; the band is not about the resolved one`, async ($, on) => {
    await startWith($, on, { rooms: [OLD, NEW], answers: ANSWERS })
    await $.command.run({ command: 'landfall', args: '' })
    const c = await $.ui.mount(CONSOLE(surface) as never)
    expect(await c.find({ type: 'Text', text: /Landfall 168 · cloudfront-5xx-high/ })).toBeDefined()
    expect(await c.find({ type: 'Text', text: /last-weeks-outage/ })).toBeUndefined()
    expect(await c.find({ type: 'Text', text: /all clear/ })).toBeUndefined()
    const band = await $.ui.mount({ ...BAND, surface } as never)
    expect(await band.find({ type: 'Text', text: /Landfall 166/ })).toBeUndefined()
    expect(await band.find({ type: 'Text', text: /all clear/ })).toBeUndefined()
  })

  test(`${surface}: a geo place whose label already names it is said once`, async ($, on) => {
    await startWith($, on, { rooms: [NEW], answers: ANSWERS })
    await $.command.run({ command: 'landfall', args: 'wall' })
    const wall = await $.ui.mount(CONSOLE(surface) as never)
    expect(await wall.find({ type: 'Text', text: 'eu-west-1 · 13.1%' })).toBeDefined()
    expect(await wall.find({ type: 'Text', text: /eu-west-1 · eu-west-1/ })).toBeUndefined()
    expect(await wall.find({ type: 'Text', text: 'us-east-1' })).toBeDefined()
  })
}

test('/landfall home in text: the current room first, then one line for the room that is over', async ($, on) => {
  await startWith($, on, { rooms: [OLD, NEW], answers: ANSWERS, placed: false })
  const answer = await $.command.run({ command: 'landfall', args: 'home' })
  const lines = String(answer.text).split('\n')
  expect(lines[0]).toBe('Landfall 168 · cloudfront-5xx-high · 0 new')
  expect(lines.at(-1)).toBe('Earlier: Landfall 166 (resolved)')
  expect(answer.text).not.toContain('all clear')
})

test('with no agent in either room: the open incident before the resolved one, then the newest', async ($, on) => {
  const old = { ...OLD, agent: { inRoom: false } }
  const quiet = { ...NEW, roomKey: 'k167', displayId: 'Landfall 167', maxSeq: 10, agent: { inRoom: false } }
  const busy = { ...NEW, roomKey: 'k168', maxSeq: 60, agent: { inRoom: false } }
  const { runs } = await startWith($, on, { rooms: [old, quiet, busy], answers: ANSWERS, placed: false })
  await $.command.run({ command: 'landfall', args: 'wall' })
  const argv = runs.find((a) => a[1] === 'wall')!
  expect(argv[argv.indexOf('--room') + 1]).toBe('k168')
})
