import { expect, test } from 'claude-code/testing'
import { NOT_PLACED, UNKNOWN, clampOffset, estRows, flatten, moreWords, offsetShowing, parseArgs, planHome, readingWords, segmentLabels } from '../hooks/console.js'
import { TABS, TOAST_MAX, clipMiddle, toastText } from '../hooks/core.js'
import { elapsed, reasonWords, webHost } from '../hooks/components/signin.js'
import { notJoinedWords } from '../hooks/components/incidents.js'
import { BAND, consolePane, roomOf, texts, world } from './_console'
import { labelText } from './_label'

// The console shell (spec §1, §2, §3, §8): the one /landfall command, the pinned switcher, Home's
// drop rule and the entry flow, driven through the plugin where the engine shows them and through
// the pure helpers where a layout rule is a number.

// ---------- §1.2 arguments ----------

test('/landfall arguments: every tab word and its one short form, the three that act, unknown words', () => {
  expect(parseArgs('')).toEqual({})
  expect(parseArgs('home')).toEqual({ tab: 'home', args: '' })
  expect(parseArgs('overview')).toEqual({ tab: 'home', args: '' })
  expect(parseArgs('Vote')).toEqual({ tab: 'vote', args: '' })
  expect(parseArgs('context')).toEqual({ tab: 'context', args: '' })
  expect(parseArgs('context artifacts')).toEqual({ tab: 'context', args: '', scrollTo: 'artifacts' })
  expect(parseArgs('wall')).toEqual({ tab: 'wall', args: '' })
  expect(parseArgs('wall mine')).toEqual({ tab: 'wall', args: 'mine' })
  expect(parseArgs('wall alice')).toEqual({ tab: 'wall', args: 'alice' })
  expect(parseArgs('topology')).toEqual({ tab: 'wall', args: '', scrollTo: 'graph' })
  expect(parseArgs('people')).toEqual({ tab: 'people', args: '' })
  expect(parseArgs('who')).toEqual({ tab: 'people', args: '' })
  expect(parseArgs('lines')).toEqual({ tab: 'people', args: '' })
  expect(parseArgs('people alice')).toEqual({ tab: 'people', args: 'alice' })
  expect(parseArgs('timeline findings')).toEqual({ tab: 'timeline', args: 'findings' })
  expect(parseArgs('timeline nonsense')).toEqual({ tab: 'timeline', args: '' })
  expect(parseArgs('lb web-edge-alb')).toEqual({ tab: 'lb', args: 'web-edge-alb' })
  expect(parseArgs('incidents')).toEqual({ tab: 'incidents', args: '' })
  expect(parseArgs('more')).toEqual({ tab: 'more', args: '' })
  expect(parseArgs('comms')).toEqual({ tab: 'more', args: 'comms' })
  expect(parseArgs('brain')).toEqual({ tab: 'more', args: 'brain' })
  expect(parseArgs('brain origin pool')).toEqual({ tab: 'more', args: 'brain origin pool' })
  expect(parseArgs('lines eu-west-1 5xx')).toEqual({ act: 'lines', label: 'eu-west-1 5xx' })
  expect(parseArgs('chart')).toEqual({ act: 'chart' })
  expect(parseArgs('sound')).toEqual({ act: 'sound' })
  expect(parseArgs('who-knows')).toEqual({ unknown: true })
  expect(UNKNOWN).toBe('Landfall tabs: home, vote, context, wall, people, timeline, lb, incidents, more (comms, brain). Also: lines <label>, chart, sound.')
})

test('one command: /landfall is registered and the twelve it replaced are not', async ($, on) => {
  const w = await world($, on, { rooms: [roomOf()] })
  expect(w.registered).toEqual(['landfall'])
})

test('an unknown word answers the tab list in one line; nothing opens', async ($, on) => {
  const w = await world($, on)
  expect((await $.command.run({ command: 'landfall', args: 'sideways' })).text).toBe(UNKNOWN)
  expect(w.opened).toEqual([])
})

