import { expect, test } from 'claude-code/testing'
import { Drawn, fakeIo, kitFor, openConsoleOn, setRooms, settle } from './_tab'
import { consoleState } from '../hooks/core.js'
import * as rosterTab from '../hooks/components/roster.js'
import * as wallTab from '../hooks/components/wall.js'
import { labelIn, labelsIn } from './_label'

// One `landfall watch` room with people[].latest (cli-json.md §1, review #7). The people rows carry
// humanActorId, except bob's: the wall's people[] names him by displayName.
const ROOM = {
  roomKey: 'k1',
  incidentId: 'i168',
  displayId: '168',
  title: 'cloudfront-5xx-high',
  slug: 'acme',
  connection: 'live',
  count: 0,
  addressed: 0,
  votesAwaited: 0,
  maxSeq: 233,
  digest: [],
  status: {
    status: 'investigating',
    severity: 'SEV2',
    lines: [
      { claimId: 'c1', label: 'eu-west-1 5xx', lineKey: 'eu-west-1-5xx', owner: 'alex', you: true, ageMs: 240000 },
      { claimId: 'c2', label: 'checkout db pool', lineKey: 'checkout-db-pool', owner: 'Beacon', you: false, ageMs: 840000 },
    ],
    people: [
      { name: 'alex', you: true, here: true, humanActorId: 'h-alex', agents: [{ tool: 'Claude Code', label: 'alex-claude-code', here: true }] },
      { name: 'carol', here: true, browser: true, humanActorId: 'h-carol', latest: { seq: 205, text: 'Rollback of web-edge v2.3.1 is ready; holding until eu-west-1 is read.', state: 'admitted', ageMs: 180000 } },
      { name: 'bob', here: true, agents: [{ tool: 'Claude Code', label: 'bob-claude-code', here: true, doing: 'comparing 5xx by region' }], latest: { seq: 212, text: 'The 5xx rise starts at 15:45Z, the same bucket as the deploy.', state: 'staged', ageMs: 120000 } },
      { name: 'alice', here: true, humanActorId: 'h-alice', agents: [{ tool: 'Codex', label: 'alice-codex', here: true }], latest: { seq: 198, text: 'Origin pool exhausted in us-east-1.', state: 'contested', ageMs: 540000 } },
      { name: 'dave', here: false, humanActorId: 'h-dave', agents: [{ tool: 'Codex', label: 'dave-codex', here: false }] },
    ],
  },
}

const NOW = 1791472800000 // 2026-10-08T15:20:00Z
const WALL_PEOPLE = [
  { humanActorId: 'h-alice', displayName: 'alice', edgeAgentLabel: 'Codex', kind: 'human', widgets: 2, trail: 4, artifacts: 2, latestSeq: 220 },
  { humanActorId: 'h-bob', displayName: 'bob', edgeAgentLabel: 'Claude Code', kind: 'human', widgets: 0, trail: 0, artifacts: 0, latestSeq: 210 },
  { humanActorId: 'h-carol', displayName: 'carol', edgeAgentLabel: '', kind: 'human', widgets: 0, trail: 0, artifacts: 0, latestSeq: 200 },
  { humanActorId: 'h-alex', displayName: 'alex', edgeAgentLabel: 'Claude Code', kind: 'human', widgets: 0, trail: 0, artifacts: 0, latestSeq: 100 },
]
const SHARED = { ok: true, sharedBy: 'carol', widgets: [], unavailable: [], people: WALL_PEOPLE }

