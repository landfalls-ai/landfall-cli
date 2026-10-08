import { expect, test } from 'claude-code/testing'
import { NOW, buttonOf, fakeIo, fakeKit, hookTab, nodes, paneProps, setRoom, textsOf, watchRoom } from './_tabd'
import * as brainMod from '../hooks/components/brain.js'

// `landfall brain` as the Go CLI prints it (internal/cli/brain.go brainMatch, brain_test.go's
// entries): title, summary, confidence, the incidents an entry came from (`incident` is the first),
// kind, maturity, id, and status only when the entry is not active. There is no fix, why or webUrl
// (review #10).
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
const MIXED = {
  ok: true,
  matches: [
    { title: 'Origin pool exhaustion after edge deploys', summary: 'Twice this year a deploy shrank the pool.', confidence: 'high', status: 'confirmed', kind: 'pattern', incidents: ['Acme 91', 'Acme 95'] },
    { title: 'Roll back web-edge first', summary: 'Scaling the pool hid the symptom.', confidence: 'medium', status: 'proposed', kind: 'runbook', incidents: ['Acme 95'] },
  ],
}

function setup(answer: unknown) {
  const room = watchRoom()
  setRoom([room])
  const f = fakeIo({ run: async (args: string[]) => (f.log.runs.push(args), answer) })
  return { room, ...f }
}

for (const surface of ['terminal', 'desktop'] as const) {
  test(`${surface}: the engine draws title, two labels at most, summary, and the kind folded into the source line`, async ($, on) => {
    const { io } = setup(MIXED)
    await brainMod.warm(io)
    hookTab(on, 'd-brain', (k, i, now, args) => brainMod.tab(k, i, now, args))
    const p = await $.ui.mount({ ...paneProps('d-brain'), surface } as never)
    expect((await p.find({ type: 'Text', text: 'Company second brain' }))?.props.bold).toBe(true)
    expect(await p.find({ type: 'Text', text: 'like Acme 168' })).toBeDefined()
    expect((await p.find({ type: 'Text', text: 'Origin pool exhaustion after edge deploys' }))?.props.bold).toBe(true)
    expect((await p.find({ type: 'Text', text: '● high confidence' }))?.props.color).toBe('#0ca30c')
    expect((await p.find({ type: 'Text', text: '● confirmed' }))?.props.color).toBe('#2a78d6')
    expect((await p.find({ type: 'Text', text: '● medium confidence' }))?.props.color).toBe('#fab219')
    expect((await p.find({ type: 'Text', text: '● proposed' }))?.props.color).toBe('#898781')
    expect(await p.find({ type: 'Text', text: 'Twice this year a deploy shrank the pool.' })).toBeDefined()
    // The kind is not a label: it is in the dim source line.
    expect((await p.find({ type: 'Text', text: 'pattern · from Acme 91, Acme 95' }))?.props.dimColor).toBe(true)
    expect(await p.find({ type: 'Text', text: '● pattern' })).toBeUndefined()
    expect(await p.find({ type: 'Text', text: 'Enter on a match quotes it into your prompt.' })).toBeDefined()
    // The field, one submit word per surface, and a quote button per match.
    const field = await p.find({ type: 'Input', key: 'brain-q' })
    expect(field?.props.label).toBe('Search')
    expect(field?.props.placeholder).toBe('origin pool exhaustion')
    expect(field?.props.submitLabel).toBe(surface === 'terminal' ? 'search' : 'Search')
    const quotes = (await p.findAll({ type: 'Button' })).filter((b: any) => /^quote-/.test(String(b.key)))
    expect(quotes.map((b: any) => b.text)).toEqual(surface === 'terminal' ? ['Enter: quote it', 'Enter: quote it'] : ['Quote into my prompt', 'Quote into my prompt'])
    expect(quotes.every((b: any) => (surface === 'terminal' ? b.props.dimColor === true : b.props.variant === undefined))).toBe(true)
    expect(await p.find({ type: 'Text', text: /^live · updated 0s ago/ })).toBeDefined()
  })

  test(`${surface}: a match with ids only says how many past incidents; a conflicted entry says so`, async () => {
    const { io } = setup(BRAIN)
    await brainMod.warm(io)
    const texts = textsOf(brainMod.tab(fakeKit(surface), io, NOW, null))
    expect(texts).toContain('lesson · settled · from Acme 91, Acme 95')
    expect(texts).toContain('from 1 past incident')
    expect(texts).toContain('● established confidence')
    expect(texts).toContain('● conflicted')
    expect(texts.some((t) => /^Fix:|^Why:/.test(t))).toBe(false)
  })

  test(`${surface}: a press quotes a match into the prompt as a draft, and the console stays`, async () => {
    const { io, log } = setup(BRAIN)
    await brainMod.warm(io)
    await buttonOf(brainMod.tab(fakeKit(surface), io, NOW, null), 'quote-0')!.props.onPress()
    expect(log.filled).toEqual([
      { text: '> From the company second brain: cdn origin pool exhaustion\n> Rolled back web-edge and raised the pool to 64.\n> From Acme 91, Acme 95\n> lesson · settled · confidence established\n\n', mode: undefined },
    ])
    expect(log.opened).toEqual([])
  })

  test(`${surface}: the field searches again, and a new text says it is asking`, async () => {
    const { io, log, room } = setup(BRAIN)
    await brainMod.warm(io)
    expect(log.runs[0]).toEqual(['brain', '--room', room.roomKey, '--host', 'claude-code'])
    const field = nodes(brainMod.tab(fakeKit(surface), io, NOW, null), 'Input')[0]
    const pending = field.props.onSubmit('cert renewal')
    expect(textsOf(brainMod.tab(fakeKit(surface), io, NOW, null))).toContain('Asking the company second brain…')
    await pending
    expect(log.runs[1]).toEqual(['brain', '--room', room.roomKey, '--q', 'cert renewal', '--host', 'claude-code'])
    expect(textsOf(brainMod.tab(fakeKit(surface), io, NOW, null))).toContain('for "cert renewal"')
  })
}

