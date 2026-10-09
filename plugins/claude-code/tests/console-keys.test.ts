import { expect, test } from 'claude-code/testing'
import { KEYS_AGAIN_MS } from '../hooks/console.js'
import { consolePane, roomOf, world } from './_console'

// The console's keyboard (0.5.4), two bugs found in a live terminal run:
//   1. after the person typed `/landfall vote` (any tab) the pane drew without the keyboard, so `c`,
//      `x`, `l` went into the prompt. The engine honours `focus` on open only while the composer is
//      empty, and a typed command runs while their text is still there: the request was refused once
//      and never again. The console asks again while the pane does not hold the keyboard.
//   2. with the ring on nothing, Enter closed the pane. Each tab with rows draws `autoFocus` on its
//      selected row, which the engine lands the ring on when the pane takes the keyboard.

const row = (id: string, title: string, severity: string, status: string, extra: Record<string, unknown> = {}) => ({
  incidentId: id,
  displayId: 'Landfall ' + id.slice(1),
  title,
  slug: 'acme',
  severity,
  status,
  openedAt: '2026-10-08T15:38:00Z',
  joined: false,
  roomKey: '',
  webUrl: 'https://app.landfalls.ai/o/acme/incidents/' + id,
  ageMs: 180000,
  ...extra,
})
const INCIDENTS = { ok: true, org: 'acme', incidents: [row('i172', 'orders-db-replica-lag', 'sev1', 'open'), row('i171', 'checkout-latency-p99', 'sev3', 'identified')] }
const JOINED = { ok: true, roomKey: 'k172', incidentId: 'i172', displayId: 'Landfall 172', title: 'orders-db-replica-lag' }
const BRIEF = { ok: true, text: 'Landfall 172', asOfSeq: 9, counts: { established: 0, open: 0, participants: 2 }, established: [], open: [], participants: [] }
const EVENTS = [
  { seq: 201, at: '2026-10-08T15:52:00Z', kind: 'status', type: 'incident.triggered', glyph: '▲', tone: 'critical', text: 'Triggered by Datadog', who: 'Datadog', detail: '' },
  { seq: 203, at: '2026-10-08T15:53:00Z', kind: 'status', type: 'status.changed', glyph: '●', tone: 'warning', text: 'Status set to investigating', who: 'carol', detail: '' },
]
const art = (id: string, filename: string) => ({ artifactId: id, filename, contentType: 'text/markdown', size: 4096, sharedAt: '2026-10-08T16:00:00Z', sharer: { displayName: 'bob', kind: 'member', edgeAgentLabel: '', humanActorId: 'h-bob' } })
const ARTIFACTS = { ok: true, artifacts: [art('a1', 'runbook.md'), art('a2', 'notes.md')] }

const ANSWERS = {
  incidents: INCIDENTS,
  join: JOINED,
  brief: BRIEF,
  timeline: { ok: true, events: EVENTS, hasMore: false, oldestSeq: 201 },
  artifacts: ARTIFACTS,
  artifact: { ok: true, artifactId: 'a1', filename: 'runbook.md', contentType: 'text/markdown', size: 4096, text: 'steps', totalChars: 5, truncated: false },
  wall: { ok: true, widgets: [] },
  lb: { ok: true, loadBalancers: [] },
  vote: { ok: true, votes: [] },
}

// panesSaying is what `$.ui.panes()` answers, read at each call: the engine's record of the pane.
const panesSaying = (state: { focused: boolean }) => () => [{ id: 'landfall', title: 'Landfall', isShown: true, isFocused: state.focused, isPlaced: true }]

// ---------- bug 1: the keyboard after the person's own command ----------

for (const tab of ['vote', 'context', 'people', 'timeline', 'incidents']) {
  test(`/landfall ${tab}: refused the keyboard at first, the console asks again until the pane holds it`, async ($, on) => {
    const state = { focused: false }
    const w = await world($, on, { rooms: [roomOf()], answers: ANSWERS, panes: panesSaying(state) })
    await $.command.run({ command: 'landfall', args: tab })
    expect(w.opened).toHaveLength(1)
    expect(w.opened[0]).toMatchObject({ id: 'landfall', focus: true })
    // The composer empties: the next beat asks again, the same id and the same request.
    await w.clock.advance(KEYS_AGAIN_MS[0])
    expect(w.opened).toHaveLength(2)
    expect(w.opened[1]).toMatchObject({ id: 'landfall', focus: true, columns: 84 })
    // Once the engine says the pane holds the keyboard, it stops asking.
    state.focused = true
    await w.clock.advance(5000)
    expect(w.opened).toHaveLength(2)
    expect(w.counts.mcp).toBe(0)
    expect(w.filled).toEqual([])
  })
}

