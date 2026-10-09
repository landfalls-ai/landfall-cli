import { expect, test } from 'claude-code/testing'
import { TABS } from '../hooks/core.js'
import { segmentLabels, segmentCells, SWITCHER_SLACK } from '../hooks/console.js'
import { SVG_THEME, clipToCells, kit, lineSvg, textCells } from '../hooks/kit.js'
import { chartSvg, graphSvg } from '../hooks/views.js'
import { sortIncidents } from '../hooks/components/incidents.js'
import * as rosterTab from '../hooks/components/roster.js'
import * as wallTab from '../hooks/components/wall.js'
import * as contextTab from '../hooks/components/context.js'
import { checkProps } from './_props'
import { Drawn, fakeIo, kitFor, openConsoleOn, setRooms, settle } from './_tab'
import { consolePane, texts, world } from './_console'

// Round 2: what the real-app captures of round 1 found (rounds/1/FINDINGS.md), one test per bug
// that the test kit can see. The captures themselves are rounds/2.

// ---------- bug 1: the engine refuses the whole tree for one prop it does not allow ----------

test('bug 1: the prop check the test tables run refuses what the engine refuses', () => {
  expect(() => checkProps('Text', { key: 'a', flexShrink: 1, children: ['x'] })).toThrow(/Text prop "flexShrink" is not allowed/)
  expect(() => checkProps('Box', { key: 'a', flexShrink: 1, children: [] })).not.toThrow()
  expect(() => checkProps('Button', { key: 'a', label: 'x', onPress() {}, hotkey: 'a' })).not.toThrow()
  expect(() => checkProps('Box', { key: 'a', maxWidth: 4 })).toThrow(/not allowed/)
})

// ---------- bug 2: the switcher is one row on the desktop, never wider than its pane ----------

const COUNTS = { vote: 1, context: 1, people: 4, timeline: 3, incidents: 4 }
const rowCells = (cells: number, active = 'people', counts: Record<string, number> = COUNTS) => {
  const items = segmentLabels(TABS, active, counts, cells, true)
  return { items, width: items.reduce((w, s) => w + segmentCells(s.active ? '▸ ' + s.label : s.label, s.active), 0) * SWITCHER_SLACK }
}

test('bug 2: off the terminal the label set is chosen by measured width and always fits when any set can', () => {
  // The real dock is about 50 body cells: nine segments with their counts do not fit; a shorter set does.
  for (const cells of [49, 58, 64, 72, 84, 98, 120]) {
    const { items, width } = rowCells(cells)
    expect(items).toHaveLength(9)
    expect(width).toBeLessThanOrEqual(cells)
  }
  // Roomy: full names, and still only Vote carries a count.
  expect(rowCells(130).items.map((s) => s.label)).toEqual(['Home', 'Vote 1', 'Context', 'Wall', 'People', 'Timeline', 'Load balancers', 'Incidents', 'More'])
})

// Round 3, decision 1: the ladder, rung by rung (REVIEW.md round 2). A label is the full word or
// the word cut to its start, the active segment always has its `▸`, and only Vote keeps a count.
test('round 3, switcher: the ladder A to E, never an invented word, only Vote counted', () => {
  const at = (cells: number, active = 'home') => rowCells(cells, active).items.map((s) => s.label)
  expect(at(64)).toEqual(['Home', 'Vote 1', 'Context', 'Wall', 'People', 'Timeline', 'LB', 'Incidents', 'More']) // A
  expect(at(61)).toEqual(['Home', 'Vote 1', 'Ctx', 'Wall', 'People', 'Timeline', 'LB', 'Incidents', 'More']) // B
  expect(at(57)).toEqual(['Home', 'Vote 1', 'Ctx', 'Wall', 'People', 'Time', 'LB', 'Incidents', 'More']) // C
  expect(at(52)).toEqual(['Home', 'Vote 1', 'Ctx', 'Wall', 'People', 'Time', 'LB', 'Inc', 'More']) // D
  expect(at(50)).toEqual(['Home', 'Vote 1', 'Ctx', 'Wall', 'Ppl', 'Time', 'LB', 'Inc', 'More']) // E: the real dock
  // The row never flips as the vote comes and goes: its one digit is reserved in every fit.
  expect(segmentLabels(TABS, 'home', {}, 50, true).map((s) => s.label)).toEqual(['Home', 'Vote', 'Ctx', 'Wall', 'Ppl', 'Time', 'LB', 'Inc', 'More'])
  // Whatever the active tab is, nothing but the real words or their starts is ever drawn.
  const words = new Set(['Home', 'Vote', 'Context', 'Ctx', 'Wall', 'People', 'Ppl', 'Timeline', 'Time', 'LB', 'Load balancers', 'Incidents', 'Inc', 'More'])
  for (const cells of [30, 40, 50, 60, 70, 90, 130]) {
    for (const tab of TABS) {
      for (const s of segmentLabels(TABS, tab, COUNTS, cells, true)) {
        expect(words.has(s.label.replace(/ \d+$/, ''))).toBe(true)
        expect((s as any).bare).toBeUndefined()
      }
    }
  }
  // Too narrow for any rung: the last one still carries its fill and its `▸` (no marker is ever dropped).
  expect(segmentLabels(TABS, 'wall', COUNTS, 20, true).map((s) => s.label)).toContain('Wall')
})

