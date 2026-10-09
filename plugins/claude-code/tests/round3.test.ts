import { expect, test } from 'claude-code/testing'
import { estRows, squeezeHome, planHome } from '../hooks/console.js'
import { statusLine } from '../hooks/components/room.js'
import { consolePane, roomOf, texts, world } from './_console'
import { Drawn, kitFor } from './_tab'

// Round 3: what the real-app captures of round 2 found (rounds/2/REVIEW.md), the parts the test kit
// can see. The tabs' own fixes are tested beside them (round3-wall-lb, round3-people-context,
// round3-timeline-incidents). The captures themselves are rounds/3.

const VOTE = { claimSeq: 212, statement: 'The 5xx rise starts at 15:45Z', authorIsAgent: true, authorHuman: 'bob', positionsSoFar: 1, needed: 2, shortfall: 1, expiresInMs: 250000, mine: false }
const WALL = {
  ok: true,
  sharedBy: 'carol',
  windowMs: 21600000,
  widgets: [
    { id: 'w1', type: 'stat', title: '5xx error rate', value: '0.2', unit: '%', tone: 'critical', deltaLabel: 'peak 3.1%', spark: [0.1, 0.2, 3.1, 0.4, 0.2] },
    { id: 'w2', type: 'stat', title: 'Healthy origins', value: '4', unit: '/6', tone: 'warning', delta: -2, trend: 'down', spark: [6, 6, 5, 4, 4] },
    { id: 'w4', type: 'stat', title: 'Requests per minute', value: '48200', delta: 2.6, trend: 'up', spark: [1, 2, 3, 2, 3] },
  ],
}
const EVENTS = [1, 2, 3].map((i) => ({ seq: 300 + i, at: '2026-10-08T16:0' + i + ':00Z', glyph: '·', text: 'event ' + i }))
const ANSWERS = { wall: WALL, lb: { ok: true, loadBalancers: [] }, timeline: { ok: true, events: EVENTS }, artifacts: { ok: true, artifacts: [] }, brief: { ok: false, error: 'not yet' } }

// ---------- decision 2: a successful open leaves no row in the transcript ----------

test('decision 2: /landfall answers no text on a successful open, on every tab', async ($, on) => {
  await world($, on, { rooms: [roomOf()], answers: ANSWERS })
  for (const args of ['', 'wall', 'people alice', 'timeline', 'lb', 'incidents']) {
    const answer = await $.command.run({ command: 'landfall', args })
    expect(answer.text).toBeUndefined()
  }
  // Where there is something to say it still says it.
  expect((await $.command.run({ command: 'landfall', args: 'nonsense' })).text).toContain('Landfall tabs')
})

// ---------- issue 18: the status line, no emoji, the ask first ----------

test('issue 18: the status line leads with the ask, carries no emoji, and ends on the quietest words', () => {
  const room = roomOf({ votes: [VOTE], status: { ...roomOf().status, people: [{ name: 'a', here: true }, { name: 'b', here: true }], beacon: 'investigating' } }) as any
  const snap = { line: '🔴 Landfall 168 · 3 new · 1 vote awaited', rooms: [room] }
  expect(statusLine(snap)).toBe('1 vote waiting on you · Landfall 168 · SEV2 · investigating · 2 here · Beacon investigating · 3 new')
  const none = { line: '🔴 Landfall 168 · 3 new', rooms: [{ ...room, votes: [] }] }
  expect(statusLine(none)).toBe('Landfall 168 · SEV2 · investigating · 2 here · Beacon investigating · 3 new')
  for (const s of [statusLine(snap), statusLine(none)]) expect(s).not.toMatch(/[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]/u)
  // The ask is there to be read in a surface that cuts from the right.
  expect(statusLine(snap).slice(0, 21)).toBe('1 vote waiting on you')
})

// ---------- issue 10: Home's tiles, one a row below 90 cells; 4/6 with no space ----------

test('issue 10: below 90 cells Home draws one tile a row, the title left and its label right; above, two across', async ($, on) => {
  const w = await world($, on, { rooms: [roomOf()], answers: ANSWERS, surface: 'desktop' })
  await $.command.run({ command: 'landfall', args: '' })
  await w.clock.settle()
  const narrow = await $.ui.mount(consolePane('desktop', 48, 50) as never)
  const tiles = (await narrow.findAll({ type: 'Box' })).filter((b) => String(b.key).startsWith('hw-c'))
  expect(tiles.length).toBeGreaterThanOrEqual(3)
  for (const t of tiles) expect(t.props.width).toBe('100%')
  const head = await narrow.find({ type: 'Box', key: 'hw-h1' })
  expect(head?.props.justifyContent).toBe('space-between')
  expect(await texts(narrow)).toContain('4/6')
  await narrow.unmount()
  const wide = await $.ui.mount(consolePane('desktop', 48, 120) as never)
  for (const t of (await wide.findAll({ type: 'Box' })).filter((b) => String(b.key).startsWith('hw-c'))) expect(t.props.width).toBe('48%')
})

test('issue 10: a tile sparkline is a plain image, so no frame paints a white band behind it', async ($, on) => {
  const w = await world($, on, { rooms: [roomOf()], answers: ANSWERS, surface: 'desktop' })
  await $.command.run({ command: 'landfall', args: '' })
  await w.clock.settle()
  const pane = await $.ui.mount(consolePane('desktop', 48, 50) as never)
  const svgs = (await pane.findAll({ type: 'Svg' })).filter((s: any) => ['5xx error rate', 'Healthy origins', 'Requests per minute'].includes(s.props.alt))
  expect(svgs.length).toBe(3)
  for (const s of svgs) expect((s as any).props.isInteractive).toBeFalsy()
})