test('/landfall lines <label> claims as the person and answers in text', async ($, on) => {
  const w = await world($, on, { rooms: [roomOf()], answers: { 'lines claim': { ok: true, label: 'eu-west-1 5xx' } } })
  const answer = await $.command.run({ command: 'landfall', args: 'lines eu-west-1 5xx' })
  expect(answer.text).toBe('You claimed the line eu-west-1 5xx. The room sees it.')
  expect(w.runs.find((r) => r[1] === 'lines')).toEqual(['landfall', 'lines', 'claim', '--room', 'k168', '--label', 'eu-west-1 5xx', '--host', 'claude-code'])
  expect(w.opened).toEqual([])
  expect(w.counts.mcp).toBe(0)
})

test('/landfall sound answers the chime text; /landfall chart says when there is nothing to chart', async ($, on) => {
  await world($, on)
  expect((await $.command.run({ command: 'landfall', args: 'sound' })).text).toMatch(/^Sound cues are off\./)
  expect((await $.command.run({ command: 'landfall', args: 'chart' })).text).toBe('No metric read to chart yet. Ask your agent to read a metric from the room, then run /landfall chart.')
})

test('where no pane can be placed, /landfall <tab> toasts once and answers the tab in text', async ($, on) => {
  const w = await world($, on, { placed: false, answers: { incidents: { ok: true, org: 'acme', incidents: [] } } })
  const answer = await $.command.run({ command: 'landfall', args: 'incidents' })
  expect(w.toasts).toEqual([NOT_PLACED])
  expect(answer.text).toBe('No open incidents in acme.')
})

// ---------- §2.1, §2.2 the pane and the switcher ----------

for (const surface of ['terminal', 'desktop'] as const) {
  test(`${surface}: /landfall in a room opens the one pane on Home, asked, at 84 columns`, async ($, on) => {
    const w = await world($, on, { rooms: [roomOf()], surface })
    const answer = await $.command.run({ command: 'landfall', args: '' })
    expect(answer.text).toBeUndefined() // a successful open leaves no row in the transcript
    expect(w.opened).toEqual([{ id: 'landfall', focus: true, columns: 84, title: 'Landfall · Home' }])
    const pane = await $.ui.mount(consolePane(surface) as never)
    const drawn: any = await pane.drawn()
    // The switcher is the pane's first row, outside the body that scrolls under it.
    expect(drawn.children[0].props.key).toBe('seg')
    expect(drawn.children[1].props.key).toBe('body')
    // The active segment is not a Button: inverse Text on the terminal, a filled Box elsewhere.
    expect(await pane.find({ type: 'Button', key: 'seg-home' })).toBeUndefined()
    if (surface === 'terminal') expect((await pane.find({ type: 'Text', text: ' ▸ Home ' }))?.props.inverse).toBe(true)
    else expect((await pane.find({ type: 'Text', text: '▸ Home' }))?.props.bold).toBe(true)
    // The others are Buttons with no hotkey: no digit and no key chip is drawn.
    const segs = (await pane.findAll({ type: 'Button' })).filter((b) => String(b.key).startsWith('seg-'))
    expect(segs.map((b) => b.key)).toEqual(TABS.filter((t) => t !== 'home').map((t) => 'seg-' + t))
    expect(segs.every((b) => b.props.hotkey === undefined)).toBe(true)
    // The header names the room; the keys row ends with refresh and close.
    const words = await texts(pane)
    expect(words.some((t) => /Landfall 168 · cloudfront-5xx-high/.test(t))).toBe(true)
    expect(await pane.find({ type: 'Button', key: 'refresh' })).toBeDefined()
    expect((await pane.find({ type: 'Button', key: 'close' }))?.props.label).toBe(surface === 'terminal' ? 'close (esc)' : 'Close')
    // No MCP call, no turn.
    expect(w.counts.mcp).toBe(0)
    expect(w.filled).toEqual([])
  })

  test(`${surface}: a press on a segment switches the body; the switcher stays`, async ($, on) => {
    await world($, on, { rooms: [roomOf()], surface })
    await $.command.run({ command: 'landfall', args: '' })
    const pane = await $.ui.mount(consolePane(surface) as never)
    await pane.press({ key: 'seg-people' })
    expect(await pane.find({ type: 'Button', key: 'seg-people' })).toBeUndefined()
    expect(await pane.find({ type: 'Button', key: 'seg-home' })).toBeDefined()
    const label = surface === 'terminal' ? / ▸ People/ : /▸ People/
    expect(await pane.find({ type: 'Text', text: label })).toBeDefined()
  })
}

