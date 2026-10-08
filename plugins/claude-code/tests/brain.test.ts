import { expect, test } from 'claude-code/testing'
import { baseRoom, pane, startWith } from './_room'

const ROOM = baseRoom({})
const PANE = pane('landfall-brain')
const BRAIN = {
  ok: true,
  matches: [
    { title: '2026-08 cdn origin pool exhaustion', incident: 'Landfall 91', fix: 'rolled back web-edge and raised pool to 64', why: 'same 5xx shape on origin-b', confidence: 'established', webUrl: 'https://app.landfalls.ai/o/acme/brain/1' },
    // An entry that carries no fix or why (review #10): drawn with what it has.
    { title: 'origin-b health check flaps', incident: 'Landfall 77' },
  ],
}

for (const surface of ['terminal', 'desktop'] as const) {
  test(`/brain shows past incidents like this one and what fixed them (${surface})`, async ($, on) => {
    const w = await startWith($, on, { rooms: [ROOM], answers: { brain: BRAIN } })
    await $.command.run({ command: 'brain', args: '' })
    expect(w.runs[0]).toEqual(['landfall', 'brain', '--room', 'k1', '--host', 'claude-code'])
    const p = await $.ui.mount({ ...PANE, surface } as never)
    expect(await p.find({ type: 'Text', text: '2026-08 cdn origin pool exhaustion' })).toBeDefined()
    expect(await p.find({ type: 'Text', text: 'Landfall 91' })).toBeDefined()
    expect(await p.find({ type: 'Text', text: 'Fix: rolled back web-edge and raised pool to 64' })).toBeDefined()
    expect(await p.find({ type: 'Text', text: 'Why: same 5xx shape on origin-b' })).toBeDefined()
    expect(await p.find({ type: 'Text', text: '● established' })).toBeDefined()
    expect(await p.find({ type: 'Text', text: 'origin-b health check flaps' })).toBeDefined()
  })

  test(`/brain: a press quotes a match into the prompt as a draft (${surface})`, async ($, on) => {
    const w = await startWith($, on, { rooms: [ROOM], answers: { brain: BRAIN } })
    await $.command.run({ command: 'brain', args: 'origin pool' })
    expect(w.runs[0]).toEqual(['landfall', 'brain', '--room', 'k1', '--q', 'origin pool', '--host', 'claude-code'])
    const p = await $.ui.mount({ ...PANE, surface } as never)
    await p.press({ key: 'quote-0' })
    expect(w.filled).toEqual([
      '> Past incident: 2026-08 cdn origin pool exhaustion (Landfall 91)\n> Fix: rolled back web-edge and raised pool to 64\n> Why: same 5xx shape on origin-b\n> Confidence: established\n\n',
    ])
    expect(w.counts.mcp).toBe(0)
  })

  test(`/brain shows the sentence when the CLI cannot reach the brain (${surface})`, async ($, on) => {
    await startWith($, on, { rooms: [ROOM], answers: { brain: { ok: false, error: 'The company second brain is not available to the CLI yet.' } } })
    await $.command.run({ command: 'brain', args: '' })
    const p = await $.ui.mount({ ...PANE, surface } as never)
    expect(await p.find({ type: 'Text', text: 'The company second brain is not available to the CLI yet.' })).toBeDefined()
  })

  test(`/brain searches again from its field (${surface})`, async ($, on) => {
    const w = await startWith($, on, { rooms: [ROOM], answers: { brain: BRAIN } })
    await $.command.run({ command: 'brain', args: '' })
    const p = await $.ui.mount({ ...PANE, surface } as never)
    await p.input({ key: 'brain-q', text: 'cert renewal' })
    expect(w.runs[1]).toEqual(['landfall', 'brain', '--room', 'k1', '--q', 'cert renewal', '--host', 'claude-code'])
  })
}

test('/brain answers in text where no pane can be drawn, and works outside a room', async ($, on) => {
  const w = await startWith($, on, { answers: { brain: BRAIN }, placed: false })
  const answer = await $.command.run({ command: 'brain', args: 'pool' })
  expect(w.runs[0]).toEqual(['landfall', 'brain', '--q', 'pool', '--host', 'claude-code'])
  expect(answer.text).toBe(
    [
      '2026-08 cdn origin pool exhaustion · Landfall 91 · established',
      '  Fix: rolled back web-edge and raised pool to 64',
      '  Why: same 5xx shape on origin-b',
      'origin-b health check flaps · Landfall 77',
    ].join('\n'),
  )
})

test('/brain on mobile has no search field but still quotes', async ($, on) => {
  const w = await startWith($, on, { rooms: [ROOM], answers: { brain: BRAIN } })
  await $.command.run({ command: 'brain', args: '' })
  const p = await $.ui.mount({ ...PANE, surface: 'mobile' } as never)
  expect(await p.find({ key: 'brain-q' })).toBeUndefined()
  await p.press({ key: 'quote-1' })
  expect(w.filled).toEqual(['> Past incident: origin-b health check flaps (Landfall 77)\n\n'])
})