// ---------- issue 20: not in a room, the desktop's header does not repeat the pane's title ----------

test('issue 20: off the terminal the not-in-a-room header is the mark and dim words, no second "Landfall"', async ($, on) => {
  await world($, on, { answers: { whoami: { ok: true, signedIn: false, instance: { web: 'https://app.landfalls.ai' } } } })
  await $.command.run({ command: 'landfall', args: '' })
  const desk = await $.ui.mount(consolePane('desktop') as never)
  const words = await texts(desk)
  expect(words).toContain('not signed in')
  expect(words).not.toContain('Landfall')
  await desk.unmount()
  const term = await $.ui.mount(consolePane('terminal') as never)
  expect(await texts(term)).toContain('Landfall')
})

// ---------- issue 8: a short inline Home keeps the people, the drop line and the keys ----------

test('issue 8: on a short terminal pane Home fits its rows, keeps Vote, People, the more line and the keys', async ($, on) => {
  const w = await world($, on, { rooms: [roomOf({ votes: [VOTE], status: { ...roomOf().status, beaconStep: { step: 6, text: 'comparing 5xx per target group in us-east-1' } } })], answers: ANSWERS })
  await $.command.run({ command: 'landfall', args: '' })
  await w.clock.settle()
  for (const bodyRows of [14, 15, 16, 18, 22]) {
    const pane = await $.ui.mount(consolePane('terminal', bodyRows) as never)
    const drawn: any = await pane.drawn()
    const rows = drawn.children[1].children as any[]
    const used = rows.reduce((n, el) => n + estRows(el, 84, true), 0)
    expect(used).toBeLessThanOrEqual(bodyRows - 1)
    const words = await texts(pane)
    expect(words).toContain('Your vote is waiting')
    expect(words.some((t) => t.startsWith('Here ·') || t === '+1 more' || t === 'alice')).toBe(true)
    expect(words.some((t) => t.startsWith('More in '))).toBe(true)
    expect(await pane.find({ type: 'Button', key: 'refresh' })).toBeDefined()
    expect(await pane.find({ type: 'Button', key: 'close' })).toBeDefined()
    await pane.unmount()
  }
})

test('issue 8: at 14 rows everyone who is here is still listed, the blank rows going before any person', async ($, on) => {
  const w = await world($, on, { rooms: [roomOf({ votes: [VOTE], status: { ...roomOf().status, beaconStep: { step: 6, text: 'comparing 5xx' }, people: [...roomOf().status.people, { name: 'bob', here: true }] } })], answers: ANSWERS })
  await $.command.run({ command: 'landfall', args: '' })
  await w.clock.settle()
  const pane = await $.ui.mount(consolePane('terminal', 14) as never)
  const words = await texts(pane)
  for (const name of ['alice', 'bob', 'carol', 'dave']) expect(words).toContain(name)
  expect(words.some((t) => t.startsWith('More in '))).toBe(true)
  expect(await pane.find({ type: 'Button', key: 'refresh' })).toBeDefined()
})

test('issue 8: the squeeze takes the blank rows, then the Beacon line, then people rows, and never the keys', () => {
  const blocks: any = { vote: { rows: 4 }, beacon: { rows: 1 }, people: { rows: 5, rowsHere: 5, total: 4, hereTotal: 4 } }
  const plan = planHome(blocks, 12, 6)
  expect(plan.shown).toEqual(['vote', 'beacon', 'people'])
  const got = squeezeHome({ ...plan, moreLine: 'More in Wall.' }, blocks, 12, 6, true)
  expect(got.compact).toBe(true)
  expect(got.noBeacon).toBe(true)
  // Roomy: nothing is taken.
  expect(squeezeHome(plan, blocks, 40, 6, true)).toEqual({})
  // Very short: People shrinks to the rows that fit, at least one.
  const tiny = squeezeHome({ ...plan, moreLine: 'More in Wall.' }, blocks, 11, 6, true)
  expect(tiny.peopleN).toBeGreaterThanOrEqual(1)
})

// ---------- issue 16: one blank row before the keys when the tab has no footer, never two ----------

test('issue 16: the sign-in state on the terminal has one blank row between its paragraph and its keys', async ($, on) => {
  await world($, on, { answers: { whoami: { ok: true, signedIn: false, instance: { web: 'https://app.landfalls.ai' } } } })
  await $.command.run({ command: 'landfall', args: '' })
  const pane = await $.ui.mount(consolePane('terminal') as never)
  const drawn: any = await pane.drawn()
  const rows = drawn.children[1].children as any[]
  const keys = rows.findIndex((r) => r.props?.key === 'keys')
  expect(keys).toBeGreaterThan(1)
  const blank = (r: any) => r?.type === 'Text' && (r.children || []).join('').trim() === ''
  expect(blank(rows[keys - 1])).toBe(true)
  expect(blank(rows[keys - 2])).toBe(false)
})

// ---------- issue 11: desktop button labels start with a capital ----------

test('issue 11: k.button capitalizes on the desktop and leaves the terminal alone', () => {
  const d = new Drawn(kitFor('desktop').button({ key: 'b', label: 'read the evidence', hotkey: '7', onPress() {} }))
  expect((d.rows as any).props.label).toBe('Read the evidence')
  const t = new Drawn(kitFor('terminal').button({ key: 'b', label: 'read the evidence', hotkey: '7', onPress() {} }))
  expect((t.rows as any).props.label).toBe('read the evidence')
})
