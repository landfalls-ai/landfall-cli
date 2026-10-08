import { expect, test } from 'claude-code/testing'
import { Drawn, fakeIo, kitFor, openConsoleOn, setRooms, settle, textOf } from './_tab'
import { consoleState, room } from '../hooks/core.js'
import { clipToCells, textCells, BUTTON_CHROME } from '../hooks/kit.js'
import * as rosterTab from '../hooks/components/roster.js'
import * as wallTab from '../hooks/components/wall.js'
import * as cx from '../hooks/components/context.js'

// Round 3: the People and Context items of rounds/2/REVIEW.md (4, 5, 11, 12, 15, 16), one test per
// fix the test kit can see. The captures are rounds/3.

const NOW = 1791472800000
const ROOM = {
  roomKey: 'k1', incidentId: 'i168', displayId: '168', title: 'cloudfront-5xx-high', slug: 'acme', connection: 'live', count: 0, addressed: 0, votesAwaited: 0, maxSeq: 233, digest: [],
  status: {
    status: 'investigating',
    severity: 'SEV2',
    lines: [{ claimId: 'c1', label: 'eu-west-1 5xx', lineKey: 'eu-west-1-5xx', owner: 'dave', you: true, ageMs: 240000 }],
    people: [
      { name: 'alice', here: true, humanActorId: 'h-alice', agents: [{ tool: 'Codex', label: 'alice-codex', here: true, doing: 'origin pool sizing' }], latest: { seq: 198, text: 'Origin pool exhausted in us-east-1', state: 'contested', ageMs: 540000 } },
      { name: 'bob', here: true, humanActorId: 'h-bob', agents: [{ tool: 'Claude Code', label: 'bob-claude-code', here: true, doing: 'on v2.3.1 deploy diff and a long tail of other words' }], latest: { seq: 212, text: 'The 5xx rise matches the v2.3.1 deploy and then some more words after it', state: 'staged', ageMs: 120000 } },
      { name: 'carol', here: true, browser: true, humanActorId: 'h-carol', latest: { seq: 205, text: 'Rollback of web-edge v2.3.1 is ready', state: 'admitted', ageMs: 180000 } },
      { name: 'dave', you: true, here: true, humanActorId: 'h-dave', agents: [{ tool: 'Claude Code', label: 'dave-claude-code', here: true }] },
    ],
  },
}
const TRAIL = [
  { seq: 220, at: '2026-10-08T16:01:00Z', type: 'edge.finding', kind: 'finding', text: 'Origin pool exhausted in us-east-1', state: 'contested' },
  { seq: 218, at: '2026-10-08T15:58:00Z', type: 'edge.hypothesis', kind: 'hypothesis', text: 'The pool limit was lowered in the 15:40 deploy and nobody noticed for a long while afterwards, which is the whole story of the evening' },
]
const ARTIFACTS = [
  { artifactId: 'a1', filename: 'origin-pool-notes.md', contentType: 'text/markdown', size: 4096, sharedAt: '2026-10-08T15:08:00Z', sharer: { displayName: 'alice', kind: 'agent', humanActorId: 'h-alice' } },
  { artifactId: 'a2', filename: 'screenshot.png', contentType: 'image/png', size: 1258291, sharedAt: '2026-10-08T15:00:00Z', sharer: { displayName: 'alice', kind: 'human', humanActorId: 'h-alice' } },
]
const PEOPLE = ROOM.status.people.map((p) => ({ humanActorId: (p as any).humanActorId, displayName: p.name, kind: 'human', widgets: 0, trail: 2, artifacts: 2, latestSeq: 1 }))
const SHARED = { ok: true, sharedBy: 'carol', widgets: [], unavailable: [], people: PEOPLE }
const ALICE = { ok: true, person: { humanActorId: 'h-alice', displayName: 'alice' }, widgets: [], unavailable: [], trail: TRAIL, artifacts: ARTIFACTS, people: PEOPLE }

function begin() {
  rosterTab.reset()
  wallTab.reset()
  setRooms([ROOM])
  openConsoleOn('people')
}
const answer = (argv: string[]) => (argv.includes('--person') ? ALICE : SHARED)
const draw = (surface: 'terminal' | 'desktop', io: any, width = 84, args: string | null = null) => new Drawn(rosterTab.tab(kitFor(surface, width), io, NOW, args))