test('the switcher label set comes from the width alone: LB and no Incidents count when docked', () => {
  const counts = { vote: 1, context: 2, people: 4, timeline: 3, incidents: 4 }
  const docked = segmentLabels(TABS, 'home', counts, 84)
  expect(docked.map((s) => s.label)).toEqual(['Home', 'Vote 1', 'Context 2', 'Wall', 'People 4', 'Timeline 3', 'LB', 'Incidents', 'More'])
  const full = segmentLabels(TABS, 'home', counts, 110)
  expect(full.map((s) => s.label)).toEqual(['Home', 'Vote 1', 'Context 2', 'Wall', 'People 4', 'Timeline 3', 'Load balancers', 'Incidents 4', 'More'])
  // With no counts at all the docked set is still docked: the row never flips.
  expect(segmentLabels(TABS, 'incidents', {}, 84).map((s) => s.label)).toContain('LB')
  // A two-digit count overflows 84 cells: the inactive counts drop, the active one keeps its own.
  const busy = segmentLabels(TABS, 'timeline', { vote: 1, context: 2, people: 14, timeline: 23 }, 84)
  expect(busy.map((s) => s.label)).toEqual(['Home', 'Vote', 'Context', 'Wall', 'People', 'Timeline 23', 'LB', 'Incidents', 'More'])
})

test('the docked row with four one-digit counts is exactly 84 cells', () => {
  const s = segmentLabels(TABS, 'home', { vote: 1, context: 1, people: 4, timeline: 3 }, 84)
  // Each segment pads one cell each side, 8 separators, 2 for the `▸`.
  const width = s.reduce((n, x) => n + x.label.length + 2, 0) + 8 + 2
  expect(width).toBe(84)
})

// ---------- §2.2 pinned: the console's own offset ----------

test('pinned: the offset is the console own, clamped so the window stays full, a focused row kept in view', () => {
  const rows = [1, 1, 3, 1, 1, 1, 2, 1]
  // Window of 4 rows: the furthest offset still fills it.
  expect(clampOffset(99, rows, 4)).toBe(5)
  expect(clampOffset(-3, rows, 4)).toBe(0)
  expect(clampOffset(2, rows, 4)).toBe(2)
  // A body that fits never scrolls.
  expect(clampOffset(3, [1, 1], 10)).toBe(0)
  const keys = rows.map((_, i) => new Set(['r' + i]))
  const v = { rows, keys, visible: 4 }
  expect(offsetShowing('r1', 3, v)).toBe(1) // above the window: it moves up to it
  expect(offsetShowing('r4', 3, v)).toBe(3) // already showing: unmoved
  expect(offsetShowing('r7', 0, v)).toBe(5) // below: the least move that shows it
  expect(offsetShowing('nowhere', 2, v)).toBe(2)
})

test('pinned: the body scrolls a row at a time (plain columns open into their rows), estimated high', () => {
  const T = (text: string, props: Record<string, unknown> = {}) => ({ type: 'Text', props, children: [text] })
  const plain = { type: 'Box', props: { flexDirection: 'column' }, children: [T('a'), T('b')] }
  const card = { type: 'Box', props: { flexDirection: 'column', borderStyle: 'round' }, children: [T('c')] }
  expect(flatten([plain, card]).length).toBe(3)
  expect(estRows(T('x'.repeat(170)), 84)).toBe(3)
  expect(estRows(T('x'.repeat(170), { wrap: 'truncate-end' }), 84)).toBe(1)
  expect(estRows(card, 84)).toBe(3)
})

// ---------- §3 Home ----------