test('the CLI sentence shows when it cannot reach the brain', async () => {
  const { io } = setup({ ok: false, error: 'The company second brain is not available to the CLI yet.' })
  await brainMod.warm(io)
  expect(textsOf(brainMod.tab(fakeKit('terminal'), io, NOW, null))).toContain('The company second brain is not available to the CLI yet.')
})

test('nothing found says so, for the incident and for a text', async () => {
  const { io } = setup({ ok: true, matches: [] })
  await brainMod.warm(io)
  expect(textsOf(brainMod.tab(fakeKit('terminal'), io, NOW, null))).toContain('The company second brain holds nothing on this incident yet.')
  await brainMod.search(io, 'origin pool')
  expect(textsOf(brainMod.tab(fakeKit('terminal'), io, NOW, null))).toContain('The company second brain has nothing on "origin pool" yet.')
})

test('on mobile there is no search field but a match still quotes', async () => {
  const { io, log } = setup(BRAIN)
  await brainMod.warm(io)
  const tree = brainMod.tab(fakeKit('mobile'), io, NOW, null)
  expect(nodes(tree, 'Input')).toEqual([])
  await buttonOf(tree, 'quote-1')!.props.onPress()
  expect(log.filled[0].text).toBe('> From the company second brain: dns ttl too long\n> From 1 past incident\n> confidence unconfirmed · conflicted\n\n')
})

test('text answers in words, searching for the text, and works outside a room', async () => {
  setRoom([])
  const f = fakeIo({ run: async (args: string[]) => (f.log.runs.push(args), BRAIN) })
  const out = await brainMod.text(f.io, 'pool')
  expect(f.log.runs[0]).toEqual(['brain', '--q', 'pool', '--host', 'claude-code'])
  expect(out).toBe(
    ['cdn origin pool exhaustion · lesson · settled · established', '  Rolled back web-edge and raised the pool to 64.', '  From Acme 91, Acme 95', 'dns ttl too long · unconfirmed · conflicted', '  From 1 past incident'].join('\n'),
  )
})

test('a different room starts the brain over; the tab has no badge, no letters and no hooks', async () => {
  const { io } = setup(BRAIN)
  await brainMod.warm(io)
  await brainMod.search(io, 'pool')
  setRoom([watchRoom()])
  const texts = textsOf(brainMod.tab(fakeKit('terminal'), io, NOW, null))
  expect(texts).toContain('Asking the company second brain…')
  expect(texts).toContain('like Acme 168')
  expect(brainMod.badge()).toBeNull()
  expect(brainMod.keys(fakeKit('terminal'), io, NOW, null)).toEqual([])
  const hooks: unknown[] = []
  brainMod.install((...a: unknown[]) => void hooks.push(a))
  expect(hooks).toEqual([])
})

test('on the desktop the confidence and status labels sit on the text baseline', async () => {
  const { io } = setup(MIXED)
  await brainMod.warm(io)
  const labels = nodes(brainMod.tab(fakeKit('desktop'), io, NOW, null), 'Box').find((b) => b.props.key === 'bl-m0')
  expect(labels?.props.alignItems).toBe('center')
})
