import { expect, test } from 'claude-code/testing'
import { NOW, buttonOf, fakeIo, fakeKit, hookTab, nodes, paneProps, setRoom, textsOf, watchRoom } from './_tabhook'
import { consoleState } from '../hooks/core.js'
import * as moreMod from '../hooks/components/more.js'

const COMMS = {
  ok: true,
  messages: [
    { id: 'm2', state: 'draft', channel: 'statuspage', kind: 'status-update', text: 'A fix is rolling out.' },
    { id: 'm1', state: 'approved', channel: 'slack', kind: 'status-update', text: 'We are investigating.', approvedBy: 'carol' },
    { id: 'm0', state: 'sent', channel: 'slack', kind: 'status-update', text: 'Earlier update.', approvedBy: 'carol' },
  ],
}
const BRAIN = { ok: true, matches: [{ title: 'Origin pool exhaustion', summary: 'A deploy shrank the pool.', confidence: 'high', status: 'confirmed', kind: 'pattern', incidents: ['Acme 91'] }] }

function setup() {
  const room = watchRoom()
  setRoom([room])
  const f = fakeIo({ run: async (args: string[]) => (f.log.runs.push(args), args[0] === 'comms' ? COMMS : BRAIN) })
  return { room, ...f }
}

// More keeps its chip while the session lives, so each test names the chip it needs first, and
// waits for the read that choosing it starts.
async function showChip(f: ReturnType<typeof setup>, words: string) {
  moreMod.tab(fakeKit('terminal'), f.io, NOW, null)
  moreMod.tab(fakeKit('terminal'), f.io, NOW, words)
  moreMod.tab(fakeKit('terminal'), f.io, NOW, null)
  await new Promise((r) => setTimeout(r, 5))
}

for (const surface of ['terminal', 'desktop'] as const) {
  test(`${surface}: the engine draws the chips in the Timeline idiom, Comms first with its count, then its body`, async ($, on) => {
    const f = setup()
    await showChip(f, 'comms')
    // Counted once read, so the chip reads `comms 2` (two updates are not sent).
    hookTab(on, 'd-more', (k, i, now, args) => moreMod.tab(k, i, now, 'comms'))
    const p = await $.ui.mount({ ...paneProps('d-more'), surface } as never)
    const chips = (await p.findAll({ type: 'Button' })).filter((b: any) => /^more-/.test(String(b.key)))
    expect(chips.map((b: any) => b.props.hotkey)).toEqual(['c', 'b'])
    expect(chips[0].text).toBe(surface === 'terminal' ? '▸ comms 2' : '▸ Comms 2')
    expect(chips[1].text).toBe(surface === 'terminal' ? 'brain' : 'Brain')
    // The active chip at full strength, the other dim; neither is primary, and nothing is filled.
    expect(chips[0].props.dimColor).toBeUndefined()
    expect(chips[1].props.dimColor).toBe(true)
    expect(chips.every((b: any) => b.props.variant === undefined)).toBe(true)
    expect((await p.findAll({ type: 'Box' })).some((b: any) => b.props.backgroundColor)).toBe(false)
    // Comms' own body under it, and no other key.
    expect(await p.find({ type: 'Text', text: 'Stakeholder updates' })).toBeDefined()
    expect(await p.find({ type: 'Text', text: 'Approving an update happens in the war room, in the browser.' })).toBeDefined()
    expect((await p.findAll({ type: 'Button' })).length).toBe(2)
  })

  test(`${surface}: b shows Brain with its Search field, and starts its reads the first time only`, async () => {
    const f = setup()
    const k = fakeKit(surface)
    await showChip(f, 'comms')
    f.log.runs.length = 0
    await buttonOf(moreMod.tab(k, f.io, NOW, null), 'more-brain')!.props.onPress()
    expect(f.log.runs.map((a) => a[0])).toEqual(['brain'])
    const tree = moreMod.tab(k, f.io, NOW, null)
    expect(buttonOf(tree, 'more-brain')!.props.label).toBe(surface === 'terminal' ? '▸ brain' : '▸ Brain')
    expect(textsOf(tree)).toContain('Company second brain')
    expect(nodes(tree, 'Input').map((i) => i.props.key)).toEqual(['brain-q'])
    // Back and forth does not read again.
    await buttonOf(tree, 'more-comms')!.props.onPress()
    await buttonOf(moreMod.tab(k, f.io, NOW, null), 'more-brain')!.props.onPress()
    expect(f.log.runs.map((a) => a[0])).toEqual(['brain'])
    // The chip is remembered.
    expect(buttonOf(moreMod.tab(k, f.io, NOW, null), 'more-brain')!.props.label).toBe(surface === 'terminal' ? '▸ brain' : '▸ Brain')
  })
}