test('Home drops Latest, then Load balancers, then Wall, and says so; People shrinks to who is here last', () => {
  const blocks = { vote: { rows: 4 }, beacon: { rows: 1 }, people: { rows: 6, rowsHere: 4 }, wall: { rows: 4 }, lb: { rows: 2 }, latest: { rows: 4 } }
  // All of it: 4+1+6+4+2+4 rows and five blanks between them, plus 6 fixed.
  expect(planHome(blocks, 32, 6)).toEqual({ shown: ['vote', 'beacon', 'people', 'wall', 'lb', 'latest'], dropped: [], hereOnly: false, moreLine: '' })
  expect(planHome(blocks, 31, 6).dropped).toEqual(['latest'])
  expect(planHome(blocks, 31, 6).moreLine).toBe('More in Timeline.')
  expect(planHome(blocks, 26, 6).moreLine).toBe('More in Load balancers and Timeline.')
  const tight = planHome(blocks, 22, 6)
  expect(tight.shown).toEqual(['vote', 'beacon', 'people'])
  expect(tight.moreLine).toBe('More in Wall, Load balancers and Timeline.')
  expect(tight.hereOnly).toBe(false)
  expect(planHome(blocks, 20, 6).hereOnly).toBe(true)
  // Vote, Beacon and People are never dropped.
  expect(planHome(blocks, 5, 6).shown).toEqual(['vote', 'beacon', 'people'])
  // Two columns: the height is the taller column.
  expect(planHome(blocks, 25, 6, 2).dropped).toEqual([])
  expect(moreWords([])).toBe('')
})

test('Home names only the sources still reading', () => {
  expect(readingWords(['wall', 'lb', 'timeline'])).toBe('Reading the wall, load balancers and timeline…')
  expect(readingWords(['timeline'])).toBe('Reading the timeline…')
  expect(readingWords([])).toBe('')
})

const VOTE = { claimSeq: 212, statement: 'The 5xx rise starts at 15:45Z', authorIsAgent: true, authorHuman: 'bob', positionsSoFar: 1, needed: 2, shortfall: 1, expiresInMs: 250000, mine: false }
const WALL = {
  ok: true,
  sharedBy: 'carol',
  windowMs: 21600000,
  widgets: [
    { id: 'w1', type: 'stat', title: '5xx error rate', value: '0.2', unit: '%', tone: 'critical', deltaLabel: 'peak 3.1%', spark: [0.1, 0.2, 3.1, 0.4, 0.2] },
    { id: 'w2', type: 'stat', title: 'Healthy origins', value: '4/6', tone: 'warning', delta: -2, trend: 'down', spark: [6, 6, 5, 4, 4] },
    { id: 'w3', type: 'stat', title: 'Empty one', empty: true },
  ],
}
const ANSWERS = { wall: WALL, lb: { ok: true, loadBalancers: [] }, timeline: { ok: true, events: [] }, artifacts: { ok: true, artifacts: [] }, brief: { ok: false, error: 'not yet' } }

for (const surface of ['terminal', 'desktop'] as const) {
  test(`${surface}: Home puts the vote, Beacon, who is here and the wall on one screen`, async ($, on) => {
    const r = roomOf({ votes: [VOTE], status: { ...roomOf().status, beaconStep: { step: 6, text: 'comparing 5xx per target group in us-east-1' } } })
    const w = await world($, on, { rooms: [r], answers: ANSWERS, surface })
    await $.command.run({ command: 'landfall', args: '' })
    await w.clock.settle()
    const pane = await $.ui.mount(consolePane(surface) as never)
    const words = await texts(pane)
    expect(words).toContain('Your vote is waiting')
    expect(words).toContain(labelText(surface, '4m 10s left'))
    expect(words.some((t) => t.includes('Beacon step 6 · comparing 5xx per target group in us-east-1'))).toBe(true)
    expect(words).toContain('Here · 3')
    expect(words).toContain('Wall · shared by carol')
    expect(words).toContain('5xx error rate')
    // A widget with no data is not a tile.
    expect(words).not.toContain('Empty one')
    // c corroborates as the person, through the CLI.
    expect((await pane.find({ type: 'Button', key: 'hv-c' }))?.props.hotkey).toBe('c')
    expect(w.counts.mcp).toBe(0)
  })
}