const TRAIL = [
  { seq: 220, at: '2026-10-08T16:01:00Z', type: 'edge.finding', kind: 'finding', text: 'Origin pool exhausted in us-east-1', state: 'contested' },
  { seq: 218, at: '2026-10-08T15:58:00Z', type: 'edge.hypothesis', kind: 'hypothesis', text: 'The pool limit was lowered in the 15:40 deploy' },
  { seq: 216, at: '2026-10-08T15:55:00Z', type: 'edge.query', kind: 'query', text: 'cloudwatch/metrics · origin-pool' },
  { seq: 214, at: '2026-10-08T15:54:00Z', type: 'edge.action.proposed', kind: 'suggestion', text: 'raise the pool limit to 64' },
]
const ARTIFACTS = [
  { artifactId: 'a1', filename: 'origin-pool-notes.md', contentType: 'text/markdown', size: 4096, sharedAt: '2026-10-08T15:08:00Z', sharer: { displayName: 'alice', kind: 'agent', edgeAgentLabel: 'Codex', humanActorId: 'h-alice' } },
  { artifactId: 'a2', filename: 'screenshot.png', contentType: 'image/png', size: 1258291, sharedAt: '2026-10-08T15:00:00Z', sharer: { displayName: 'alice', kind: 'human', humanActorId: 'h-alice' } },
]
const ALICE = {
  ok: true,
  person: { humanActorId: 'h-alice', displayName: 'alice', you: false },
  widgets: [
    { id: 'edge-widget-201', type: 'stat', title: 'Origin pool saturation', value: '94', unit: '%', tone: 'critical', spark: [40, 55, 70, 90, 94], capturedAt: '2026-10-08T15:11:00Z', seq: 201 },
    { id: 'edge-widget-205', type: 'geo', title: 'Pool connections by zone', points: [{ place: 'us-east-1a', value: 98, unit: '%', tone: 'critical' }], capturedAt: '2026-10-08T15:11:00Z', seq: 205 },
  ],
  unavailable: [],
  trail: TRAIL,
  artifacts: ARTIFACTS,
  people: WALL_PEOPLE,
}
const NOTHING = { ok: true, person: {}, widgets: [], unavailable: [], trail: [], artifacts: [], people: WALL_PEOPLE }

const SURFACES = ['terminal', 'desktop'] as const

function begin(room: unknown = ROOM) {
  rosterTab.reset()
  wallTab.reset()
  setRooms(room ? [room] : [])
  openConsoleOn('people')
}

function answers(by: Record<string, unknown> = {}) {
  return (argv: string[]) => {
    if (argv[0] === 'lines') return by['lines ' + argv[1]] ?? { ok: false, error: 'unexpected lines ' + argv[1] }
    const at = argv.indexOf('--person')
    if (at < 0) return by.shared ?? SHARED
    return by[argv[at + 1]] ?? (argv[at + 1] === 'h-alice' ? ALICE : NOTHING)
  }
}

function draw(surface: 'terminal' | 'desktop' | 'mobile', io: ReturnType<typeof fakeIo>, args: string | null = null, width = 84) {
  return new Drawn(rosterTab.tab(kitFor(surface, width), io as never, NOW, args))
}

const keysOf = (surface: 'terminal' | 'desktop', io: ReturnType<typeof fakeIo>) => new Drawn(rosterTab.keys(kitFor(surface), io as never))

async function withShared(io: ReturnType<typeof fakeIo>) {
  wallTab.warm(io as never)
  await settle()
}