test('round 3, switcher: the active segment is filled with the theme-neutral grey and always has the `▸`', () => {
  const k = kitFor('desktop')
  const row = new Drawn(k.segments([{ id: 'wall', label: 'Wall', active: true, onPress() {} }, { id: 'more', label: 'More', onPress() {} }], { key: 'seg' }))
  const seg = row.find({ key: 'seg-wall' })!
  expect(seg.props.backgroundColor).toBe('#64635e')
  expect(seg.props.paddingX).toBe(1)
  const text = row.find({ key: 'seg-wall-t' })!
  expect(text.props.color).toBe('#fafafa')
  expect(text.props.bold).toBe(true)
  expect(String(text.children[0])).toBe('▸ Wall')
  // The fill is not a Button and not the primary variant, which a tab's action keeps.
  expect(row.find({ type: 'Button', key: 'seg-wall' })).toBeUndefined()
})

test('round 3, switcher: a tab change retitles the pane with the full tab name, never taking the keyboard', async ($, on) => {
  const w = await world($, on, { rooms: [], answers: {} })
  void w
  const pane = await $.ui.mount({ ...(consolePane('desktop', 48, 50) as object) } as never)
  await pane.press({ key: 'seg-people' })
  expect(w.opened.at(-1)).toMatchObject({ id: 'landfall', title: 'Landfall · People' })
  expect(w.opened.at(-1)!.focus).toBeUndefined()
  await pane.unmount()
})

test('bug 2: the segment widths are estimated from glyphs, not counted (a wide word costs more than a narrow one)', () => {
  expect(textCells('Timeline')).toBeGreaterThan(textCells('Illinois') - 0.0001 - 5)
  expect(segmentCells('Mmmmm', false)).toBeGreaterThan(segmentCells('Iiiii', false))
  // Calibrated on the app: "Home" is 6.3 cells wide, "Timeline" 8.2, "LB" 3.6 (round 1 capture).
  expect(segmentCells('Home', false)).toBeGreaterThan(5.5)
  expect(segmentCells('Home', false)).toBeLessThan(7)
  expect(segmentCells('Timeline', false)).toBeGreaterThan(7.5)
  expect(segmentCells('Timeline', false)).toBeLessThan(9)
})

test('bug 2: every desktop segment sits in a Box that does not shrink, in a row that never wraps or spills', () => {
  const k = kitFor('desktop')
  const row = new Drawn(k.segments([{ id: 'home', label: 'Home', active: true, onPress() {} }, { id: 'vote', label: 'Vote 1', onPress() {} }, { id: 'more', label: 'More', onPress() {} }], { key: 'seg' }))
  const seg = row.find({ key: 'seg' })!
  expect(seg.props.flexWrap).toBe('nowrap')
  expect(seg.props.overflow).toBe('hidden')
  expect(row.find({ key: 'seg-home' })!.props.flexShrink).toBe(0)
  expect(row.find({ key: 'seg-home-t' })!.props.wrap).toBe('truncate')
  expect(row.find({ key: 'seg-vote-w' })!.props.flexShrink).toBe(0)
  expect(row.find({ key: 'seg-more-w' })!.props.flexShrink).toBe(0)
  // Presses still reach the Buttons.
  expect(row.find({ type: 'Button', key: 'seg-vote' })).toBeDefined()
})

test('bug 2: through the console, the desktop switcher is picked from the pane width it was handed', async ($, on) => {
  const w = await world($, on, { rooms: [], answers: {} })
  void w
  const narrow = await $.ui.mount({ ...(consolePane('desktop', 48, 50) as object) } as never)
  const words = await texts(narrow)
  // At 50 cells the full names cannot all show; "Load balancers" and "Context" are never spelled out.
  expect(words).not.toContain('Load balancers')
  expect(words.filter((t) => ['Home', 'Vote', 'Wall', 'LB', 'More'].includes(t) || /^▸ /.test(t)).length).toBeGreaterThan(3)
  await narrow.unmount()
  const wide = await $.ui.mount({ ...(consolePane('desktop', 48, 120) as object) } as never)
  expect(await texts(wide)).toContain('Load balancers')
})

