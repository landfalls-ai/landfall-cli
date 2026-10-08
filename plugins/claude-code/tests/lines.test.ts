import { expect, test } from 'claude-code/testing'
import { BAND, baseRoom, startWith } from './_room'
import { Drawn, fakeIo, kitFor, openConsoleOn, setRooms, settle } from './_tab'
import * as linesLib from '../hooks/components/lines.js'
import * as rosterTab from '../hooks/components/roster.js'
import * as wallTab from '../hooks/components/wall.js'

// status.lines as review #5 has it: {claimId, label, lineKey, owner, you, ageMs}.
const ROOM = baseRoom({
  lines: [
    { claimId: 'c1', label: 'eu-west-1 5xx', lineKey: 'eu-west-1-5xx', owner: 'dave', you: false, ageMs: 60000 },
    { claimId: 'c2', label: 'origin pool', lineKey: 'origin-pool', owner: 'alex', you: true, ageMs: 240000 },
  ],
})

for (const surface of ['terminal', 'desktop'] as const) {
  test(`the band shows who is on which line, one dim row (${surface})`, async ($, on) => {
    await startWith($, on, { rooms: [ROOM] })
    const band = await $.ui.mount({ ...BAND, surface } as never)
    const row = await band.find({ type: 'Text', text: 'Lines: eu-west-1 5xx (dave) · origin pool (you)' })
    expect(row?.props.dimColor).toBe(true)
  })
}

test('no lines, no band row', async ($, on) => {
  await startWith($, on, { rooms: [baseRoom({})] })
  const band = await $.ui.mount({ ...BAND, surface: 'terminal' } as never)
  expect(await band.find({ type: 'Text', text: /^Lines:/ })).toBeUndefined()
})

function begin(room: unknown = ROOM) {
  rosterTab.reset()
  wallTab.reset()
  setRooms([room])
  openConsoleOn('people')
}

test('a claim runs the CLI as the person and says what became of it', async () => {
  begin()
  const io = fakeIo(() => ({ ok: true, claimId: 'c3', lineKey: 'us-east-1c-targets', label: 'us-east-1c targets' }))
  const got = await linesLib.claimLine(io as never, '  us-east-1c targets ')
  expect(io.runs[0]).toEqual(['lines', 'claim', '--room', 'k1', '--label', 'us-east-1c targets', '--host', 'claude-code'])
  expect(got.text).toBe('You claimed the line us-east-1c targets. The room sees it.')
  expect(got.ok).toBe(true)
  // `/landfall lines` with no label, and outside a room.
  expect((await linesLib.claimLine(io as never, '')).text).toBe('Say which line you are on: /landfall lines <label>.')
  setRooms([])
  expect((await linesLib.claimLine(io as never, 'dns')).ok).toBe(false)
  expect(io.runs).toHaveLength(1)
})

test('a refused claim says why; a line someone else holds says who has it', async () => {
  begin()
  const refused = fakeIo(() => ({ ok: false, error: 'Sign in to claim a line as yourself: run landfall login.' }))
  expect((await linesLib.claimLine(refused as never, 'dns')).text).toBe('Line not claimed: Sign in to claim a line as yourself: run landfall login.')
  // `landfall lines claim` when the line is someone else's (internal/cli/lines.go):
  // ok:false with the holder, the claim and the line key beside the sentence.
  const held = fakeIo(() => ({ ok: false, error: 'dave already holds this line. Help them, or claim another.', heldBy: 'dave', claimId: 'c1', lineKey: 'eu-west-1-5xx' }))
  expect((await linesLib.claimLine(held as never, 'eu-west-1 5xx')).text).toBe('dave already holds this line. Help them, or claim another.')
})

test('a release names the line, or says it was already released, or says why not', async () => {
  begin()
  const ok = fakeIo(() => ({ ok: true }))
  const line = { claimId: 'c2', label: 'origin pool' }
  expect((await linesLib.releaseLine(ok as never, line)).text).toBe('You released the line origin pool.')
  expect(ok.runs[0]).toEqual(['lines', 'release', '--room', 'k1', '--claim', 'c2', '--host', 'claude-code'])
  const twice = fakeIo(() => ({ ok: true, note: 'That line was already released.' }))
  expect((await linesLib.releaseLine(twice as never, line)).text).toBe('That line was already released.')
  const no = fakeIo(() => ({ ok: false, error: 'not yours' }))
  expect((await linesLib.releaseLine(no as never, line)).text).toBe('Line not released: not yours')
})

test('lines belong to the people who hold them; the rest are listed under Lines', async () => {
  const room = baseRoom({
    people: [
      { name: 'alex', you: true, here: true, agents: [] },
      { name: 'dave', here: true, agents: [] },
    ],
    lines: [...ROOM.status.lines, { claimId: 'c3', label: 'checkout db pool', owner: 'Beacon', you: false, ageMs: 840000 }],
  })
  const people = room.status.people as { name: string; you?: boolean }[]
  expect(linesLib.linesOf(room, people[1]).map((l: any) => l.label)).toEqual(['eu-west-1 5xx'])
  expect(linesLib.linesOf(room, people[0]).map((l: any) => l.label)).toEqual(['origin pool'])
  expect(linesLib.loneLines(room, people).map(linesLib.lineWords)).toEqual(['checkout db pool · Beacon · 14m'])
  expect(linesLib.linesWords(room)).toBe('eu-west-1 5xx (dave) · origin pool (you) · checkout db pool (Beacon)')
})

test('lines as text, where no pane can be drawn', async () => {
  expect(linesLib.linesText(ROOM)).toBe('Lines of investigation\n  eu-west-1 5xx · dave · 1m\n  origin pool · you · 4m')
  expect(linesLib.linesText(baseRoom({}))).toBe('Nobody has claimed a line yet. Type /landfall lines and what you are on to claim one.')
})

test('People draws the claim field where a field can be drawn, and a hint where it cannot', async () => {
  begin(baseRoom({ people: [{ name: 'alex', you: true, here: true }], lines: [] }))
  const io = fakeIo(() => ({ ok: false, error: 'unexpected' }))
  const pane = new Drawn(rosterTab.tab(kitFor('terminal'), io as never, 0, null))
  expect(pane.find({ key: 'claim-label' })).toBeUndefined()
  await new Drawn(rosterTab.keys(kitFor('terminal'), io as never)).press('claim')
  await settle()
  const field = new Drawn(rosterTab.tab(kitFor('desktop'), io as never, 0, null)).find({ key: 'claim-label' })
  expect(field?.props.submitLabel).toBe('Claim')
  expect(new Drawn(rosterTab.tab(kitFor('terminal'), io as never, 0, null)).find({ key: 'claim-label' })?.props.submitLabel).toBe('Enter: claim')
})
