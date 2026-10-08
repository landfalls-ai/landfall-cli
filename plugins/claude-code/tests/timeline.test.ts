import { expect, test } from 'claude-code/testing'
import { NOW, buttonOf, fakeIo, fakeKit, hookTab, nodes, paneProps, setRoom, textsOf, watchRoom } from './_tab'
import { consoleState } from '../hooks/core.js'
import * as tlMod from '../hooks/components/timeline.js'

// `landfall timeline` as the Go CLI prints it (internal/cli/timeline.go
// timelineRow): oldest first, each row with its server `type`, and the page's
// `oldestSeq` beside `hasMore`.
const EVENTS = [
  { seq: 201, at: '2026-10-08T15:52:00Z', kind: 'status', type: 'incident.triggered', glyph: '▲', tone: 'critical', text: 'Triggered by Datadog: 5xx over 2% for 5 minutes', who: 'Datadog', detail: '' },
  { seq: 203, at: '2026-10-08T15:53:00Z', kind: 'status', type: 'status.changed', glyph: '●', tone: 'warning', text: 'Status set to investigating', who: 'carol', detail: 'war room' },
  { seq: 204, at: '2026-10-08T15:53:20Z', kind: 'beacon', type: 'agent.run.started', glyph: '◆', tone: 'violet', text: 'Beacon run 1 started', who: 'Beacon', detail: 'focus web-edge' },
  { seq: 206, at: '2026-10-08T15:58:00Z', kind: 'findings', type: 'claim.staged', glyph: '◇', tone: 'info', text: 'alice staged #206 origin pool exhausted', who: 'alice', detail: '' },
  { seq: 209, at: '2026-10-08T16:06:00Z', kind: 'findings', type: 'claim.admitted', glyph: '✓', tone: 'good', text: '#209 admitted: rollback ready', who: 'carol', detail: 'proposed by carol, accepted by bob and dave' },
  { seq: 212, at: '2026-10-08T16:09:00Z', kind: 'findings', type: 'claim.staged', glyph: '◇', tone: 'info', text: 'bob staged #212 5xx matches v2.3.1', who: 'bob', detail: '' },
  { seq: 214, at: '2026-10-08T16:10:00Z', kind: 'people', type: 'edge.participant.joined', glyph: '·', tone: 'neutral', text: 'dave joined from Claude Code', who: 'dave', detail: '' },
]
const OLDER = [{ seq: 150, at: '2026-10-08T15:40:00Z', kind: 'findings', type: 'edge.finding', glyph: '◇', tone: 'info', text: 'carol noted the CDN alarm', who: 'carol', detail: '' }]
const FINDINGS = EVENTS.filter((e) => e.kind === 'findings')


// The CLI as the tests want it: kind and --before honoured, as the contract says. `argv` is what
// the component hands io.run (no leading binary).
function answerFor(args: readonly string[]) {
  const at = (flag: string) => {
    const i = args.indexOf(flag)
    return i >= 0 ? args[i + 1] : undefined
  }
  if (at('--before')) return { ok: true, events: OLDER, hasMore: false, oldestSeq: 150 }
  if (at('--kind') === 'findings') return { ok: true, events: FINDINGS, hasMore: false, oldestSeq: 206 }
  if (at('--kind') === 'other') return { ok: true, events: [], hasMore: false }
  return { ok: true, events: EVENTS, hasMore: true, oldestSeq: 201 }
}

function setup(over: Record<string, unknown> = {}) {
  const room = watchRoom({ maxSeq: 214, ...over })
  setRoom([room])
  const f = fakeIo({ run: async (args: string[]) => (f.log.runs.push(args), answerFor(args)) })
  return { room, ...f }
}

const eventButtons = (tree: unknown) => nodes(tree, 'Button').filter((b) => /^ev-/.test(String(b.props.key)))