test('terminal: Home on a short inline pane drops from the bottom and says what moved', async ($, on) => {
  const events = [1, 2, 3].map((i) => ({ seq: 300 + i, at: '2026-10-08T16:0' + i + ':00Z', glyph: '·', text: 'event ' + i }))
  const w = await world($, on, { rooms: [roomOf({ votes: [VOTE] })], answers: { ...ANSWERS, timeline: { ok: true, events } } })
  await $.command.run({ command: 'landfall', args: '' })
  await w.clock.settle()
  const tall = await $.ui.mount(consolePane('terminal', 60) as never)
  expect(await texts(tall)).toContain('Latest')
  await tall.unmount()
  const short = await $.ui.mount(consolePane('terminal', 20) as never)
  const words = await texts(short)
  expect(words).not.toContain('Latest')
  expect(words).not.toContain('Wall · shared by carol')
  expect(words).toContain('Your vote is waiting')
  expect(words).toContain('More in Wall and Timeline.')
})

// Home's People rows: a real name must fit its column with a cell to spare, `(you)` on the same
// line, and the next column must not start flush against it (a live run drew `●collab-… (yo` with
// `u)` wrapped, and `collab-car…war room` with no gap).
const LONG_PEOPLE = [
  { name: 'collab-alice', you: true, here: true, agents: [{ tool: 'Claude Code', label: 'collab-alice-claude-code', here: true }] },
  { name: 'collab-carol', here: true, browser: true },
  { name: 'collab-bob-the-very-long-named-responder', here: true, browser: true },
]

function textOf(n: any): string {
  if (!n || typeof n !== 'object') return ''
  return (n.children ?? []).map((c: any) => (typeof c === 'string' ? c : textOf(c))).join('')
}

function findAll(n: any, pick: (n: any) => boolean, out: any[] = []): any[] {
  if (!n || typeof n !== 'object') return out
  if (pick(n)) out.push(n)
  for (const c of n.children ?? []) findAll(c, pick, out)
  return out
}

test('terminal: Home people rows with long names fit their column, (you) on the same line, a gap before the next column', async ($, on) => {
  const r = roomOf({ status: { ...roomOf().status, people: LONG_PEOPLE } })
  const w = await world($, on, { rooms: [r], answers: ANSWERS })
  await $.command.run({ command: 'landfall', args: '' })
  await w.clock.settle()
  const pane = await $.ui.mount(consolePane('terminal') as never)
  const tree = await pane.drawn()
  const rows = findAll(tree, (n) => /^hp-t-r\d+$/.test(String(n.key ?? n.props?.key ?? '')))
  expect(rows).toHaveLength(3)
  const nameParts = (row: any) => row.children[0].children[0].children.map(textOf)
  const byName = (name: string) => rows.find((row: any) => nameParts(row)[1].startsWith(name))
  for (const row of rows) {
    const [nameCol, whereCol] = row.children
    const width = nameCol.props.width
    // The name cell is one row of Texts (dot, name, (you)); their total leaves a cell before the next column.
    expect(nameParts(row).join('').length).toBeLessThanOrEqual(width - 1)
    expect(textOf(whereCol).length).toBeLessThanOrEqual(whereCol.props.width - 1)
  }
  expect(nameParts(byName('collab-alice'))).toEqual(['● ', 'collab-alice', ' (you)'])
  expect(nameParts(byName('collab-carol'))).toEqual(['● ', 'collab-carol'])
  // A name longer than its column is clipped with an ellipsis, not left to wrap.
  expect(nameParts(byName('collab-bob'))[1]).toMatch(/…$/)
})

test('desktop: Home people rows clip a very long name, (you) kept', async ($, on) => {
  const r = roomOf({ status: { ...roomOf().status, people: [{ ...LONG_PEOPLE[0], name: 'collab-alice-with-a-very-long-display-name' }, LONG_PEOPLE[1]] } })
  const w = await world($, on, { rooms: [r], answers: ANSWERS, surface: 'desktop' })
  await $.command.run({ command: 'landfall', args: '' })
  await w.clock.settle()
  const pane = await $.ui.mount(consolePane('desktop') as never)
  const words = await texts(pane)
  const you = words.find((t) => t.endsWith(' (you)'))!
  expect(you.length).toBeLessThanOrEqual(28)
  expect(you).toContain('…')
  expect(words).toContain('collab-carol')
})

