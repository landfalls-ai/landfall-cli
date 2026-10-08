import { expect, test } from 'claude-code/testing'
import { NOW, buttonOf, fakeIo, fakeKit, hookTab, nodes, paneProps, ran, setRoom, textsOf, watchRoom } from './_tabhook'
import * as voteMod from '../hooks/components/vote.js'

// The Vote tab (spec 4.1): the component's own tab(k, io, nowMs, args), as console.js draws it.

function vote(over: Record<string, unknown> = {}) {
  return {
    claimSeq: 212,
    class: 'finding',
    statement: 'The 5xx rise starts at 15:45Z, the same bucket as the v2.3.1 deploy',
    authoredBy: 'bob-claude-code',
    authorIsAgent: true,
    authorHuman: 'bob',
    positionsSoFar: 1,
    needed: 2,
    shortfall: 1,
    expiresInMs: 250000,
    stale: false,
    evidence: 'CloudWatch 5xxErrorRate · deploy record 15:48:59Z',
    mine: false,
    ...over,
  }
}
const SECOND = vote({ claimSeq: 214, statement: 'Pool sits at 41%', authoredBy: 'carol', authorIsAgent: false, authorHuman: undefined, expiresInMs: 400000, evidence: undefined, needed: undefined, shortfall: 2, positionsSoFar: 0 })
const MINE = vote({ claimSeq: 210, statement: 'My own finding', mine: true })