for (const surface of ['terminal', 'desktop'] as const) {
  test(`${surface}: the engine draws the chips, one row per event oldest first, glyphs in their tone`, async ($, on) => {
    const { io } = setup()
    await tlMod.warm(io)
    hookTab(on, 'd-tl', (k, i, now, args) => tlMod.tab(k, i, now, args))
    const pane = await $.ui.mount({ ...paneProps('d-tl'), surface } as never)
    // The chips: letters, the active one `▸ <kind>` at full strength, the others dim.
    const chips = (await pane.findAll({ type: 'Button' })).filter((b: any) => /^kind-/.test(String(b.key)))
    expect(chips.map((b: any) => b.props.hotkey)).toEqual(['a', 'f', 's', 'b', 'p', 'o'])
    expect(chips[0].text).toBe(surface === 'terminal' ? '▸ all' : '▸ All')
    expect(chips[0].props.dimColor).toBeUndefined()
    expect(chips[3].text).toBe(surface === 'terminal' ? 'Beacon' : 'Beacon')
    expect(chips.slice(1).every((b: any) => b.props.dimColor === true)).toBe(true)
    // Earlier events, dim, while the CLI says there is more.
    const more = await pane.find({ type: 'Button', key: 'tl-more' })
    expect(more?.text).toBe(surface === 'terminal' ? 'earlier events' : 'Earlier events')
    expect(more?.props.hotkey).toBe('m')
    expect(more?.props.dimColor).toBe(true)
    // Rows sorted by seq, each a Button; no chip repeats a row's look.
    const rows = (await pane.findAll({ type: 'Button' })).filter((b: any) => /^ev-/.test(String(b.key)))
    expect(rows.map((b: any) => b.key)).toEqual(['ev-201', 'ev-203', 'ev-204', 'ev-206', 'ev-209', 'ev-212', 'ev-214'])
    expect(rows[0].text).toBe('Triggered by Datadog: 5xx over 2% for 5 minutes')
    expect(rows[1].text).toBe('Status set to investigating · carol')
    expect((await pane.find({ type: 'Text', text: '▲' }))?.props.color).toBe('#d03b3b')
    expect((await pane.find({ type: 'Text', text: '✓' }))?.props.color).toBe('#0ca30c')
    expect(await pane.find({ type: 'Text', text: '15:52' })).toBeDefined()
    expect((await pane.find({ type: 'Text', text: 'Enter on a row quotes it into your prompt' }))?.props.dimColor).toBe(true)
    expect(await pane.find({ type: 'Text', text: 'live · updated 0s ago' })).toBeDefined()
    // The detail line shows where it draws.
    const detail = await pane.find({ type: 'Text', text: 'proposed by carol, accepted by bob and dave' })
    if (surface === 'terminal') expect(detail).toBeUndefined()
    else expect(detail).toBeDefined()
  })

  test(`${surface}: a chip filters by kind, m reads further back, a press quotes the row as a draft`, async () => {
    const { io, log } = setup()
    const k = fakeKit(surface)
    await tlMod.warm(io)
    expect(log.runs[0].slice(0, 2)).toEqual(['timeline', '--host'])
    expect(log.runs[0]).toContain('--limit')
    const draw = () => tlMod.tab(k, io, NOW, null)

    await buttonOf(draw(), 'ev-201')!.props.onPress()
    expect(log.filled).toEqual([{ text: '> 15:52 Triggered by Datadog: 5xx over 2% for 5 minutes\n\n', mode: 'insert' }])

    await buttonOf(draw(), 'tl-more')!.props.onPress()
    expect(log.runs.at(-1)).toContain('--before')
    expect(log.runs.at(-1)![log.runs.at(-1)!.indexOf('--before') + 1]).toBe('201')
    let keys = eventButtons(draw()).map((b) => b.props.key)
    expect(keys[0]).toBe('ev-150')
    expect(keys).toHaveLength(8)
    expect(buttonOf(draw(), 'tl-more')).toBeUndefined()

    await buttonOf(draw(), 'kind-findings')!.props.onPress()
    // The chip's own read, and a small unfiltered one that keeps Home's Latest current.
    const last = log.runs.filter((a) => a.includes('--kind')).at(-1)!
    expect(last[last.indexOf('--kind') + 1]).toBe('findings')
    keys = eventButtons(draw()).map((b) => b.props.key)
    expect(keys).toEqual(['ev-206', 'ev-209', 'ev-212'])
    expect(buttonOf(draw(), 'kind-findings')!.props.label).toBe(surface === 'terminal' ? '▸ findings' : '▸ Findings')
    expect(buttonOf(draw(), 'kind-all')!.props.label).toBe(surface === 'terminal' ? 'all' : 'All')

    await buttonOf(draw(), 'kind-other')!.props.onPress()
    expect(textsOf(draw())).toContain('Nothing of this kind yet.')
    await buttonOf(draw(), 'kind-all')!.props.onPress()
    expect(log.toasts).toEqual([])
  })
}