async function personView(surface: 'terminal' | 'desktop', width = 84) {
  begin()
  const io = fakeIo(answer)
  wallTab.warm(io as never)
  await settle()
  await draw(surface, io, width).press('open-0')
  await settle()
  return { io, pane: draw(surface, io, width), width }
}

// ---------- item 4: one claim-state tone map ----------

test('item 4: People and Context draw a claim state in the same tone, on both surfaces', async () => {
  for (const surface of ['terminal', 'desktop'] as const) {
    begin()
    const pane = draw(surface, fakeIo(answer))
    expect(pane.find({ type: 'Text', text: '● contested' })?.props.color).toBe('#ec835a')
    expect(pane.find({ type: 'Text', text: '● staged' })?.props.color).toBe('#898781')
    expect(pane.find({ type: 'Text', text: '● admitted' })?.props.color).toBe('#0ca30c')
  }
  expect(rosterTab.latestTone('contested')).toBe('serious')
  expect(rosterTab.latestTone('staged')).toBe('neutral')
  expect(rosterTab.latestTone('corroborated')).toBe('info')
  expect(rosterTab.latestTone('admitted')).toBe('good')
})

// ---------- item 5: the investigation button is never lost ----------

test('item 5: desktop People rows keep their button: it never shrinks, the name and meta give way', () => {
  for (const width of [40, 50, 69, 70, 84, 120]) {
    begin()
    const pane = draw('desktop', fakeIo(answer), width)
    for (let i = 0; i < 4; i++) {
      const button = pane.find({ key: 'open-' + i })!
      expect(button).toBeDefined()
      const holder = pane.find({ key: 'ob-p' + i })!
      expect(holder.props.flexShrink).toBe(0)
      const id = 'p' + i
      const identity = pane.find({ key: 'nv-' + id })!
      expect(identity.props.minWidth).toBe(0)
      expect(identity.props.flexShrink).toBe(1)
      expect(pane.find({ key: 'nm-' + id })!.props.wrap).toBe('truncate-end')
      const via = pane.find({ key: 'via-' + id })
      if (via) {
        expect(via.props.wrap).toBe('truncate-end')
        // The meta is measured to the row: what the identity box has left beside the button, ellipsis only there.
        const text = String(via.children[0])
        const room = width - 4 - 4 - 1 - (width < 70 ? 0 : textCells(String(button.props.label)) + BUTTON_CHROME + 1)
        expect(textCells(text)).toBeLessThanOrEqual(Math.max(8, room) + 0.0001)
      }
      const head = pane.find({ key: 'ph-' + id })!
      const inHead = head.children.some((c: any) => c && c.key === 'ob-' + id)
      // Below 70 cells the button is its own row under the meta, left aligned; from 70 it sits in the head row.
      expect(inHead).toBe(width >= 70)
      if (width < 70) expect(holder.props.alignSelf).toBe('flex-start')
    }
  }
})

test('item 5: the quote under a card is measured to the card, cut with an ellipsis only at its edge', () => {
  begin()
  const pane = draw('desktop', fakeIo(answer), 50)
  const label = String(pane.find({ key: 'quote-1' })!.props.label)
  expect(label.endsWith('…”')).toBe(true)
  expect(textCells(label) + BUTTON_CHROME).toBeLessThanOrEqual(50 - 4 + 0.5)
})

// ---------- item 15: the person view ----------

test('item 15: the desktop person view names the person once; the terminal keeps its heading', async () => {
  const d = await personView('desktop')
  expect(d.pane.find({ type: 'Text', text: "alice's investigation" })).toBeUndefined()
  expect(d.pane.find({ key: 'pv-h' })?.children).toEqual(['Investigation'])
  expect(d.pane.find({ key: 'pv-nm' })?.children).toEqual(['alice'])
  const t = await personView('terminal')
  expect(t.pane.find({ key: 'pv-h' })?.children).toEqual(["alice's investigation"])
})