for (const surface of SURFACES) {
  test(`People lists each person, where they work and their latest contribution with its state (${surface})`, async () => {
    begin()
    const io = fakeIo(answers())
    const pane = draw(surface, io)
    expect(pane.find({ type: 'Text', text: 'Here · 4' })?.props.bold).toBe(true)
    expect(pane.find({ text: 'carol' })).toBeDefined()
    expect(pane.find({ type: 'Text', text: 'war room' })).toBeDefined()
    expect(pane.find({ type: 'Text', text: 'Claude Code · comparing 5xx by region' })).toBeDefined()
    expect(labelIn(pane.rows, 'admitted')).toBeDefined()
    expect(labelIn(pane.rows, 'staged')).toBeDefined()
    expect(labelIn(pane.rows, 'contested')).toBeDefined()
    expect(pane.find({ type: 'Text', text: '#205 · 3m' })).toBeDefined()
    expect(pane.find({ type: 'Text', text: 'nothing shared yet' })).toBeDefined()
    // The pill carries the state's tone from the kit's one map: contested is serious, never critical red.
    expect(labelIn(pane.rows, 'contested')?.tone).toBe('#ec835a')
    expect(labelIn(pane.rows, 'staged')?.tone).toBe('#898781')
    expect(labelIn(pane.rows, 'admitted')?.tone).toBe('#0ca30c')
    // Quotes in curly marks, the hint once.
    expect(pane.find({ type: 'Button', text: '“Origin pool exhausted in us-east-1.”' })).toBeDefined()
    expect(pane.all({ type: 'Text', text: 'Enter on a name opens' })).toHaveLength(1)
    // Your line is on your row, an agent's under Lines.
    expect(pane.find({ type: 'Text', text: 'Claude Code · on eu-west-1 5xx' })).toBeDefined()
    expect(pane.find({ type: 'Text', text: /^Lines$/ })?.props.bold).toBe(true)
    expect(pane.find({ type: 'Text', text: 'checkout db pool · Beacon · 14m' })).toBeDefined()
    if (surface === 'desktop') expect(pane.all({ type: 'Svg' })).not.toHaveLength(0)
    expect(rosterTab.badge()).toBe(4)
  })

  test(`People: a press quotes a contribution into the prompt as a draft, and nothing is sent (${surface})`, async () => {
    begin()
    const io = fakeIo(answers())
    const pane = draw(surface, io)
    // Here first by name: alice, bob, carol, then alex (you), then dave who is away.
    await pane.press('quote-0')
    expect(io.filled).toEqual(['> Origin pool exhausted in us-east-1.\n\n'])
    expect(io.appended).toEqual([])
  })

  test(`People: the investigation button sits on every person's row (${surface})`, async () => {
    begin()
    const io = fakeIo(answers())
    const pane = draw(surface, io)
    const opens = pane.all({ type: 'Button' }).filter((b) => String(b.key).startsWith('open-'))
    expect(opens).toHaveLength(5)
    if (surface === 'desktop') {
      expect(opens.map((b) => b.props.label)).toEqual(["alice's investigation", "bob's investigation", "carol's investigation", 'Your investigation', "dave's investigation"])
      // One column: each button is the last child of its row, in a box that never shrinks, after the
      // name block that takes the rest and may shrink to nothing (round 2 review, issue 5).
      for (const row of pane.all({ type: 'Box' }).filter((b) => String(b.key).startsWith('ph-'))) {
        expect(row.children.at(-1).props.flexShrink).toBe(0)
        expect(row.children.at(-1).children[0].type).toBe('Button')
        expect(row.children.at(-2).props.flexGrow).toBe(1)
        expect(row.children.at(-2).props.minWidth).toBe(0)
        expect(row.children.at(-2).children.at(-1).props.wrap).toBe('truncate-end')
      }
    }
  })
}

test('People: the keys are c: claim a line (primary) and e: release while you hold one', async () => {
  begin()
  const io = fakeIo(answers({ 'lines claim': { ok: true, claimId: 'c3', label: 'us-east-1c targets' }, 'lines release': { ok: true } }))
  draw('terminal', io)
  const t = keysOf('terminal', io)
  expect(t.find({ key: 'claim' })?.props.hotkey).toBe('c')
  expect(t.find({ key: 'release' })?.props.hotkey).toBe('e')
  const d = keysOf('desktop', io)
  expect(d.find({ key: 'claim' })?.props.variant).toBe('primary')
  expect(d.find({ key: 'release' })?.props.variant).toBeUndefined()

  await t.press('claim')
  const field = draw('terminal', io)
  await field.input('claim-label', 'us-east-1c targets')
  expect(io.runs[0]).toEqual(['lines', 'claim', '--room', 'k1', '--label', 'us-east-1c targets', '--host', 'claude-code'])
  await t.press('release')
  expect(io.runs[1]).toEqual(['lines', 'release', '--room', 'k1', '--claim', 'c1', '--host', 'claude-code'])
  expect(io.toasts).toEqual(['You claimed the line us-east-1c targets. The room sees it.', 'You released the line eu-west-1 5xx.'])
  expect(io.toasts.every((t) => t.length <= 80)).toBe(true)
  // Claimed: the field is gone and the key is back.
  expect(draw('terminal', io).find({ key: 'claim-label' })).toBeUndefined()
})

test('People: no line held, no release key; mobile draws no field', async () => {
  begin({ ...ROOM, status: { ...ROOM.status, lines: [] } })
  const io = fakeIo(answers())
  expect(keysOf('terminal', io).find({ key: 'release' })).toBeUndefined()
  expect(draw('terminal', io).find({ type: 'Text', text: 'Lines' })).toBeUndefined()
  begin()
  const mobile = draw('mobile', io)
  expect(mobile.find({ type: 'Text', text: 'To claim a line here, type /landfall lines and what you are on.' })).toBeDefined()
  expect(new Drawn(rosterTab.keys(kitFor('mobile'), io as never)).find({ key: 'claim' })).toBeUndefined()
})