test('/landfall timeline <chip> chooses that chip once; the person\'s own chip wins after that', async () => {
  const { io, log } = setup()
  const k = fakeKit('terminal')
  consoleState.args = 'findings'
  tlMod.tab(k, io, NOW, 'findings')
  expect(consoleState.args).toBeNull()
  await new Promise((r) => setTimeout(r, 0))
  expect(log.runs.filter((a) => a.includes('--kind')).map((a) => a[a.indexOf('--kind') + 1])).toEqual(['findings'])
  expect(buttonOf(tlMod.tab(k, io, NOW, null), 'kind-findings')!.props.label).toBe('▸ findings')
  // The same words drawn again (a console that passes them on every draw) change nothing.
  await buttonOf(tlMod.tab(k, io, NOW, 'findings'), 'kind-status')!.props.onPress()
  tlMod.tab(k, io, NOW, 'findings')
  await new Promise((r) => setTimeout(r, 0))
  expect(buttonOf(tlMod.tab(k, io, NOW, 'findings'), 'kind-status')!.props.label).toBe('▸ status')
})

test('a new room event reads the newest page and lays it over the rows shown', async () => {
  const ev = (seq: number, text: string) => ({ seq, at: '2026-10-08T15:52:00Z', kind: 'other', type: 'chat.message', glyph: '·', tone: 'neutral', text, who: '', detail: '' })
  let page = [ev(201, 'Triggered'), ev(204, 'Beacon at step 3')]
  const room = watchRoom({ maxSeq: 204 })
  setRoom([room])
  const f = fakeIo({ run: async (args: string[]) => (f.log.runs.push(args), { ok: true, events: page, hasMore: false, oldestSeq: page[0].seq }) })
  const k = fakeKit('terminal')
  consoleState.open = true
  await tlMod.warm(f.io)
  expect(eventButtons(tlMod.tab(k, f.io, NOW, null)).map((b) => b.props.key)).toEqual(['ev-201', 'ev-204'])
  // Ticks with no new event read nothing.
  await tlMod.tick(f.io, NOW + 20000)
  expect(f.log.runs).toHaveLength(1)
  // A new event: the newest page again, its run folded anew into one row.
  page = [ev(201, 'Triggered'), ev(238, 'Beacon at step 6'), ev(240, 'bob: rolling back')]
  tlMod.onSnapshot(f.io, { rooms: [{ ...room, maxSeq: 240 }] })
  await new Promise((r) => setTimeout(r, 0))
  expect(f.log.runs).toHaveLength(2)
  expect(eventButtons(tlMod.tab(k, f.io, NOW, null)).map((b) => b.props.key)).toEqual(['ev-201', 'ev-238', 'ev-240'])
  // The console closing stops it.
  consoleState.open = false
  await tlMod.tick(f.io, NOW + 30000)
  tlMod.onSnapshot(f.io, { rooms: [{ ...room, maxSeq: 250 }] })
  expect(f.log.runs).toHaveLength(2)
})

test('the badge counts news past the seq the tab was last drawn at, and drawing the tab clears it', async () => {
  const { io, room } = setup({ maxSeq: 214, count: 2, digest: ['#212 chat.message [bob@acme.com] — hi', '#214 chat.message [carol@acme.com] — hello'] })
  const k = fakeKit('terminal')
  await tlMod.warm(io)
  expect(tlMod.badge()).toBe(2)
  tlMod.tab(k, io, NOW, null)
  expect(tlMod.badge()).toBeNull()
  // News newer than what was drawn counts again.
  setRoom([{ ...room, maxSeq: 220, count: 1, digest: ['#220 chat.message [bob@acme.com] — more'] }])
  expect(tlMod.badge()).toBe(1)
  setRoom([])
  expect(tlMod.badge()).toBeNull()
})