test('item 15: a desktop finding is one row: time, glyph slot, state label, then the clipped quote', async () => {
  for (const width of [50, 84]) {
    const { pane } = await personView('desktop', width)
    for (const i of [0, 1]) {
      const row = pane.find({ key: 'pv-t' + i })!
      expect(row.props.flexWrap).toBeUndefined()
      const keys = row.children.map((c: any) => c.key)
      expect(keys[0]).toBe('pv-ti' + i)
      expect(keys[1]).toBe('pv-gs' + i)
      expect(row.children[1].props.width).toBe(2)
      // The label comes before the text.
      if (i === 0) expect(keys.slice(2)).toEqual(['pv-s0', 'pv-qb0'])
      else expect(keys.slice(2)).toEqual(['pv-qb1'])
      const label = String(pane.find({ key: 'trail-' + i })!.props.label)
      const taken = textCells('16:01') + 2 + (i === 0 ? textCells('● contested') + 2 + 1 : 0) + 3
      expect(textCells(label) + BUTTON_CHROME + taken).toBeLessThanOrEqual(width + 2 - 1 + 0.0001)
    }
    expect(String(pane.find({ key: 'trail-1' })!.props.label).endsWith('…”')).toBe(true)
    // A roomy pane shows the whole 34-character finding beside its label; a tight one cuts it, with an ellipsis, at the measured edge.
    expect(String(pane.find({ key: 'trail-0' })!.props.label)).toBe(width === 84 ? '“Origin pool exhausted in us-east-1”' : '“Origin pool exhausted in us-ea…”')
  }
})

test('item 15 and 11: the desktop keys are three capitalised Buttons that fit one row', async () => {
  const { io } = await personView('desktop')
  const labels = new Drawn(rosterTab.keys(kitFor('desktop'), io as never)).all({ type: 'Button' }).map((b) => b.props.label)
  expect(labels).toEqual(['Back to people', 'Ask about this', 'Next widget'].slice(0, labels.length))
  // The list's keys read `Claim a line…` and `Release`, not lower case (the terminal keeps its `k: label` words).
  begin()
  const list = new Drawn(rosterTab.keys(kitFor('desktop'), fakeIo(answer) as never))
  expect(list.find({ key: 'claim' })?.props.label).toBe('Claim a line…')
  expect(list.find({ key: 'release' })?.props.label).toBe('Release')
  const term = new Drawn(rosterTab.keys(kitFor('terminal'), fakeIo(answer) as never))
  expect(term.find({ key: 'claim' })?.props.label).toBe('claim a line…')
})

test('item 15: every desktop artifact row is two lines (name, then dim meta) with the button centred on both', async () => {
  const { pane } = await personView('desktop')
  for (const id of ['a1', 'a2']) {
    const row = pane.find({ key: 'art-' + id })!
    expect(row.props.alignItems).toBe('center')
    const head = row.children[0]
    expect(head.props.flexDirection).toBe('column')
    expect(head.children).toHaveLength(2)
    const name = pane.find({ key: 'art-' + id + '-n' })!
    const meta = pane.find({ key: 'art-' + id + '-m' })!
    expect(name.props.bold).toBe(true)
    expect(meta.props.dimColor).toBe(true)
    expect(String(meta.children[0])).toMatch(/·/)
    expect(row.children[1].props.flexShrink).toBe(0)
    expect(row.children[1].children[0].props.label).toBe('Add to my context')
  }
})

// ---------- item 12: Context rows ----------

const BRIEF = {
  ok: true, text: 'x', asOfSeq: 214, freshnessMs: 4000, counts: { established: 1, open: 2, participants: 1, scope: 1, focus: true },
  established: [{ seq: 205, statement: 'Rollback of web-edge v2.3.1 is ready', by: 'carol', admission: 'admitted: corroborated by bob (member)' }],
  open: [
    { seq: 212, statement: 'The 5xx rise matches the v2.3.1 deploy at 15:45Z', by: "bob's agent", state: 'staged' },
    { seq: 198, statement: 'Origin pool exhausted in us-east-1', by: 'alice', state: 'contested' },
  ],
  participants: [{ humanActorId: 'u1', displayName: 'carol', edgeAgentLabel: '', active: true }],
  scope: [{ kind: 'component', label: 'web-edge', by: 'carol' }],
  focus: { text: 'the origin pool in us-east-1', by: 'carol' },
  instructionsVersion: 3,
  listening: false,
}
const ARTS = { ok: true, artifacts: [{ artifactId: 'c1', filename: 'runbook.md', contentType: 'text/markdown', size: 4096, sharedAt: new Date(NOW - 12 * 60000).toISOString(), sharer: { displayName: 'bob', kind: 'agent', edgeAgentLabel: 'bob-claude-code', humanActorId: 'h-bob' } }] }