test('Home with no room points at Incidents above', async ($, on) => {
  await world($, on, { answers: { whoami: { ok: true, signedIn: true, org: 'acme' }, incidents: { ok: true, org: 'acme', incidents: [] } } })
  await $.command.run({ command: 'landfall', args: 'home' })
  const pane = await $.ui.mount(consolePane('terminal') as never)
  const words = await texts(pane)
  expect(words).toContain('Not in a war room yet. Open Incidents above to join one, or open a share link from the room.')
  expect(words).toContain('not in a war room')
})

// ---------- §2.6 entry flow ----------

const LIST = {
  ok: true,
  org: 'acme',
  incidents: [
    { incidentId: 'i171', displayId: 'Landfall 171', title: 'cache-stampede', severity: 'sev1', status: 'investigating', joined: false, roomKey: '', webUrl: 'https://app.landfalls.ai/o/acme/incidents/i171', ageMs: 720000, hereCount: 2, here: ['alice', 'carol'] },
    { incidentId: 'i173', displayId: 'Landfall 173', title: 'search-indexer-lag', severity: 'sev3', status: 'mitigated', joined: false, roomKey: '', webUrl: 'https://app.landfalls.ai/o/acme/incidents/i173', ageMs: 10800000 },
  ],
}
const BRIEF = { ok: true, text: 'Landfall 171 · cache-stampede\nAs of seq 40', asOfSeq: 40, counts: { established: 2, open: 3, participants: 4 }, established: [], open: [], participants: [] }

test('signed out: /landfall lands on Home as the sign-in state (titled Sign in), which runs the CLI handoff and never opens a browser', async ($, on) => {
  const w = await world($, on, {
    answers: { whoami: { ok: true, signedIn: false, instance: { web: 'https://app.landfalls.ai' } }, incidents: LIST },
    login: { pieces: [{ stream: 'stdout', text: '{"event":"url","url":"https://app.landfalls.ai/cli-auth?nonce=k3Jx"}\n{"ok":true,"org":"acme","web":"https://app.landfalls.ai"}\n' }] },
  })
  await $.command.run({ command: 'landfall', args: '' })
  expect(w.runs[0]).toEqual(['landfall', 'whoami', '--json', '--host', 'claude-code'])
  const pane = await $.ui.mount(consolePane('terminal') as never)
  let words = await texts(pane)
  // Round 3 review, issue 4: nothing can be read signed out, so Home is the active tab and the title says Sign in.
  expect(words).toContain(' ▸ Home ')
  expect(words).not.toContain(' ▸ Incidents ')
  expect(w.opened.at(-1)?.title).toBe('Landfall · Sign in')
  expect(words).toContain('not signed in')
  expect(words).toContain('Sign in to Landfall')
  expect(words).toContain('Your browser opens the Landfall sign-in page at app.landfalls.ai. Finish there; this console continues on its own.')
  expect(words).toContain('Signing in is what lets you join an incident from here. A room you join by share link works without it.')
  expect((await pane.find({ type: 'Button', key: 'si-go' }))?.props).toMatchObject({ hotkey: 's', label: 'sign in with your browser' })
  // The list is not read while signed out.
  expect(w.count('incidents')).toBe(0)

  await pane.press({ key: 'si-go' })
  expect(w.spawned.find((a) => a[1] === 'login')).toEqual(['landfall', 'login', '--json', '--host', 'claude-code'])
  expect(w.toasts).toEqual(['Signed in to acme.'])
  // Same session: the picker reads at once.
  expect(w.count('incidents')).toBe(1)
  words = await texts(pane)
  // Signed in, the console goes to the picker and the title goes with it.
  expect(words).toContain(' ▸ Incidents ')
  expect(w.opened.at(-1)?.title).toBe('Landfall · Incidents')
  expect(words).toContain('Join an incident')
  expect(words).toContain('acme · 2 open')
  expect(words).toContain('Enter: join · your agent gets the shared context when you do')
  expect(w.counts.mcp).toBe(0)
  expect(w.filled).toEqual([])
})

