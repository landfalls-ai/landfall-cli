import { expect, test } from 'claude-code/testing'
import { baseRoom, pane, startWith } from './_room'

const ROOM = baseRoom({})
const PANE = pane('landfall-brain')
// `landfall brain` as the Go CLI prints it (internal/cli/brain.go brainMatch,
// brain_test.go's entries): title, summary, confidence, the incidents an entry
// came from (`incident` is the first), kind, maturity, id, and status only
// when the entry is not active. There is no fix, why or webUrl (review #10).
const BRAIN = {
  ok: true,
  matches: [
    {
      title: 'cdn origin pool exhaustion',
      summary: 'Rolled back web-edge and raised the pool to 64.',
      confidence: 'established',
      incident: 'Acme 91',
      incidents: ['Acme 91', 'Acme 95'],
      kind: 'lesson',
      maturity: 'settled',
      id: 'e1',
    },
    // From the room's own read: source incidents by id only, and an entry the brain no longer stands behind.
    { title: 'dns ttl too long', confidence: 'unconfirmed', incident: '0b9e3c1a-6f1d-4d6e-9a51-2f3c4d5e6f70', incidents: ['0b9e3c1a-6f1d-4d6e-9a51-2f3c4d5e6f70'], id: 'e2', status: 'conflicted' },
  ],
}

for (const surface of ['terminal', 'desktop'] as const) {
  test(`/brain shows what the brain holds: summary, kind, confidence and where it came from (${surface})`, async ($, on) => {
    const w = await startWith($, on, { rooms: [ROOM], answers: { brain: BRAIN } })
    await $.command.run({ command: 'brain', args: '' })
    expect(w.runs[0]).toEqual(['landfall', 'brain', '--room', 'k1', '--host', 'claude-code'])
    const p = await $.ui.mount({ ...PANE, surface } as never)
    expect(await p.find({ type: 'Text', text: 'cdn origin pool exhaustion' })).toBeDefined()
    expect(await p.find({ type: 'Text', text: 'Rolled back web-edge and raised the pool to 64.' })).toBeDefined()
    expect(await p.find({ type: 'Text', text: '● lesson · settled' })).toBeDefined()
    expect((await p.find({ type: 'Text', text: '● established' }))?.props.color).toBe('#0ca30c')
    expect(await p.find({ type: 'Text', text: 'From Acme 91, Acme 95' })).toBeDefined()
    // Ids are not names: the count instead. A conflicted entry says so.
    expect(await p.find({ type: 'Text', text: 'dns ttl too long' })).toBeDefined()
    expect(await p.find({ type: 'Text', text: 'From 1 past incident' })).toBeDefined()
    expect((await p.find({ type: 'Text', text: '● conflicted' }))?.props.color).toBe('#d03b3b')
    expect(await p.find({ type: 'Text', text: /^Fix:|^Why:/ })).toBeUndefined()
  })

  test(`/brain: a press quotes a match into the prompt as a draft (${surface})`, async ($, on) => {
    const w = await startWith($, on, { rooms: [ROOM], answers: { brain: BRAIN } })
    await $.command.run({ command: 'brain', args: 'origin pool' })
    expect(w.runs[0]).toEqual(['landfall', 'brain', '--room', 'k1', '--q', 'origin pool', '--host', 'claude-code'])
    const p = await $.ui.mount({ ...PANE, surface } as never)
    await p.press({ key: 'quote-0' })
    expect(w.filled).toEqual([
      '> From the company second brain: cdn origin pool exhaustion\n> Rolled back web-edge and raised the pool to 64.\n> From Acme 91, Acme 95\n> lesson · settled · confidence established\n\n',
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
      'cdn origin pool exhaustion · lesson · settled · established',
      '  Rolled back web-edge and raised the pool to 64.',
      '  From Acme 91, Acme 95',
      'dns ttl too long · unconfirmed · conflicted',
      '  From 1 past incident',
    ].join('\n'),
  )
})

test('/brain on mobile has no search field but still quotes', async ($, on) => {
  const w = await startWith($, on, { rooms: [ROOM], answers: { brain: BRAIN } })
  await $.command.run({ command: 'brain', args: '' })
  const p = await $.ui.mount({ ...PANE, surface: 'mobile' } as never)
  expect(await p.find({ key: 'brain-q' })).toBeUndefined()
  await p.press({ key: 'quote-1' })
  expect(w.filled).toEqual(['> From the company second brain: dns ttl too long\n> From 1 past incident\n> confidence unconfirmed · conflicted\n\n'])
})

test('/brain with nothing found says so', async ($, on) => {
  await startWith($, on, { rooms: [ROOM], answers: { brain: { ok: true, matches: [] } } })
  await $.command.run({ command: 'brain', args: '' })
  const p = await $.ui.mount({ ...PANE, surface: 'terminal' } as never)
  expect(await p.find({ type: 'Text', text: 'The company second brain holds nothing on this incident yet.' })).toBeDefined()
})