let n = 0
async function contextTab(surface: 'terminal' | 'desktop', width = 84) {
  n += 1
  const r = { ...ROOM, roomKey: 'r3-ctx-' + n }
  room.snapshot = { line: '', rooms: [r] } as any
  consoleState.open = true
  consoleState.warm.add('context')
  consoleState.tab = 'context'
  const io = fakeIo((argv) => (argv[0] === 'brief' ? BRIEF : argv[0] === 'artifacts' ? ARTS : { ok: false, error: 'unexpected' }))
  const k = kitFor(surface, width)
  cx.tab(k, io as never, NOW, null)
  await settle()
  return new Drawn(cx.tab(k, io as never, NOW, null))
}

test('item 12: an open item is its statement, then a dim "who · #seq" line with the label right after the number', async () => {
  for (const surface of ['terminal', 'desktop'] as const) {
    const pane = await contextTab(surface, surface === 'desktop' ? 60 : 84)
    const item = pane.find({ key: 'cx-o0' })!
    expect(item.props.flexDirection).toBe('column')
    expect(item.children[0].type).toBe('Text')
    expect(item.children[0].children).toEqual(['The 5xx rise matches the v2.3.1 deploy at 15:45Z'])
    const second = pane.find({ key: 'cx-o0-m2' })!
    expect(second.props.flexDirection).toBe('row')
    const [meta, label] = second.children
    expect(meta.children).toEqual(["bob's agent · #212"])
    expect(meta.props.dimColor).toBe(true)
    // The label follows the number directly, in a gap of one, never pushed to the row's edge.
    expect(second.props.columnGap).toBe(1)
    expect(second.props.justifyContent).toBeUndefined()
    expect(textOf(label)).toBe('● staged')
    // An established item has no state, only its dim line and, below, how it got in.
    const est = pane.find({ key: 'cx-e0' })!
    expect(textOf(pane.find({ key: 'cx-e0-b' })!)).toBe('carol · #205')
    expect(est.children).toHaveLength(3)
    // Context's tones are the People tab's.
    expect(pane.find({ type: 'Text', text: '● contested' })?.props.color ?? pane.find({ key: 'cx-o1-st-t' })?.props.color).toBe('#ec835a')
    expect(pane.find({ key: 'cx-o0-st-t' })?.props.color ?? pane.find({ key: 'cx-o0-st' })?.props.color).toBe('#898781')
  }
})

// ---------- item 16: one blank row between blocks on the terminal, never two ----------

const isBlank = (n: any) => n && n.type === 'Text' && n.children.join('') === ' '
function expectRhythm(rows: any[]) {
  const list = rows.filter(Boolean)
  list.forEach((r, i) => {
    if (isBlank(r)) {
      expect(i).toBeGreaterThan(0)
      expect(i).toBeLessThan(list.length - 1)
      expect(isBlank(list[i - 1])).toBe(false)
    }
  })
  return list.filter(isBlank).length
}

test('item 16: the person view and the list put one blank row between blocks on the terminal, none on the desktop', async () => {
  const t = await personView('terminal')
  const blanks = expectRhythm(t.pane.rows)
  // Identity, Findings, Dashboard, Artifacts and the hint are five blocks: four gaps (and one before the hint).
  expect(blanks).toBeGreaterThanOrEqual(4)
  const between = (key: string) => {
    const i = t.pane.rows.findIndex((r: any) => r && r.key === key)
    return isBlank(t.pane.rows[i - 1])
  }
  expect(between('pv-f-h')).toBe(true)
  expect(between('pv-a-h')).toBe(true)
  expect(between('pv-hint')).toBe(true)
  const d = await personView('desktop')
  expect(d.pane.rows.filter(isBlank)).toHaveLength(0)
  begin()
  expectRhythm(draw('terminal', fakeIo(answer)).rows)
  expect(draw('desktop', fakeIo(answer)).rows.filter(isBlank)).toHaveLength(0)
})

test('item 16: Context on the terminal separates its blocks with one blank row; the desktop adds none', async () => {
  const t = await contextTab('terminal')
  const shared = t.find({ key: 'cx-shared' })!
  expectRhythm(shared.children)
  expect(shared.children.filter(isBlank).length).toBeGreaterThanOrEqual(3)
  const d = await contextTab('desktop')
  expect(d.all({ type: 'Text', text: /^ $/ })).toHaveLength(0)
})

test('a clip never overruns what it is given (the measure the rows above lean on)', () => {
  expect(textCells(clipToCells('x'.repeat(100), 20))).toBeLessThanOrEqual(20)
})
