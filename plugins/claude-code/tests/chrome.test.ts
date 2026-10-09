import { expect, test } from 'claude-code/testing'
import { consoleState } from '../hooks/core.js'
import * as contextTab from '../hooks/components/context.js'
import * as lbTab from '../hooks/components/lb.js'
import * as moreTab from '../hooks/components/more.js'
import * as timelineTab from '../hooks/components/timeline.js'
import * as wallTab from '../hooks/components/wall.js'
import { BAND, consolePane, roomOf, world } from './_console'
import { fakeIo, openConsoleOn, setRooms, settle } from './_tab'

// Round 7: the console draws one keys row and one footer for every tab, built from each component's
// optional keys() and footer() plus `r: refresh` and close. No tab's own body adds a second pair, and
// a tab reads only while the console is open and the tab is warm (Home's wall, lb and timeline apart).

const VOTE = { claimSeq: 212, statement: 'The 5xx rise starts at 15:45Z', authorIsAgent: true, authorHuman: 'bob', positionsSoFar: 1, needed: 2, shortfall: 1, expiresInMs: 250000, mine: false }
const WALL = {
  ok: true,
  sharedBy: 'carol',
  windowMs: 21600000,
  widgets: [{ id: 'w1', type: 'stat', title: '5xx error rate', value: '0.2', unit: '%', tone: 'critical', spark: [0.1, 0.2, 3.1, 0.4, 0.2] }],
}
const ANSWERS = {
  wall: WALL,
  lb: { ok: true, loadBalancers: [] },
  timeline: { ok: true, events: [{ seq: 301, at: '2026-10-08T16:01:00Z', glyph: '·', text: 'event 1' }] },
  artifacts: { ok: true, artifacts: [] },
  brief: { ok: true, text: 'Landfall 168', asOfSeq: 40, counts: { established: 0, open: 0, participants: 1 }, established: [], open: [], participants: [] },
  comms: { ok: true, messages: [{ id: 'm1', state: 'draft', channel: 'slack', kind: 'status-update', text: 'We are investigating.' }] },
  brain: { ok: true, matches: [] },
  incidents: { ok: true, org: 'acme', incidents: [] },
  whoami: { ok: true, signedIn: true, org: 'acme' },
}

// The tab words `/landfall` takes, and whether the tab has a read to report in its footer.
const TABS: Array<[string, boolean]> = [
  ['home', true],
  ['vote', false],
  ['context', true],
  ['wall', true],
  ['people', false],
  ['timeline', true],
  ['lb', true],
  ['incidents', true],
  ['comms', true],
  ['brain', true],
]
const FOOTER = /(^|· )updated \d+[smh] ago/

for (const surface of ['terminal', 'desktop'] as const) {
  for (const [tab, hasFooter] of TABS) {
    test(`${surface}: ${tab} draws the console's one keys row and one footer`, async ($, on) => {
      const w = await world($, on, { rooms: [roomOf({ votes: [VOTE] })], answers: ANSWERS, surface })
      await $.command.run({ command: 'landfall', args: tab })
      await w.clock.settle()
      const pane = await $.ui.mount(consolePane(surface, 80) as never)
      const all = await pane.findAll({})
      const keyOf = (n: any) => String(n.key ?? n.props?.key ?? '')
      // Exactly one keys row (the `keys` tail), and no tab's own `*-keys` row (Home's vote block is a block, not a tab row).
      expect(all.filter((n: any) => keyOf(n) === 'keys')).toHaveLength(1)
      expect(all.filter((n: any) => /^(vt|lb|tl|cm|br|who|wall|cx|more)-keys$/.test(keyOf(n)))).toEqual([])
      // One refresh and one close, never a second pair of the same words.
      const buttons = all.filter((n: any) => n.type === 'Button')
      const label = (n: any) => String(n.props?.label ?? '').toLowerCase()
      expect(buttons.filter((n: any) => label(n) === 'refresh')).toHaveLength(1)
      expect(buttons.filter((n: any) => label(n).startsWith('close'))).toHaveLength(1)
      expect(buttons.filter((n: any) => n.props?.hotkey === 'r')).toHaveLength(1)
      // One live / updated footer where the tab reads, and never two.
      const footers = all.filter((n: any) => n.type === 'Text' && FOOTER.test((n.children ?? []).map((c: any) => (typeof c === 'string' ? c : '')).join('')))
      expect(footers.length).toBeLessThanOrEqual(1)
      if (hasFooter) expect(footers).toHaveLength(1)
      // The footer sits before the keys row, which is last.
      const order = all.map(keyOf)
      expect(order.lastIndexOf('keys')).toBeGreaterThan(order.indexOf('foot') === -1 ? -1 : order.indexOf('foot'))
    })
  }
}

test('the Vote tab keys come from keys() alone: c x l, then refresh and close', async ($, on) => {
  const w = await world($, on, { rooms: [roomOf({ votes: [VOTE] })], answers: ANSWERS })
  await $.command.run({ command: 'landfall', args: 'vote' })
  await w.clock.settle()
  const pane = await $.ui.mount(consolePane('terminal', 80) as never)
  const hot = (await pane.findAll({ type: 'Button' })).map((b: any) => b.props.hotkey).filter(Boolean)
  expect(hot).toEqual(['c', 'x', 'l', 'r'])
})

