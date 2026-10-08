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

for (const surface of ['terminal', 'desktop'] as const) {
  test(`${surface}: every pane reads the room this session's agent is in, named with --room`, async ($, on) => {
    const { runs } = await startWith($, on, { rooms: [OLD, NEW], answers: ANSWERS })
    // (Load balancers, timeline, comms and brain read the current room too; their tests name --room.)
    for (const command of ['topology']) {
      await $.command.run({ command, args: '' })
    }
    const reads = runs.filter((argv) => ['wall'].includes(argv[1]))
    expect(reads.length).toBeGreaterThanOrEqual(1)
    for (const argv of reads) {
      const at = argv.indexOf('--room')
      expect(at).toBeGreaterThan(0)
      expect(argv[at + 1]).toBe('k168')
    }
    expect(runs.some((argv) => argv.includes('k166'))).toBe(false)
  })

  test(`${surface}: /room lists the current room first and the resolved one on one dim line`, async ($, on) => {
    await startWith($, on, { rooms: [OLD, NEW], answers: ANSWERS })
    const p = await $.ui.mount({ ...pane('landfall-room'), surface } as never)
    expect(await p.find({ type: 'Text', text: /Landfall 168 · cloudfront-5xx-high/ })).toBeDefined()
    expect(await p.find({ type: 'Text', text: /Leading theory: eu-west-1 drives the alert/ })).toBeDefined()
    // The old room is named once, dim, and none of its news is drawn.
    const earlier = await p.find({ type: 'Text', text: 'Earlier: Landfall 166 (resolved)' })
    expect(earlier).toBeDefined()
    expect(earlier?.props.dimColor).toBe(true)
    expect(await p.find({ type: 'Text', text: /last-weeks-outage/ })).toBeUndefined()
    expect(await p.find({ type: 'Text', text: /all clear/ })).toBeUndefined()
    // ...and the band is not about it either: its news is not the band's.
    const band = await $.ui.mount({ ...BAND, surface } as never)
    expect(await band.find({ type: 'Text', text: /Landfall 166/ })).toBeUndefined()
    expect(await band.find({ type: 'Text', text: /all clear/ })).toBeUndefined()
  })
}

test('/room in text: the current room first, then one line for the room that is over', async ($, on) => {
  await startWith($, on, { rooms: [OLD, NEW], answers: ANSWERS, placed: false })
  const answer = await $.command.run({ command: 'room', args: '' })
  const lines = String(answer.text).split('\n')
  expect(lines[0]).toBe('Landfall 168 · cloudfront-5xx-high · 0 new')
  expect(lines.at(-1)).toBe('Earlier: Landfall 166 (resolved)')
  expect(answer.text).not.toContain('all clear')
})