test('the sign-in that does not finish says why in the designed words, with s: try again', async ($, on) => {
  const w = await world($, on, {
    answers: { whoami: { ok: true, signedIn: false } },
    login: { pieces: [{ stream: 'stdout', text: '{"event":"url","url":"https://app.landfalls.ai/cli-auth?nonce=1"}\n{"ok":false,"error":"timed out waiting for the browser to complete sign-in"}\n' }], code: 1 },
  })
  await $.command.run({ command: 'landfall', args: '' })
  const pane = await $.ui.mount(consolePane('desktop') as never)
  await pane.press({ key: 'si-go' })
  const words = await texts(pane)
  expect(words).toContain('Sign-in did not finish')
  expect(words).toContain('Your browser did not finish within 5 minutes.')
  expect(words).toContain('Nothing changed. Try again, or run landfall login in a terminal.')
  expect((await pane.find({ type: 'Button', key: 'si-go' }))?.props).toMatchObject({ label: 'Try again', variant: 'primary' })
  expect(w.toasts).toEqual([])
})

test('while the browser handoff waits: the link, the clock, and x cancels the CLI', async ($, on) => {
  let release: () => void = () => {}
  const gate = new Promise<void>((r) => (release = r))
  const w = await world($, on, {
    answers: { whoami: { ok: true, signedIn: false } },
    login: { pieces: [{ stream: 'stdout', text: '{"event":"url","url":"https://app.landfalls.ai/cli-auth?nonce=k3Jx"}\n' }], gate, code: 1 },
  })
  await $.command.run({ command: 'landfall', args: '' })
  const pane = await $.ui.mount(consolePane('terminal') as never)
  const pressed = pane.press({ key: 'si-go' })
  let words: string[] = []
  for (let i = 0; i < 40 && !words.includes('Waiting for your browser…'); i++) {
    await new Promise((r) => setTimeout(r, 5))
    words = await texts(pane)
  }
  expect(words).toContain('Waiting for your browser…')
  expect(words).toContain('You have 5 minutes. The console goes back to the incident list when you are in.')
  expect((await pane.find({ type: 'Link' }))?.props).toMatchObject({ href: 'https://app.landfalls.ai/cli-auth?nonce=k3Jx', label: 'the Landfall sign-in page' })
  expect(words.some((t) => /^waiting · \d+s$/.test(t))).toBe(true)
  await pane.press({ key: 'si-cancel' })
  release()
  await pressed
  expect(w.toasts).toEqual(['Sign-in cancelled.'])
  expect(await texts(pane)).toContain('Sign in to Landfall')
})

test('signed in, not in a room: the picker; Enter joins, adds the shared context, says so once and goes Home', async ($, on) => {
  const w = await world($, on, {
    answers: {
      whoami: { ok: true, signedIn: true, org: 'acme' },
      incidents: LIST,
      join: { ok: true, roomKey: 'k171', incidentId: 'i171', displayId: 'Landfall 171', title: 'cache-stampede' },
      brief: BRIEF,
    },
  })
  await $.command.run({ command: 'landfall', args: '' })
  const pane = await $.ui.mount(consolePane('terminal') as never)
  const first = (await pane.findAll({ type: 'Button' })).find((b) => String(b.key).startsWith('inc-'))
  expect(first?.key).toBe('inc-i171')
  expect(first?.props.autoFocus).toBe(true)
  await pane.press({ key: 'inc-i171' })
  expect(w.runs.find((r) => r[1] === 'join')).toEqual(['landfall', 'join', '--incident', 'i171', '--host', 'claude-code'])
  expect(w.runs.find((r) => r[1] === 'brief')).toEqual(['landfall', 'brief', '--host', 'claude-code', '--room', 'k171'])
  expect(w.toasts).toEqual(['Joined Landfall 171. Shared context added: 2 established, 3 open.'])
  expect(w.toasts.every((t) => t.length <= TOAST_MAX)).toBe(true)
  // One row the model reads, no turn: the shared context with its framing line.
  const rows = w.session.appended()
  expect(rows).toHaveLength(1)
  expect(JSON.stringify(rows[0].message)).toContain('[Landfall war room] Shared context of Landfall 171')
  expect(w.filled).toEqual([])
  // The console is on Home now.
  expect(await texts(pane)).toContain(' ▸ Home ')
})