for (const surface of ['terminal', 'desktop'] as const) {
  test(`${surface}: the engine draws the card, the keys c x l, and the other votes waiting`, async ($, on) => {
    const room = watchRoom({ votes: [vote(), SECOND, MINE] })
    setRoom([room])
    hookTab(on, 'd-vote', (k, io, now, args) => voteMod.tab(k, io, now, args))
    const pane = await $.ui.mount({ ...paneProps('d-vote'), surface } as never)
    expect(await pane.find({ type: 'Text', text: 'Your vote is waiting' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: '● 4m 10s left' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: "bob's agent · staged #212" })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: 'positions 1 of 2 · your vote would admit it' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: '“The 5xx rise starts at 15:45Z, the same bucket as the v2.3.1 deploy”' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: 'Evidence: CloudWatch 5xxErrorRate · deploy record 15:48:59Z' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: 'Expires in 4m 10s.' })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: 'Your position admits it to the shared context. Every agent in the room reads it after that.' })).toBeDefined()
    if (surface === 'desktop') expect((await pane.findAll({ type: 'Svg' })).map((x: any) => x.props.alt)).toContain('1 of 2 positions')
    else {
      expect(await pane.find({ type: 'Text', text: '●' })).toBeDefined()
      expect(await pane.find({ type: 'Text', text: '○' })).toBeDefined()
    }
    // Keys: c, x, l in that order; the person's own claim is never offered; the other vote is a row.
    const keys = (await pane.findAll({ type: 'Button' })).map((b: any) => b.props.hotkey).filter(Boolean)
    expect(keys).toEqual(['c', 'x', 'l'])
    expect(await pane.find({ type: 'Text', text: /My own finding/ })).toBeUndefined()
    expect(await pane.find({ type: 'Text', text: 'Also waiting' })).toBeDefined()
    expect(await pane.find({ type: 'Button', key: 'vt-also-214' })).toBeDefined()
  })

  test(`${surface}: Enter on another vote brings it up, with its own evidence line and admission sentence`, async () => {
    const room = watchRoom({ votes: [vote(), SECOND, MINE] })
    setRoom([room])
    const k = fakeKit(surface)
    const { io } = fakeIo()
    expect(textsOf(voteMod.tab(k, io, NOW, null))).toContain("bob's agent · staged #212")
    buttonOf(voteMod.tab(k, io, NOW, null), 'vt-also-214')!.props.onPress()
    const texts = textsOf(voteMod.tab(k, io, NOW, null))
    expect(texts).toContain('carol · staged #214')
    expect(texts).toContain('No evidence is attached to this claim.')
    expect(texts).toContain('Your position counts toward admitting it to the shared context.')
    expect(texts).toContain('Also waiting')
  })

  test(`${surface}: x draws the reason field and reads cancel; an empty reason records nothing; a reason contests`, async () => {
    const room = watchRoom({ votes: [vote()] })
    setRoom([room])
    const k = fakeKit(surface)
    const run = async (args: string[]) => {
      runs.push(args)
      return { ok: true, claimSeq: 212, position: 'contest', admitted: false }
    }
    const runs: string[][] = []
    let now = NOW
    const { io, log } = fakeIo({ run, now: () => now })
    const draw = () => voteMod.tab(k, io, now, null)
    expect(nodes(draw(), 'Input')).toEqual([])
    expect(buttonOf(draw(), 'vt-contest')?.props.label).toBe(surface === 'terminal' ? 'contest…' : 'Contest…')
    buttonOf(draw(), 'vt-contest')!.props.onPress()
    const field = nodes(draw(), 'Input')[0]
    expect(field.props.key).toBe('vt-reason')
    expect(field.props.submitLabel).toBe(surface === 'terminal' ? 'contest' : 'Contest')
    expect(buttonOf(draw(), 'vt-contest')?.props.label).toBe(surface === 'terminal' ? 'cancel' : 'Cancel')
    await field.props.onSubmit('  ')
    expect(runs).toEqual([])
    expect(log.toasts.at(-1)).toBe('Say why you contest it, then press Enter.')
    // x again removes the field.
    buttonOf(draw(), 'vt-contest')!.props.onPress()
    expect(nodes(draw(), 'Input')).toEqual([])
    buttonOf(draw(), 'vt-contest')!.props.onPress()
    await nodes(draw(), 'Input')[0].props.onSubmit('pool sits at 41%')
    expect(runs).toEqual([['vote', '--room', room.roomKey, '--claim', '212', '--position', 'contest', '--reason', 'pool sits at 41%']])
    expect(log.toasts.at(-1)).toBe('Contested #212')
    // Recorded: the tab says so for 10 s, then the empty text.
    expect(textsOf(draw())).toContain('Your vote on #212 is recorded.')
    expect(buttonOf(draw(), 'vt-corroborate')).toBeUndefined()
    now = NOW + 10000
    expect(textsOf(draw())).toEqual(['No vote is waiting on you.'])
  })

  test(`${surface}: l sets it aside; with nothing waiting the tab says so and draws no key`, async () => {
    setRoom([watchRoom({ votes: [vote()] })])
    const k = fakeKit(surface)
    const { io } = fakeIo()
    buttonOf(voteMod.tab(k, io, NOW, null), 'vt-later')!.props.onPress()
    const tree = voteMod.tab(k, io, NOW, null)
    expect(textsOf(tree)).toEqual(['No vote is waiting on you.'])
    expect(nodes(tree, 'Button')).toEqual([])
  })

  test(`${surface}: c corroborates as the person through the CLI, never an MCP call`, async () => {
    const room = watchRoom({ votes: [vote()] })
    setRoom([room])
    const k = fakeKit(surface)
    const { io, log } = fakeIo({ run: async (args: string[]) => (log.runs.push(args), { ok: true, admitted: true }) })
    await buttonOf(voteMod.tab(k, io, NOW, null), 'vt-corroborate')!.props.onPress()
    expect(log.runs).toEqual([['vote', '--room', room.roomKey, '--claim', '212', '--position', 'corroborate']])
    expect(log.toasts).toEqual(['Corroborated #212 · admitted'])
    expect(textsOf(voteMod.tab(k, io, NOW, null))).toContain('Your vote on #212 is recorded.')
  })
}

test('no room: the tab says the folder is not in a war room, and badge is null', async ($, on) => {
  setRoom([])
  const vote = voteMod
  const k = fakeKit('terminal')
  const { io } = fakeIo()
  expect(textsOf(vote.tab(k, io, NOW, null))).toEqual(['This folder is not in a war room.'])
  expect(vote.badge()).toBeNull()
  expect(vote.keys(k, io, NOW, null)).toEqual([])
})

test('the badge is the votes waiting on the person, never her own; none at zero', async () => {
  const vote = voteMod
  setRoom([watchRoom({ votes: [MINE] })])
  expect(vote.badge()).toBeNull()
  setRoom([watchRoom({ votes: [vote_(), SECOND, MINE] })])
  expect(vote.badge()).toBe(2)
})

function vote_() {
  return vote()
}