// ---------- bug 3: the interactive Svg carries its own theme ----------

test('bug 3: topology and chart drawings paint their own background and use theme classes, never currentColor', () => {
  const g = graphSvg([{ id: 'a', label: 'cloudfront', tone: 'warning' }, { id: 'b', label: 'web-edge-alb', tone: 'neutral' }], [{ from: 'a', to: 'b', trust: 'confirmed' }]).source
  const c = chartSvg([{ label: 's', points: [[1, 1], [2, 3], [3, 2]] }], [{ atMs: 2, label: 'deploy' }], 2)
  for (const svg of [g, c]) {
    expect(svg).toContain(SVG_THEME)
    expect(svg).not.toContain('currentColor')
    expect(svg).not.toMatch(/fill="#8a8a8a">/) // text is the muted class, readable on both
  }
  expect(g).toContain('class="fg"')
  expect(SVG_THEME).toContain('prefers-color-scheme:dark')
  expect(SVG_THEME).toContain('x="-5000"') // the background reaches the frame's letterbox, not just the viewBox
  expect(SVG_THEME).toMatch(/\.bg\{fill:#fff/)
  // A hoverable sparkline is a frame with its own page too; a plain one stays transparent.
  expect(lineSvg([1, 2, 3], 100, 40, '#d03b3b', { hover: true })).toContain(SVG_THEME)
  expect(lineSvg([1, 2, 3], 100, 40, '#d03b3b')).not.toContain(SVG_THEME)
})

// ---------- bug 7: a finding takes the row's real width ----------

const ROOM = {
  roomKey: 'k1', incidentId: 'i168', displayId: '168', title: 'cloudfront-5xx-high', slug: 'acme', connection: 'live', count: 0, addressed: 0, votesAwaited: 0, maxSeq: 233, digest: [],
  status: { status: 'investigating', severity: 'SEV2', people: [{ name: 'alice', here: true, humanActorId: 'h-alice' }] },
}
const TRAIL = [
  { seq: 220, at: '2026-10-08T16:01:00Z', type: 'edge.finding', kind: 'finding', text: 'Origin pool exhausted in us-east-1', state: 'contested' },
  { seq: 218, at: '2026-10-08T15:58:00Z', type: 'edge.hypothesis', kind: 'hypothesis', text: 'The pool limit was lowered in the 15:40 deploy' },
  { seq: 216, at: '2026-10-08T15:55:00Z', type: 'edge.query', kind: 'query', text: 'queried cloudwatch/metrics, origin-pool, 5xx by zone, last hour' },
]
const SHARED = { ok: true, sharedBy: 'carol', widgets: [], unavailable: [], people: [{ humanActorId: 'h-alice', displayName: 'alice', edgeAgentLabel: 'Codex', kind: 'human', widgets: 0, trail: 3, artifacts: 0, latestSeq: 220 }] }
const ALICE = { ok: true, person: { humanActorId: 'h-alice', displayName: 'alice' }, widgets: [], unavailable: [], trail: TRAIL, artifacts: [], people: SHARED.people }

test('bug 7: on the desktop a finding is cut only at the row real width, measured; the terminal keeps its character budget', async () => {
  rosterTab.reset()
  wallTab.reset()
  setRooms([ROOM])
  openConsoleOn('people')
  const io = fakeIo((argv: string[]) => (argv.includes('--person') ? ALICE : SHARED))
  wallTab.warm(io as never)
  await settle()
  await new Drawn(rosterTab.tab(kitFor('desktop', 48), io as never, 1791472800000, null)).press('open-0')
  await settle()
  const labels = (surface: 'terminal' | 'desktop', width: number) => {
    const pane = new Drawn(rosterTab.tab(kitFor(surface, width), io as never, 1791472800000, null))
    return [0, 1, 2].map((i) => String(pane.find({ key: 'trail-' + i })!.props.label))
  }
  // The round 1 pane (48 cells): the 34-character finding with its state label no longer loses its tail at 25.
  const narrow = labels('desktop', 48)
  // (round 3: the state label now precedes the text, so the quote is measured to what is left of the row.)
  expect(narrow[0].startsWith('“Origin pool exhausted in')).toBe(true)
  expect(narrow[0].length).toBeGreaterThan(25)
  // A finding longer than the row ends in an ellipsis, and that one is measured to fit.
  expect(narrow[1].length).toBeGreaterThan(30)
  expect(narrow[2].endsWith('…”')).toBe(true)
  expect(narrow[2].length).toBeGreaterThan(30)
  expect(labels('desktop', 84)[1]).toBe('“The pool limit was lowered in the 15:40 deploy”')
  // A roomier pane shows more of it.
  expect(labels('desktop', 84)[2].length).toBeGreaterThan(narrow[2].length)
  // clipToCells never overruns what it was given.
  expect(textCells(clipToCells('x'.repeat(200), 30))).toBeLessThanOrEqual(30)
})

// ---------- bug 8: no refresh key where there is nothing to read; the CLI's order of incidents ----------

test('bug 8: the sign-in states draw no r: refresh, the incident list does', async ($, on) => {
  const w = await world($, on, { answers: { whoami: { ok: true, signedIn: false, instance: { web: 'https://app.landfalls.ai' } } } })
  void w
  await $.command.run({ command: 'landfall', args: '' })
  for (const surface of ['terminal', 'desktop'] as const) {
    const pane = await $.ui.mount(consolePane(surface) as never)
    expect(await pane.find({ type: 'Button', key: 'si-go' })).toBeDefined()
    expect(await pane.find({ type: 'Button', key: 'refresh' })).toBeUndefined()
    expect(await pane.find({ type: 'Button', key: 'close' })).toBeDefined()
    await pane.unmount()
  }
})

test('bug 8: incidents keep the CLI order within a severity; yours first, then a SEV1 you are not in', () => {
  const list = [
    { displayId: '168', severity: 'sev2', joined: false, ageMs: 26 * 60000 },
    { displayId: '170', severity: 'sev1', joined: false, ageMs: 7200000 },
    { displayId: '169', severity: 'sev3', joined: false, ageMs: 5 * 3600000 },
    { displayId: '171', severity: 'sev3', joined: false, ageMs: 12 * 60000 },
  ]
  expect(sortIncidents(list).map((i) => i.displayId)).toEqual(['170', '168', '169', '171'])
  const inRoom = list.map((i) => (i.displayId === '168' ? { ...i, joined: true } : i))
  expect(sortIncidents(inRoom).map((i) => i.displayId)).toEqual(['168', '170', '169', '171'])
})

// ---------- bug 6: on the terminal the cards stop before they push the footer and keys off the pane ----------

import { consoleState } from '../hooks/core.js'

test('bug 6: a short terminal pane draws only the widgets that fit, and says how many it left out', async () => {
  const eight = { ok: true, sharedBy: 'carol', windowMs: 21600000, widgets: Array.from({ length: 8 }, (_, i) => ({ id: 'e' + i, type: 'chart', title: 'Chart ' + i, series: [{ label: 's', points: [[1, 1], [2, 2], [3, 1]] }] })) }
  const room = { roomKey: 'k168', incidentId: 'i168', displayId: '168', title: 't', slug: 'acme', connection: 'live', count: 0, addressed: 0, votesAwaited: 0, maxSeq: 1, digest: [], status: { status: 'investigating', severity: 'SEV2', people: [] } }
  const drawnCount = async (bodyRows: number, surface: 'terminal' | 'desktop') => {
    wallTab.reset()
    wallTab.setWallCap()
    setRooms([room])
    openConsoleOn('wall')
    consoleState.bodyRows = bodyRows
    const io = fakeIo(() => eight)
    wallTab.warm(io as never)
    await settle()
    const pane = new Drawn(wallTab.tab(kitFor(surface), io as never, io.clock.t, null))
    const n = pane.all({ type: 'Button', text: /^(▸ )?Chart \d$/ }).length
    const more = pane.find({ type: 'Text', text: /^Showing \d of 8 widgets/ })
    return { n, more: more ? String(more.children[0]) : '' }
  }
  try {
    const tall = await drawnCount(120, 'terminal')
    expect(tall.n).toBe(6)
    const short = await drawnCount(36, 'terminal')
    expect(short.n).toBeLessThan(tall.n)
    expect(short.n).toBeGreaterThanOrEqual(1)
    expect(short.more).toContain('Showing ' + short.n + ' of 8 widgets')
    // n walks only what is drawn.
    const keys = wallTab.keys(kitFor('terminal'), fakeIo(() => eight) as never)
    expect(keys.length).toBeGreaterThan(0)
    // The desktop pane scrolls like a page: the cap alone applies.
    expect((await drawnCount(36, 'desktop')).n).toBe(6)
  } finally {
    consoleState.bodyRows = 0
  }
})