test('/landfall brain <text> shows Brain and searches for the text once; comms shows Comms', async () => {
  const f = setup()
  const k = fakeKit('terminal')
  consoleState.args = 'brain origin pool'
  const tree = moreMod.tab(k, f.io, NOW, 'brain origin pool')
  expect(consoleState.args).toBeNull()
  await new Promise((r) => setTimeout(r, 0))
  expect(f.log.runs).toEqual([['brain', '--room', f.room.roomKey, '--q', 'origin pool', '--host', 'claude-code']])
  expect(buttonOf(tree, 'more-brain')!.props.label).toBe('▸ brain')
  // Drawn again with the same words: nothing more is read.
  moreMod.tab(k, f.io, NOW, 'brain origin pool')
  await new Promise((r) => setTimeout(r, 0))
  expect(f.log.runs).toHaveLength(1)
  // The object form, and a bare `comms`.
  moreMod.tab(k, f.io, NOW, null)
  moreMod.tab(k, f.io, NOW, { chip: 'comms' })
  await new Promise((r) => setTimeout(r, 0))
  expect(buttonOf(moreMod.tab(k, f.io, NOW, null), 'more-comms')!.props.label.startsWith('▸ comms')).toBe(true)
  expect(f.log.runs.at(-1)![0]).toBe('comms')
})

test('a bare /landfall brain reads the room\'s own incident', async () => {
  const f = setup()
  moreMod.tab(fakeKit('terminal'), f.io, NOW, null)
  moreMod.tab(fakeKit('terminal'), f.io, NOW, 'brain')
  await new Promise((r) => setTimeout(r, 0))
  expect(f.log.runs.at(-1)).toEqual(['brain', '--room', f.room.roomKey, '--host', 'claude-code'])
})

test('warm and refresh read the chip shown; More has no badge and no letters of its own', async () => {
  const f = setup()
  await showChip(f, 'brain')
  f.log.runs.length = 0
  await moreMod.warm(f.io)
  expect(f.log.runs.at(-1)![0]).toBe('brain')
  await moreMod.refresh(f.io)
  expect(f.log.runs.at(-1)![0]).toBe('brain')
  await buttonOf(moreMod.tab(fakeKit('terminal'), f.io, NOW, null), 'more-comms')!.props.onPress()
  await moreMod.refresh(f.io)
  expect(f.log.runs.at(-1)![0]).toBe('comms')
  expect(moreMod.badge()).toBeNull()
  expect(moreMod.keys(fakeKit('terminal'), f.io, NOW, null)).toEqual([])
})

test('text answers the chip asked for, in words', async () => {
  const f = setup()
  expect(await moreMod.text(f.io, 'comms')).toContain('Approving an update happens in the war room, in the browser.')
  expect(await moreMod.text(f.io, 'brain origin')).toContain('Origin pool exhaustion · pattern · high · confirmed')
  expect(f.log.runs.at(-1)).toEqual(['brain', '--room', f.room.roomKey, '--q', 'origin', '--host', 'claude-code'])
})

test('no standalone panes or commands are left behind', () => {
  const hooks: unknown[] = []
  moreMod.install((...a: unknown[]) => void hooks.push(a))
  expect(hooks).toEqual([])
})