test('People: nobody, and no room', async () => {
  begin({ ...ROOM, status: { ...ROOM.status, people: [], lines: [] } })
  const io = fakeIo(answers())
  expect(draw('terminal', io).find({ type: 'Text', text: 'Nobody is in the room yet.' })).toBeDefined()
  expect(rosterTab.badge()).toBeNull()
  begin(null)
  expect(draw('terminal', io).find({ type: 'Text', text: 'This folder is not in a war room.' })).toBeDefined()
})

test('People as text, and a person as text', async () => {
  begin()
  const io = fakeIo(answers())
  expect(await rosterTab.text(io as never, '')).toBe(
    [
      '168 · cloudfront-5xx-high · 4 here',
      '  ● alice · Codex',
      '    contested · #198 · 9m: "Origin pool exhausted in us-east-1."',
      '  ● bob · Claude Code · comparing 5xx by region',
      '    staged · #212 · 2m: "The 5xx rise starts at 15:45Z, the same bucket as the deploy."',
      '  ● carol · war room',
      '    admitted · #205 · 3m: "Rollback of web-edge v2.3.1 is ready; holding until eu-west-1 is read."',
      '  ● alex (you) · Claude Code',
      '    Nothing shared to the context yet',
      '  ○ dave · away',
      '    Nothing shared to the context yet',
    ].join('\n'),
  )
  await withShared(io)
  const text = await rosterTab.text(io as never, 'ali')
  expect(text).toContain("alice's investigation\nFindings · 4\n  16:01 ◇ Origin pool exhausted in us-east-1  contested")
  expect(text).toContain('  15:55 ✓ queried cloudwatch/metrics · origin-pool')
  expect(text).toContain('  15:54 ◇ suggested: raise the pool limit to 64')
  expect(text).toContain('Dashboard · 2 widgets · snapshots\n  Origin pool saturation\n  Pool connections by zone\nArtifacts · 2\n  origin-pool-notes.md')
  begin(null)
  expect(await rosterTab.text(io as never, '')).toBe('This folder is not in a war room. Open a share link from the room, or join one from Incidents.')
})

// ---------------------------------------------------------------------------
// A person's investigation (spec §4.3.1).

for (const surface of SURFACES) {
  test(`a press on a name opens their investigation: findings with their gate state, the dashboard, the artifacts (${surface})`, async () => {
    begin()
    const io = fakeIo(answers())
    await withShared(io)
    await draw(surface, io).press('open-0')
    await settle()
    expect(io.runs.at(-1)).toEqual(['wall', '--host', 'claude-code', '--room', 'k1', '--person', 'h-alice'])
    const pane = draw(surface, io)
    // The desktop names her once, in the identity row; the terminal's heading names her.
    expect(pane.find({ type: 'Text', text: 'Investigation' })?.props.bold).toBe(true)
    // Her own row, as the list draws it.
    expect(pane.find({ type: 'Text', text: 'Codex' })).toBeDefined()
    // Findings, newest first, with the Timeline's glyphs; a claim carries its state.
    expect(pane.find({ type: 'Text', text: 'Findings · 4' })?.props.bold).toBe(true)
    expect(pane.find({ type: 'Text', text: '16:01' })).toBeDefined()
    expect(labelIn(pane.rows, 'contested')?.tone).toBe('#ec835a')
    const finding = pane.find({ key: 'trail-0' })!
    expect(finding.props.label).toBe('“Origin pool exhausted in us-east-1”')
    expect(pane.find({ key: 'trail-2' })?.props.label).toContain('queried cloudwatch/metrics · origin-pool')
    expect(pane.find({ key: 'trail-3' })?.props.label).toContain('suggested: raise the pool limit to 64')
    // Only a claim carries a label.
    expect(labelsIn(pane.rows, /^(staged|contested|admitted|corroborated|withdrawn)$/)).toHaveLength(1)
    expect(pane.all({ type: 'Text' }).filter((t) => ['◇', '?', '✓', '·'].includes(String(t.children[0]))).map((t) => t.children[0])).toEqual(['◇', '?', '✓', '◇'])
    // The dashboard, drawn as the Wall draws a person's: snapshots, the selected title marked.
    expect(pane.find({ type: 'Text', text: 'Dashboard' })?.props.bold).toBe(true)
    expect(pane.find({ type: 'Text', text: ' · 2 widgets · snapshots' })).toBeDefined()
    expect(pane.all({ type: 'Text', text: 'snapshot 9m ago' })).toHaveLength(2)
    expect(pane.find({ type: 'Button', text: '▸ Origin pool saturation' })).toBeDefined()
    expect(labelIn(pane.rows, '98%')).toBeDefined()
    // Artifacts and the hint, once.
    expect(pane.find({ type: 'Text', text: 'Artifacts · 2' })?.props.bold).toBe(true)
    // The row is context.js's: the name is the Button on the terminal, a bold Text beside "Add to my context" on the desktop.
    expect(pane.find(surface === 'terminal' ? { type: 'Button', text: 'origin-pool-notes.md' } : { type: 'Text', text: 'origin-pool-notes.md' })).toBeDefined()
    expect(pane.all({ type: 'Text', text: 'Enter on a finding quotes it; on an artifact, adds it to your context' })).toHaveLength(1)
    // The tab is still People; the footer is this read's.
    expect(rosterTab.footer(kitFor(surface), NOW)).not.toBeNull()
  })
}

