import { expect, test } from 'claude-code/testing'
import { BAND, baseRoom, pane, startWith } from './_room'

// status.lines as review #5 has it: {claimId, label, lineKey, owner, you, ageMs}.
const ROOM = baseRoom({
  lines: [
    { claimId: 'c1', label: 'eu-west-1 5xx', lineKey: 'eu-west-1-5xx', owner: 'dave', you: false, ageMs: 60000 },
    { claimId: 'c2', label: 'origin pool', lineKey: 'origin-pool', owner: 'alex', you: true, ageMs: 240000 },
  ],
})
const PANE = pane('landfall-lines')

for (const surface of ['terminal', 'desktop'] as const) {
  test(`the band shows who is on which line, one dim row (${surface})`, async ($, on) => {
    await startWith($, on, { rooms: [ROOM] })
    const band = await $.ui.mount({ ...BAND, surface } as never)
    const row = await band.find({ type: 'Text', text: 'Lines: eu-west-1 5xx (dave) · origin pool (you)' })
    expect(row?.props.dimColor).toBe(true)
  })

  test(`/lines claims a line through the CLI as the person, and releases your own (${surface})`, async ($, on) => {
    const w = await startWith($, on, {
      rooms: [ROOM],
      answers: { 'lines claim': { ok: true, claimId: 'c3', lineKey: 'us-east-1c-targets', label: 'us-east-1c targets' }, 'lines release': { ok: true } },
    })
    await $.command.run({ command: 'lines', args: '' })
    expect(w.opened).toEqual(['landfall-lines'])
    const p = await $.ui.mount({ ...PANE, surface } as never)
    expect(await p.find({ type: 'Text', text: 'eu-west-1 5xx' })).toBeDefined()
    expect(await p.find({ type: 'Text', text: 'dave · 1m' })).toBeDefined()
    // Only your own line can be released.
    expect(await p.find({ key: 'release-c1' })).toBeUndefined()
    await p.press({ key: 'claim' })
    await p.input({ key: 'claim-label', text: 'us-east-1c targets' })
    expect(w.runs[0]).toEqual(['landfall', 'lines', 'claim', '--room', 'k1', '--label', 'us-east-1c targets', '--host', 'claude-code'])
    await p.press({ key: 'release-c2' })
    expect(w.runs[1]).toEqual(['landfall', 'lines', 'release', '--room', 'k1', '--claim', 'c2', '--host', 'claude-code'])
    expect(w.toasts).toEqual(['You claimed the line us-east-1c targets. The room sees it.', 'You released the line origin pool.'])
    expect(w.counts.mcp).toBe(0)
  })

  test(`a refused claim says why (${surface})`, async ($, on) => {
    const w = await startWith($, on, { rooms: [ROOM], answers: { 'lines claim': { ok: false, error: 'Sign in to claim a line as yourself: run landfall login.' } } })
    await $.command.run({ command: 'lines', args: '' })
    const p = await $.ui.mount({ ...PANE, surface } as never)
    await p.press({ key: 'claim' })
    await p.input({ key: 'claim-label', text: 'dns' })
    expect(w.toasts).toEqual(['Line not claimed: Sign in to claim a line as yourself: run landfall login.'])
  })
}

test('on mobile there is no field, so the pane offers no claim', async ($, on) => {
  await startWith($, on, { rooms: [ROOM] })
  await $.command.run({ command: 'lines', args: '' })
  const p = await $.ui.mount({ ...PANE, surface: 'mobile' } as never)
  expect(await p.find({ key: 'claim' })).toBeUndefined()
  expect(await p.find({ type: 'Text', text: 'To claim a line here, type /lines and what you are on.' })).toBeDefined()
  expect(await p.find({ key: 'release-c2' })).toBeDefined()
})

test('no lines, no band row', async ($, on) => {
  await startWith($, on, { rooms: [baseRoom({})] })
  const band = await $.ui.mount({ ...BAND, surface: 'terminal' } as never)
  expect(await band.find({ type: 'Text', text: /^Lines:/ })).toBeUndefined()
})

test('/lines with text claims at once and answers in text', async ($, on) => {
  const w = await startWith($, on, { rooms: [ROOM], answers: { 'lines claim': { ok: true, claimId: 'c3', lineKey: 'dns', label: 'dns' } } })
  const answer = await $.command.run({ command: 'lines', args: 'dns' })
  expect(answer.text).toBe('You claimed the line dns. The room sees it.')
  expect(w.runs[0]).toEqual(['landfall', 'lines', 'claim', '--room', 'k1', '--label', 'dns', '--host', 'claude-code'])
})

test('/lines answers in text where no pane can be drawn', async ($, on) => {
  await startWith($, on, { rooms: [ROOM], placed: false })
  const answer = await $.command.run({ command: 'lines', args: '' })
  expect(answer.text).toBe('Lines of investigation\n  eu-west-1 5xx · dave · 1m\n  origin pool · you · 4m')
})