// ---------- reads only while visible ----------

const READS = ['wall', 'lb', 'timeline', 'brief', 'artifacts', 'comms', 'brain', 'incidents']

test('a closed console reads nothing, and a tab never shown stays cold while Home reads its three', async ($, on) => {
  const w = await world($, on, { rooms: [roomOf()], answers: ANSWERS })
  await $.command.run({ command: 'landfall', args: 'home' })
  await w.clock.settle()
  const pane = await $.ui.mount(consolePane('terminal', 80) as never)
  const cold = () => READS.filter((c) => !['wall', 'lb', 'timeline'].includes(c)).map((c) => w.count(c))
  const before = cold()
  const lb = w.count('lb')
  await w.clock.advance(130000)
  // Home's reads keep their cadence; Context, Comms, Brain and Incidents were never shown, so they are not read.
  expect(w.count('lb')).toBeGreaterThan(lb)
  expect(cold()).toEqual(before)

  // The person closes it: every read stops, on the next tick and in between.
  await pane.press({ key: 'close' })
  const total = () => READS.map((c) => w.count(c))
  const after = total()
  await w.clock.advance(300000)
  expect(total()).toEqual(after)
})

test('a tab shown once reads on its cadence until the console closes; Context included', async ($, on) => {
  const w = await world($, on, { rooms: [roomOf()], answers: ANSWERS })
  await $.command.run({ command: 'landfall', args: 'context' })
  await w.clock.settle()
  const pane = await $.ui.mount(consolePane('terminal', 80) as never)
  const brief = w.count('brief')
  await w.clock.advance(35000)
  expect(w.count('brief')).toBeGreaterThan(brief)
  await pane.press({ key: 'close' })
  const after = w.count('brief')
  await w.clock.advance(120000)
  expect(w.count('brief')).toBe(after)
})

test('the band is untouched by the chrome rule: it still draws without the console open', async ($, on) => {
  await world($, on, { rooms: [roomOf({ votes: [VOTE] })], answers: ANSWERS })
  const band = await $.ui.mount({ ...BAND, surface: 'desktop' } as never)
  expect(await band.find({ type: 'Button', key: 'vote-corroborate' })).toBeDefined()
  // The positions row is the console's to show, not the band's (five rows at most).
  expect(await band.find({ type: 'Text', text: /^positions / })).toBeUndefined()
})

// The components themselves: a tick or a stream event reads only while the console is open and the
// tab is warm. Closing clears both before any tick can notice, so an event in between reads nothing.
test('a stream event right after the close, before any tick, reads nothing', async () => {
  const room = { ...roomOf(), roomKey: 'rk-chrome', maxSeq: 300, widgetSeq: 300 }
  setRooms([room])
  openConsoleOn('wall')
  consoleState.warm.add('timeline')
  const io = fakeIo((argv) => (argv[0] === 'timeline' ? { ok: true, events: [], hasMore: false } : { ok: true, widgets: [] }))
  void wallTab.warm(io as never)
  await timelineTab.warm(io as never)
  await settle()
  const reads = () => io.runs.length
  const open = reads()
  expect(open).toBeGreaterThan(0)
  // The person closes it (ui.close): open and warm both go.
  consoleState.open = false
  consoleState.warm.clear()
  const moved = { line: '', rooms: [{ ...room, maxSeq: 310, widgetSeq: 310 }] }
  wallTab.onSnapshot(io as never, moved as never, null as never)
  timelineTab.onSnapshot(io as never, moved as never, null as never)
  await settle()
  expect(reads()).toBe(open)
})

test('an open console reads only the tabs that are warm: a cold Context, Load balancers or Comms stays quiet', async () => {
  const room = { ...roomOf(), roomKey: 'rk-cold' }
  setRooms([room])
  consoleState.open = true
  consoleState.warm.clear()
  consoleState.tab = 'home'
  const io = fakeIo(() => ({ ok: true, loadBalancers: [], messages: [], artifacts: [] }))
  // Ticks a minute, two minutes and ten minutes on, for tabs the console never warmed.
  for (const ms of [62000, 124000, 600000]) {
    const at = io.clock.t + ms
    await contextTab.tick(io as never, at)
    await lbTab.tick(io as never, at)
    await moreTab.tick(io as never, at)
    await wallTab.tick(io as never, at)
    await timelineTab.tick(io as never, at)
  }
  await settle()
  expect(io.runs).toEqual([])
  consoleState.open = false
})

test('Home draws l: later on its vote block, and l sets the vote aside', async ($, on) => {
  const w = await world($, on, { rooms: [roomOf({ votes: [VOTE] })], answers: ANSWERS })
  await $.command.run({ command: 'landfall', args: 'home' })
  await w.clock.settle()
  const pane = await $.ui.mount(consolePane('terminal', 80) as never)
  expect((await pane.find({ type: 'Button', key: 'hv-l' }))?.props.hotkey).toBe('l')
  await pane.press({ key: 'hv-l' })
  expect(await pane.find({ type: 'Text', text: 'Your vote is waiting' })).toBeUndefined()
  expect(w.counts.mcp).toBe(0)
})