test('a join refused for want of a sign-in turns the tab into the sign-in state, expired', async ($, on) => {
  const w = await world($, on, {
    answers: {
      whoami: { ok: true, signedIn: true, org: 'acme' },
      incidents: LIST,
      join: { ok: false, error: 'Sign in to join an incident from here: run landfall login.' },
    },
  })
  await $.command.run({ command: 'landfall', args: '' })
  const pane = await $.ui.mount(consolePane('terminal') as never)
  await pane.press({ key: 'inc-i171' })
  expect(w.toasts).toEqual(['Not joined: Sign in to join an incident from here: run landfall login.'])
  const words = await texts(pane)
  expect(words).toContain('Your sign-in expired.')
  expect(words).toContain('Sign in to Landfall')
})

test('the person closing the console stops its reads', async ($, on) => {
  const w = await world($, on, { rooms: [], answers: { whoami: { ok: true, signedIn: true }, incidents: LIST } })
  await $.command.run({ command: 'landfall', args: 'incidents' })
  const pane = await $.ui.mount(consolePane('terminal') as never)
  expect(w.count('incidents')).toBe(1)
  await w.clock.advance(30000)
  expect(w.count('incidents')).toBe(2)
  await pane.press({ key: 'close' })
  expect(w.closed).toEqual(['landfall'])
  await w.clock.advance(60000)
  expect(w.count('incidents')).toBe(2)
})

// ---------- §5.1 band key 2 ----------

test('band key 2 opens the console on Home, asked; while it is open it reads go to the console', async ($, on) => {
  const w = await world($, on, { rooms: [roomOf({ count: 1, maxSeq: 234, digest: ['#234 chat.message [bob@acme.com] — rolling back'] })], answers: ANSWERS })
  const band = await $.ui.mount({ ...BAND, surface: 'terminal' } as never)
  expect((await band.find({ type: 'Button', key: 'console' }))?.props).toMatchObject({ hotkey: '2', label: 'open the console' })
  await band.press({ key: 'console' })
  expect(w.opened).toEqual([{ id: 'landfall', focus: true, columns: 84, title: 'Landfall · Home' }])
  await band.unmount()
  const again = await $.ui.mount({ ...BAND, surface: 'terminal' } as never)
  expect((await again.find({ type: 'Button', key: 'console' }))?.props.label).toBe('go to the console')
})

test('the prompt does not close the console', async ($, on) => {
  const w = await world($, on, { rooms: [roomOf()], answers: ANSWERS })
  await $.command.run({ command: 'landfall', args: '' })
  await $.prompt.submit({ text: 'what broke?', wait: false, origin: { kind: 'user' } } as never)
  expect(w.closed).toEqual([])
})

// ---------- copy: every toast fits the engine's box ----------

test('every toast the shell raises is 80 characters or less, the outcome first', () => {
  const long = 'x'.repeat(300)
  for (const t of [NOT_PLACED, notJoinedWords(long), toastText('Signed in to ' + 'a'.repeat(90) + '.'), 'Sign-in cancelled.']) {
    expect(t.length).toBeLessThanOrEqual(TOAST_MAX)
  }
  expect(notJoinedWords('Landfall has no incident i999.')).toBe('Not joined: Landfall has no incident i999.')
  const name = clipMiddle('postmortem-acme-95-final-final-v2.pdf', 32)
  expect(name.length).toBe(32)
  expect(name.startsWith('postmortem-')).toBe(true)
  expect(name.endsWith('-v2.pdf')).toBe(true)
  expect(name).toContain('…')
  expect(clipMiddle('runbook.md', 32)).toBe('runbook.md')
  expect(reasonWords('timed out waiting for the browser to complete sign-in')).toBe('Your browser did not finish within 5 minutes.')
  expect(reasonWords('cannot reach https://landfall.example.com')).toBe('cannot reach https://landfall.example.com')
  expect(elapsed(72000)).toBe('1m 12s')
  expect(webHost('https://app.landfalls.ai')).toBe('app.landfalls.ai')
})