test('the first read says it is reading; a failed first read shows its sentence; a later failure keeps the rows and goes stale', async () => {
  const room = watchRoom()
  setRoom([room])
  let answer: unknown = { ok: false, error: 'Your sign-in expired. Run landfall login.' }
  const f = fakeIo({ run: async () => answer })
  const k = fakeKit('terminal')
  expect(textsOf(tlMod.tab(k, f.io, NOW, null))).toContain('Reading the timeline…')
  await tlMod.warm(f.io)
  expect(textsOf(tlMod.tab(k, f.io, NOW, null))).toContain('Your sign-in expired. Run landfall login.')
  answer = { ok: true, events: EVENTS, hasMore: false, oldestSeq: 201 }
  await tlMod.refresh(f.io)
  expect(eventButtons(tlMod.tab(k, f.io, NOW, null))).toHaveLength(7)
  answer = { ok: false, error: 'The room did not answer.' }
  consoleState.open = true
  tlMod.onSnapshot(f.io, { rooms: [{ ...room, maxSeq: 999 }] })
  await new Promise((r) => setTimeout(r, 0))
  const tree = tlMod.tab(k, f.io, NOW + 90000, null)
  expect(eventButtons(tree)).toHaveLength(7)
  expect(textsOf(tree).some((t) => t.startsWith('stale · updated 1m ago · The room did not answer.'))).toBe(true)
  consoleState.open = false
})

test('an empty timeline says so', async () => {
  setRoom([watchRoom()])
  const f = fakeIo({ run: async () => ({ ok: true, events: [], hasMore: false }) })
  await tlMod.warm(f.io)
  expect(textsOf(tlMod.tab(fakeKit('terminal'), f.io, NOW, null))).toContain('Nothing on the timeline yet.')
})

test('Home\'s Latest is the newest unfiltered rows, even while a chip filters the tab', async () => {
  const { io } = setup()
  const k = fakeKit('terminal')
  await tlMod.warm(io)
  expect(tlMod.latestRows(3).map((e: any) => e.seq)).toEqual([209, 212, 214])
  await buttonOf(tlMod.tab(k, io, NOW, null), 'kind-findings')!.props.onPress()
  await new Promise((r) => setTimeout(r, 0))
  expect(tlMod.latestRows(3).map((e: any) => e.seq)).toEqual([209, 212, 214])
})

test('a different room starts the timeline over', async () => {
  const { io } = setup()
  const k = fakeKit('terminal')
  await tlMod.warm(io)
  await buttonOf(tlMod.tab(k, io, NOW, null), 'kind-findings')!.props.onPress()
  setRoom([watchRoom()])
  const tree = tlMod.tab(k, io, NOW, null)
  expect(textsOf(tree)).toContain('Reading the timeline…')
  expect(buttonOf(tree, 'kind-all')!.props.label).toBe('▸ all')
})

test('text answers in words, with the chip asked for, and with the sentence when it cannot read', async () => {
  const { io, log } = setup()
  expect(await tlMod.text(io, 'findings')).toBe(
    ['Timeline · Acme 168 · cloudfront-5xx-high · findings', '  15:58 ◇ alice staged #206 origin pool exhausted', '  16:06 ✓ #209 admitted: rollback ready · carol', '  16:09 ◇ bob staged #212 5xx matches v2.3.1'].join('\n'),
  )
  expect(log.runs[0][log.runs[0].indexOf('--kind') + 1]).toBe('findings')
  setRoom([watchRoom()])
  expect(await tlMod.text(fakeIo({ run: async () => ({ ok: false, error: 'Your sign-in expired. Run landfall login.' }) }).io, '')).toBe('Your sign-in expired. Run landfall login.')
})

test('no standalone pane or command is left behind', () => {
  const hooks: unknown[] = []
  tlMod.install((...a: unknown[]) => void hooks.push(a))
  expect(hooks).toEqual([])
  expect(tlMod.keys(fakeKit('terminal'), fakeIo().io, NOW, null)).toEqual([])
})

test('the timeline draws on mobile too', async () => {
  const { io } = setup()
  await tlMod.warm(io)
  expect(buttonOf(tlMod.tab(fakeKit('mobile'), io, NOW, null), 'ev-201')).toBeDefined()
})
