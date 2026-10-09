import { expect, test } from 'claude-code/testing'
import { FILL_W, LOW_CONTRAST, TONE, kit } from '../hooks/kit.js'
import * as tlMod from '../hooks/components/timeline.js'
import * as incMod from '../hooks/components/incidents.js'
import * as wallTab from '../hooks/components/wall.js'
import { consoleState } from '../hooks/core.js'
import { consolePane, roomOf, texts, world } from './_console'
import { Drawn, els, fakeIo, kitFor, openConsoleOn, setRooms, settle } from './_tab'
import { labelIn, tonedIn } from './_label'

// Round 4: what the round 3 review of the real console (rounds/3/REVIEW.md) found, the parts the
// test kit can see. The captures themselves are rounds/4.

// ---------- issue 1: a state word is never drawn in its tone color on a light page ----------

const hex = (c: string) => [1, 3, 5].map((i) => parseInt(c.slice(i, i + 2), 16))
const lum = (c: number[]) => {
  const [r, g, b] = c.map((v) => {
    const s = v / 255
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}
const contrast = (a: number[], b: number[]) => {
  const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}
const over = (fg: number[], bg: number[], alpha: number) => fg.map((v, i) => Math.round(v * alpha + bg[i] * (1 - alpha)))

test('issue 1: the tones whose own color fails 3:1 on its tint over a white page are exactly the ones drawn as dot plus ink', () => {
  const white = [255, 255, 255]
  const low = Object.entries(TONE)
    .filter(([name]) => name !== 'neutral' && name !== 'violet')
    .filter(([, color]) => contrast(hex(color), over(hex(color), white, 31 / 255)) < 3)
    .map(([name]) => name)
    .sort()
  expect(low).toEqual([...LOW_CONTRAST].sort())
  // The word in the theme's ink reads on the tint in both themes: the review's 13:1 and better.
  for (const color of Object.values(TONE)) {
    expect(contrast(hex('#141414'), over(hex(color), white, 31 / 255))).toBeGreaterThan(9)
    expect(contrast(hex('#fafafa'), over(hex(color), hex('#1d1c1b'), 31 / 255))).toBeGreaterThan(9)
  }
})

test('issue 1: the desktop label is a tinted box, its dot in the tone and its word in ink; the terminal keeps one colored text', () => {
  const d = kit(els as never, { surface: 'desktop', props: { bodyColumns: 86 } } as never)
  const label: any = d.pill('warning', 'warning', 'p')
  expect(label.type).toBe('Box')
  expect(label.props.backgroundColor).toBe('#fab2191f')
  expect(label.children.map((c: any) => [c.props.key, c.props.color, c.children.join('')])).toEqual([
    ['p-d', '#fab219', '● '],
    ['p-t', undefined, 'warning'],
  ])
  const found = labelIn(label, 'warning')!
  expect(found.tone).toBe('#fab219')
  expect(found.wordColor).toBeUndefined()
  const t = kit(els as never, { surface: 'terminal', props: { bodyColumns: 86 } } as never)
  const text: any = t.pill('warning', 'warning', 'p')
  expect(text.type).toBe('Text')
  expect(text.props.color).toBe('#fab219')
  expect(text.children).toEqual(['● warning'])
})

test('issue 1: toned words split into a dot and ink off the terminal for the low-contrast tones only', () => {
  const d = kit(els as never, { surface: 'desktop', props: { bodyColumns: 86 } } as never)
  const t = kit(els as never, { surface: 'terminal', props: { bodyColumns: 86 } } as never)
  for (const tone of ['warning', 'serious', 'good']) {
    const got = tonedIn(d.toned('4/6', tone, { key: 'v', bold: true }), '4/6')!
    expect(got).toMatchObject({ dotted: true, tone: TONE[tone as keyof typeof TONE], wordColor: undefined })
    expect(tonedIn(t.toned('4/6', tone, { key: 'v' }), '4/6')).toMatchObject({ dotted: false, wordColor: TONE[tone as keyof typeof TONE] })
  }
  for (const tone of ['critical', 'info']) {
    expect(tonedIn(d.toned('ERR', tone, { key: 'v' }), 'ERR')).toMatchObject({ dotted: false, wordColor: TONE[tone as keyof typeof TONE] })
  }
  // The word keeps its own styling.
  const bold: any = d.toned('4/6', 'warning', { key: 'v', bold: true })
  expect(bold.children[1].props.bold).toBe(true)
})

const T0 = 1791472800000
const ROOM = { roomKey: 'k168', incidentId: 'i168', displayId: '168', title: 'cloudfront-5xx-high', slug: 'acme', connection: 'live', count: 0, addressed: 0, votesAwaited: 0, maxSeq: 233, digest: [] as string[], status: { status: 'investigating', severity: 'SEV2', people: [] as unknown[] } }

test('issue 1: a toned stat value on the Wall is a dot and ink on the desktop (4/6 in yellow was 1.7:1)', async () => {
  const answer = { ok: true, sharedBy: 'carol', windowMs: 21600000, widgets: [{ id: 's2', type: 'stat', title: 'Healthy origins', value: '4', unit: '/6', tone: 'warning', spark: [6, 5, 4] }, { id: 's3', type: 'stat', title: 'Requests', value: '48', spark: [1, 2, 3] }] }
  for (const surface of ['terminal', 'desktop'] as const) {
    wallTab.reset()
    wallTab.setWallCap()
    setRooms([ROOM])
    openConsoleOn('wall')
    consoleState.bodyRows = 0
    const io = fakeIo(() => answer)
    wallTab.warm(io as never)
    await settle()
    const pane = new Drawn(wallTab.tab(kitFor(surface), io as never, io.clock.t, null))
    const toned = tonedIn(pane.rows, '4')!
    expect(toned.tone).toBe('#fab219')
    expect(toned.dotted).toBe(surface === 'desktop')
    if (surface === 'desktop') expect(toned.wordColor).toBeUndefined()
    // An untoned value is plain ink, never the neutral grey.
    expect(pane.find({ type: 'Text', text: '48' })?.props.color).toBeUndefined()
  }
})

test('issue 1: the stale footer and the reconnecting line are a dot and ink on the desktop', async ($, on) => {
  const w = await world($, on, { rooms: [roomOf({ connection: 'disconnected' })], answers: { wall: { ok: true, widgets: [] }, lb: { ok: true, loadBalancers: [] }, timeline: { ok: true, events: [] }, artifacts: { ok: true, artifacts: [] }, brief: { ok: false, error: 'x' } }, surface: 'desktop' })
  await $.command.run({ command: 'landfall', args: '' })
  await w.clock.settle()
  const pane = await $.ui.mount(consolePane('desktop') as never)
  const conn = tonedIn(await pane.drawn(), 'Reconnecting to the room…') ?? tonedIn(await pane.drawn(), (await texts(pane)).find((t) => /^Reconnecting/.test(t)) ?? '')
  expect(conn).toMatchObject({ dotted: true, tone: '#fab219' })
  expect(conn?.wordColor).toBeUndefined()
})

// ---------- issue 2: the timeline's text starts at one x ----------

const EVENTS = [
  { seq: 201, at: '2026-10-08T15:52:00Z', kind: 'status', glyph: '▲', tone: 'critical', text: 'Triggered by Datadog', who: 'Datadog', detail: '' },
  { seq: 203, at: '2026-10-08T21:11:00Z', kind: 'status', glyph: '●', tone: 'warning', text: 'Status set to investigating', who: 'carol', detail: '' },
  { seq: 209, at: '2026-10-08T16:06:00Z', kind: 'findings', glyph: '✓', tone: 'good', text: '#209 admitted: rollback ready', who: 'carol', detail: '' },
]

test('issue 2: each timeline row is a fixed time box, a fixed glyph slot, then the text in the box that takes the rest', async () => {
  setRooms([ROOM])
  openConsoleOn('timeline')
  const io = fakeIo(() => ({ ok: true, events: EVENTS, hasMore: false, oldestSeq: 201 }))
  await tlMod.warm(io as never)
  await settle()
  const desk = new Drawn(tlMod.tab(kitFor('desktop'), io as never, T0, null))
  const times = desk.all({ type: 'Box', key: 'tw' })
  expect(times).toHaveLength(EVENTS.length)
  expect(new Set(times.map((b) => b.props.width))).toEqual(new Set([tlMod.TIME_SLOT]))
  expect(times.every((b) => b.props.flexShrink === 0)).toBe(true)
  const texts = desk.all({ type: 'Box', key: 'xw' })
  expect(texts).toHaveLength(EVENTS.length)
  expect(texts.every((b) => b.props.flexGrow === 1 && b.props.minWidth === 0)).toBe(true)
  for (const e of EVENTS) {
    const line = desk.find({ type: 'Box', key: 'row-' + e.seq })!
    expect(line.children.map((c: any) => c.props.key)).toEqual(['tw', 'gs', 'xw'])
  }
  // The terminal's cells are monospaced: no boxes.
  const term = new Drawn(tlMod.tab(kitFor('terminal'), io as never, T0, null))
  expect(term.all({ type: 'Box', key: 'tw' })).toHaveLength(0)
})

// ---------- issue 3: a Home tile's sparkline fills its tile ----------

test('issue 3: a filling sparkline names no width, so the box takes its slot; its strokes do not scale', async ($, on) => {
  const d = kit(els as never, { surface: 'desktop', props: { bodyColumns: 86 } } as never)
  const svg: any = d.spark([1, 3, 2, 5], { key: 's', fill: true })
  expect(svg.type).toBe('Svg')
  expect(svg.props.width).toBeUndefined()
  expect(svg.props.height).toBeUndefined()
  expect(svg.props.isInteractive).toBeFalsy()
  expect(svg.props.source).toContain('width="' + FILL_W + '"')
  expect(svg.props.source).toContain('vector-effect="non-scaling-stroke"')
  // A fixed one still names its pixels.
  const fixed: any = d.spark([1, 3, 2, 5], { key: 's', px: 260, height: 44 })
  expect(fixed.props.width).toBe(260)
  // On Home the tile's sparkline is the filling one.
  const answers = { wall: { ok: true, sharedBy: 'carol', windowMs: 1, widgets: [{ id: 'w1', type: 'stat', title: 'Requests', value: '48', spark: [1, 2, 3, 2] }] }, lb: { ok: true, loadBalancers: [] }, timeline: { ok: true, events: [] }, artifacts: { ok: true, artifacts: [] }, brief: { ok: false, error: 'x' } }
  const w = await world($, on, { rooms: [roomOf()], answers, surface: 'desktop' })
  await $.command.run({ command: 'landfall', args: '' })
  await w.clock.settle()
  const pane = await $.ui.mount(consolePane('desktop', 48, 86) as never)
  const tile = (await pane.findAll({ type: 'Svg' })).find((x: any) => x.props.alt === 'Requests')
  expect(tile).toBeDefined()
  expect(tile?.props.width).toBeUndefined()
})

// ---------- issue 6: the terminal says practice on the meta line ----------

const row = (id: string, title: string, severity: string, status: string, extra: Record<string, unknown> = {}) => ({
  incidentId: id, displayId: 'Landfall ' + id.slice(1), title, slug: 'acme', severity, status, openedAt: '2026-10-08T15:38:00Z', joined: false, roomKey: '', webUrl: 'https://app.landfalls.ai/o/acme/incidents/' + id, ageMs: 1560000, ...extra,
})

test('issue 6: on the terminal `practice` sits on the meta line, not between the status and the age', async () => {
  setRooms([])
  openConsoleOn('incidents')
  const io = fakeIo(() => ({ ok: true, org: 'acme', incidents: [row('i180', 'cascade-demo', 'sev2', 'open', { practice: true, hereCount: 0 }), row('i181', 'checkout-p99', 'sev3', 'identified', { hereCount: 0 })] }))
  await incMod.load(io as never)
  const got: any = incMod.tab(kitFor('terminal', 84), io as never, T0, null)
  const pane = new Drawn(got.rows)
  const head = pane.find({ type: 'Box', key: 'r-i180' })!
  expect(head.children.map((c: any) => c.props.key)).toEqual(['sev-i180', 'inc-i180', 'st-i180', 'age-i180'])
  const meta = pane.find({ type: 'Box', key: 'meta-i180' })!
  expect(labelIn(meta, 'practice')?.tone).toBe('#8a5cd6')
  expect(new Drawn(meta).words().join(' ')).toContain('nobody here yet')
  // A card without it keeps its note line as before.
  expect(pane.find({ key: 'meta-i181' })).toBeUndefined()
  expect(pane.find({ type: 'Text', key: 'n-i181' })).toBeDefined()
})

// ---------- nit 8: Refresh and Close are always their own last row off the terminal ----------

test('nit 8: off the terminal Refresh and Close sit on their own last row under the tab keys, on every tab', async ($, on) => {
  const answers = { wall: { ok: true, sharedBy: 'carol', windowMs: 1, widgets: [{ id: 'w1', type: 'stat', title: 'Requests', value: '48', spark: [1, 2, 3, 2] }] }, lb: { ok: true, loadBalancers: [] }, timeline: { ok: true, events: [] }, artifacts: { ok: true, artifacts: [] }, brief: { ok: false, error: 'x' } }
  const w = await world($, on, { rooms: [roomOf()], answers, surface: 'desktop' })
  await $.command.run({ command: 'landfall', args: 'wall' })
  await w.clock.settle()
  const pane = await $.ui.mount(consolePane('desktop', 48, 86) as never)
  const own = (await pane.find({ type: 'Box', key: 'keys-own' })) as any
  const tail = (await pane.find({ type: 'Box', key: 'keys' })) as any
  expect(own).toBeDefined()
  expect(tail).toBeDefined()
  const keysOf = (b: any) => (b.children as any[]).filter((c) => c && c.type === 'Button').map((c) => c.props.key)
  expect(keysOf(tail)).toEqual(['refresh', 'close'])
  expect(keysOf(own)).not.toContain('refresh')
  expect(keysOf(own)).not.toContain('close')
})