test('in flight, the pressed key reads corroborating… and a second press records nothing', async () => {
  const room = watchRoom({ votes: [vote()] })
    setRoom([room])
  const mod = voteMod
  let release: (v: unknown) => void = () => {}
  const runs: string[][] = []
  const { io } = fakeIo({
    run: (args: string[]) => {
      runs.push(args)
      return new Promise((r) => (release = r))
    },
  })
  const k = fakeKit('terminal')
  const first = mod.castVote(io, room.roomKey, vote(), 'corroborate')
  const label = (kk: any) => buttonOf(mod.keys(kk, io, NOW, null), 'vt-corroborate')?.props.label
  expect(label(k)).toBe('corroborating…')
  expect(label(fakeKit('desktop'))).toBe('Corroborating…')
  await mod.castVote(io, room.roomKey, vote(), 'corroborate')
  expect(runs).toHaveLength(1)
  release({ ok: true, admitted: false })
  await first
  expect(label(k)).toBeUndefined()
})

test('mobile: no reason field and no contest key', async () => {
  const room = watchRoom({ votes: [vote()] })
    setRoom([room])
  const mod = voteMod
  const k = fakeKit('mobile')
  const { io } = fakeIo({ surface: 'mobile' })
  const tree = mod.tab(k, io, NOW, null)
  expect(nodes(tree, 'Input')).toEqual([])
  const keys = mod.keys(k, io, NOW, null).map((b: any) => b.props.key)
  expect(keys).toEqual(['vt-corroborate', 'vt-later'])
})

test('the old standalone pane and command are gone: install registers nothing', async () => {
  const mod = voteMod
  const hooks: unknown[] = []
  mod.install((...a: unknown[]) => void hooks.push(a))
  expect(hooks).toEqual([])
})

test('text answers the waiting votes in words, or says none is waiting', async () => {
  const mod = voteMod
  const { io } = fakeIo()
  setRoom([watchRoom({ votes: [vote(), MINE] })])
  expect(await mod.text(io, '')).toBe(
    [
      'Acme 168 · cloudfront-5xx-high · 1 vote waiting on you',
      "#212 bob's agent: “The 5xx rise starts at 15:45Z, the same bucket as the v2.3.1 deploy”",
      '  positions 1 of 2 · your vote would admit it · 4m 10s left',
      '  Evidence: CloudWatch 5xxErrorRate · deploy record 15:48:59Z',
      'Vote as yourself: landfall vote --claim <seq> --position corroborate|contest [--reason "<why>"]',
    ].join('\n'),
  )
  setRoom([watchRoom({ votes: [MINE] })])
  expect(await mod.text(io, '')).toBe('No vote is waiting on you.')
  setRoom([])
  expect(await mod.text(io, '')).toContain('This folder is not in a war room.')
})

test('every vote toast stays within 80 characters, however long the name or the CLI sentence', async () => {
  const mod = voteMod
  const room = watchRoom({ votes: [] })
  setRoom([room])
  const long = vote({ authorHuman: 'a-very-long-collaborator-name-that-goes-on', authoredBy: 'x'.repeat(60) })
  const { io, log } = fakeIo({ run: async () => ({ ok: false, error: 'Sign in to vote as yourself: run landfall login. '.repeat(4) }) })
  mod.onSnapshot(io, { rooms: [{ ...watchRoom(), votes: [long] }] })
  await mod.castVote(io, room.roomKey, long, 'corroborate')
  const ok = fakeIo({ run: async () => ({ ok: true, admitted: true, note: 'You joined as a guest, so your vote counts as a person but not as a member.' }) })
  await mod.castVote(ok.io, room.roomKey, vote({ claimSeq: 999 }), 'corroborate')
  const boom = fakeIo({ run: async () => { throw new Error('x'.repeat(300)) } })
  await mod.castVote(boom.io, room.roomKey, vote({ claimSeq: 998 }), 'corroborate')
  for (const t of [...log.toasts, ...ok.log.toasts, ...boom.log.toasts]) expect(t.length).toBeLessThanOrEqual(80)
  expect(log.toasts.length + ok.log.toasts.length + boom.log.toasts.length).toBeGreaterThanOrEqual(4)
})

test('on the desktop every row holding a status label centres its children on the text baseline', async () => {
  const row = (tree: unknown, key: string) => nodes(tree, 'Box').find((b) => b.props.key === key)
  setRoom([watchRoom({ votes: [vote()] })])
  const { io } = fakeIo()
  expect(row(voteMod.tab(fakeKit('desktop'), io, NOW, null), 'vt-head')?.props.alignItems).toBe('center')
  // The terminal draws one text row: nothing to centre.
  expect(row(voteMod.tab(fakeKit('terminal'), io, NOW, null), 'vt-head')?.props.alignItems).toBeUndefined()
})