test('a finding quotes into the prompt; an artifact is added to the context, never a prompt', async () => {
  begin()
  const io = fakeIo(answers())
  await withShared(io)
  await draw('terminal', io).press('open-0')
  await settle()
  const pane = draw('terminal', io)
  await pane.press('trail-1')
  expect(io.filled).toEqual(['> The pool limit was lowered in the 15:40 deploy\n\n'])
  // The artifact row is context.js's; pressing it adds to the context (an append), never a prompt.
  const row = pane.find({ type: 'Button', key: 'art-a1' })
  expect(row).toBeDefined()
  await row!.props.onPress()
  await settle()
  expect(io.filled).toHaveLength(1)
})

test('the keys of a person view: b back first, a asks about the selected widget, n goes to the next', async () => {
  begin()
  const io = fakeIo(answers())
  await withShared(io)
  await draw('terminal', io).press('open-0')
  await settle()
  const pane = draw('terminal', io)
  const keys = keysOf('terminal', io)
  expect(keys.all({ type: 'Button' }).map((b) => [b.props.hotkey, b.props.label])).toEqual([
    ['b', 'back to people'],
    ['a', 'ask about this'],
    ['n', 'next widget'],
  ])
  expect(new Drawn(rosterTab.keys(kitFor('desktop'), io as never)).find({ key: 'back' })?.props.label).toBe('Back to people')
  // The desktop's three keys fit one row: the widget is not named (round 2 review, issue 15).
  expect(new Drawn(rosterTab.keys(kitFor('desktop'), io as never)).all({ type: 'Button' }).map((b) => b.props.label)).toEqual(['Back to people', 'Ask about this', 'Next widget'])
  await keys.press('pv-ask')
  await keys.press('pv-next')
  await pane.press('pv-edge-widget-201-t')
  expect(io.filled).toEqual(["Tell me about the Origin pool saturation widget on alice's dashboard in 168."])
  expect(draw('terminal', io).find({ type: 'Button', text: '▸ Origin pool saturation' })).toBeDefined()
  // Back to the list: the person view's keys are gone.
  await keysOf('terminal', io).press('back')
  expect(draw('terminal', io).find({ type: 'Text', text: 'Here · 4' })).toBeDefined()
  expect(keysOf('terminal', io).find({ key: 'claim' })).toBeDefined()
})