test('asking again is bounded, and ends with the person closing the console', async ($, on) => {
  const state = { focused: false }
  const w = await world($, on, { rooms: [roomOf()], answers: ANSWERS, panes: panesSaying(state) })
  await $.command.run({ command: 'landfall', args: 'vote' })
  await w.clock.advance(60000)
  // The first request and one per beat, never more: a person who is typing is refused every time.
  expect(w.opened).toHaveLength(1 + KEYS_AGAIN_MS.length)
})

test('the person closing the console cancels the beats still to come', async ($, on) => {
  const state = { focused: false }
  const w = await world($, on, { rooms: [roomOf()], answers: ANSWERS, panes: panesSaying(state) })
  await $.command.run({ command: 'landfall', args: 'vote' })
  const pane = await $.ui.mount(consolePane('terminal') as never)
  await pane.press({ key: 'close' })
  const before = w.opened.length
  await w.clock.advance(60000)
  expect(w.opened).toHaveLength(before)
})

test('a pane the engine does not list is left alone, and a failing panes() ends the asking quietly', async ($, on) => {
  const w = await world($, on, { rooms: [roomOf()], answers: ANSWERS, panes: () => [] })
  await $.command.run({ command: 'landfall', args: 'vote' })
  await w.clock.advance(5000)
  expect(w.opened).toHaveLength(1)
})

// ---------- bug 2: Enter acts on the selected row ----------

// autoKeyOf is the key of the first Button drawn `autoFocus`, in document order: where the engine
// lands the ring when the pane holds the keyboard with the ring on nothing.
function autoKeyOf(el: any): string {
  if (!el || typeof el !== 'object') return ''
  if (el.type === 'Button' && el.props?.autoFocus && typeof el.props.key === 'string') return el.props.key
  for (const c of el.children ?? []) {
    const got = autoKeyOf(c)
    if (got) return got
  }
  return ''
}

const ROWS = [
  { tab: 'incidents', surface: 'terminal', key: 'inc-i172', pressed: (w: any) => expect(w.runs.find((r: string[]) => r[1] === 'join')).toEqual(['landfall', 'join', '--incident', 'i172', '--host', 'claude-code']) },
  { tab: 'incidents', surface: 'desktop', key: 'inc-i172', pressed: (w: any) => expect(w.runs.find((r: string[]) => r[1] === 'join')).toBeDefined() },
  { tab: 'people', surface: 'terminal', key: 'open-0', pressed: (w: any) => expect(w.runs.some((r: string[]) => r[1] === 'wall')).toBe(true) },
  { tab: 'people', surface: 'desktop', key: 'open-0', pressed: (w: any) => expect(w.runs.some((r: string[]) => r[1] === 'wall')).toBe(true) },
  { tab: 'timeline', surface: 'terminal', key: 'ev-201', pressed: (w: any) => expect(w.filled.join('')).toContain('Triggered by Datadog') },
  { tab: 'timeline', surface: 'desktop', key: 'ev-201', pressed: (w: any) => expect(w.filled.join('')).toContain('Triggered by Datadog') },
  { tab: 'context', surface: 'terminal', key: 'art-a1', pressed: (w: any) => expect(w.runs.some((r: string[]) => r[1] === 'artifact')).toBe(true) },
  { tab: 'context', surface: 'desktop', key: 'art-add-a1', pressed: (w: any) => expect(w.runs.some((r: string[]) => r[1] === 'artifact')).toBe(true) },
] as const

for (const c of ROWS) {
  test(`${c.surface}: ${c.tab} starts the focus ring on its selected row, so Enter acts on it`, async ($, on) => {
    const w = await world($, on, { rooms: c.tab === 'incidents' ? [] : [roomOf()], surface: c.surface, answers: { ...ANSWERS, whoami: { ok: true, signedIn: true, org: 'acme' } } })
    await $.command.run({ command: 'landfall', args: c.tab })
    const pane = await $.ui.mount(consolePane(c.surface) as never)
    await w.clock.settle()
    // The engine lands the ring on the first Button drawn `autoFocus`; Enter on it is its press.
    expect(autoKeyOf(await pane.drawn())).toBe(c.key)
    const auto = (await pane.findAll({ type: 'Button' })).filter((b: any) => b.props.autoFocus)
    expect(auto.map((b: any) => b.key)).toContain(c.key)
    await pane.press({ key: c.key })
    await w.clock.settle()
    c.pressed(w)
    expect(w.counts.mcp).toBe(0)
  })
}

test('People: while a line is being claimed the field keeps the ring, not the first name', async ($, on) => {
  await world($, on, { rooms: [roomOf()], answers: ANSWERS })
  await $.command.run({ command: 'landfall', args: 'people' })
  const pane = await $.ui.mount(consolePane('terminal') as never)
  await pane.press({ key: 'claim' })
  const tree = await pane.drawn()
  expect(autoKeyOf(tree)).not.toBe('open-0')
})