test('the person view: reading, failed, empty, no dashboard, and a person who left', async () => {
  begin()
  let alice: unknown = ALICE
  const io = fakeIo((argv) => (argv.includes('--person') ? alice : SHARED))
  await withShared(io)
  // Reading, until the first good read.
  const pane = draw('terminal', io, 'alice')
  expect(consoleState.args).toBeNull()
  expect(pane.find({ type: 'Text', text: "Reading alice's investigation…" })?.props.dimColor).toBe(true)
  await settle()
  expect(draw('terminal', io).find({ type: 'Text', text: 'Findings · 4' })).toBeDefined()

  // Failed with nothing shown: the CLI's sentence.
  begin()
  alice = { ok: false, error: 'Your sign-in expired. Run landfall login.' }
  const failing = fakeIo((argv) => (argv.includes('--person') ? alice : SHARED))
  await withShared(failing)
  draw('terminal', failing, 'alice')
  await settle()
  expect(draw('terminal', failing).find({ type: 'Text', text: 'Your sign-in expired. Run landfall login.' })).toBeDefined()

  // Nothing at all: the heading and the row, then one dim sentence, no empty block headings.
  begin()
  alice = NOTHING
  const empty = fakeIo((argv) => (argv.includes('--person') ? alice : SHARED))
  await withShared(empty)
  draw('terminal', empty, 'alice')
  await settle()
  const none = draw('terminal', empty)
  expect(none.find({ type: 'Text', text: 'alice has not shared anything yet.' })?.props.dimColor).toBe(true)
  expect(none.find({ type: 'Text', text: /^Findings/ })).toBeUndefined()
  expect(none.find({ type: 'Text', text: /^Dashboard/ })).toBeUndefined()

  // Findings but no dashboard: the block says so.
  begin()
  alice = { ...ALICE, widgets: [], artifacts: [] }
  const noDash = fakeIo((argv) => (argv.includes('--person') ? alice : SHARED))
  await withShared(noDash)
  draw('terminal', noDash, 'alice')
  await settle()
  const nd = draw('terminal', noDash)
  expect(nd.find({ type: 'Text', text: 'Dashboard' })).toBeDefined()
  expect(nd.find({ type: 'Text', text: 'alice has not shared a dashboard yet.' })).toBeDefined()
  expect(nd.find({ type: 'Text', text: /^Artifacts/ })).toBeUndefined()

  // A person who built something and left the room: dim, the view still readable.
  begin({ ...ROOM, status: { ...ROOM.status, people: ROOM.status.people.filter((p) => p.name !== 'alice') } })
  alice = ALICE
  const left = fakeIo((argv) => (argv.includes('--person') ? alice : SHARED))
  await withShared(left)
  await draw('terminal', left).find({ key: 'open-0' })
  rosterTab.reset()
  // (alice is in the wall's people[] only: opened by id, as the Wall's selector would)
  const wallOnly = new Drawn(wallTab.tab(kitFor('terminal'), left as never, NOW, null))
  expect(wallOnly.find({ key: 'sel-h-alice' })).toBeDefined()
})

test('/landfall people <name> opens that person; yourself reads `me` and says Your investigation', async () => {
  begin()
  const io = fakeIo(answers({ me: { ...NOTHING, person: { you: true } } }))
  await withShared(io)
  draw('terminal', io, 'ALEX')
  await settle()
  expect(io.runs.at(-1)).toEqual(['wall', '--host', 'claude-code', '--room', 'k1', '--person', 'me'])
  const pane = draw('terminal', io)
  expect(pane.find({ type: 'Text', text: 'Investigation' })?.props.bold).toBe(true)
  expect(pane.find({ type: 'Text', text: 'You have not shared anything yet.' })).toBeDefined()
  // A name nobody has leaves the list.
  begin()
  const other = fakeIo(answers())
  expect(draw('terminal', other, 'zed').find({ type: 'Text', text: 'Here · 4' })).toBeDefined()
})

test('a person with no humanActorId yet is read once the wall says who they are, by name', async () => {
  begin()
  const io = fakeIo(answers())
  // Before the wall has been read, bob has no id: nothing is read and nothing is made up.
  draw('terminal', io, 'bob')
  expect(io.runs).toEqual([])
  await withShared(io)
  draw('terminal', io)
  await settle()
  expect(io.runs.at(-1)).toEqual(['wall', '--host', 'claude-code', '--room', 'k1', '--person', 'h-bob'])
})

test('the open person view reads every 15 s and on any new room event, and stops with the console', async () => {
  begin()
  const io = fakeIo(answers())
  await withShared(io)
  await draw('terminal', io).press('open-0')
  await settle()
  const reads = () => io.runs.filter((r) => r.includes('--person')).length
  expect(reads()).toBe(1)
  setRooms([{ ...ROOM, maxSeq: 240 }])
  rosterTab.onSnapshot(io as never, { line: '', rooms: [{ ...ROOM, maxSeq: 240 }] } as never, null as never)
  await settle()
  expect(reads()).toBe(2)
  io.clock.t += 16000
  await rosterTab.tick(io as never, io.clock.t)
  await settle()
  expect(reads()).toBe(3)
  // Not on another tab, and not once the console is closed.
  consoleState.tab = 'home'
  io.clock.t += 16000
  await rosterTab.tick(io as never, io.clock.t)
  await settle()
  expect(reads()).toBe(3)
  consoleState.tab = 'people'
  consoleState.open = false
  io.clock.t += 16000
  await rosterTab.tick(io as never, io.clock.t)
  await settle()
  expect(reads()).toBe(3)
})
