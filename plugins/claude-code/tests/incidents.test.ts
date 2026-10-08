import { expect, test } from 'claude-code/testing'
import { incidentsText, notOpenedWords, openedWords } from '../hooks/components/incidents.js'
import { TOAST_MAX } from '../hooks/core.js'
import { consolePane, roomOf, texts, world } from './_console'

// The Incidents tab of the console (spec §4.6): `landfall incidents` as the Go CLI prints it
// (internal/cli/incidents.go): severity and status lowercased, openedAt and slug on every row,
// here/hereCount/beacon only for the rooms this checkout is in, `practice` on a simulated
// incident, `truncated` when the list was cut.
const row = (id: string, title: string, severity: string, status: string, openedAt: string, ageMs: number, extra: Record<string, unknown> = {}) => ({
  incidentId: id,
  displayId: 'Landfall ' + id.slice(1),
  title,
  slug: 'acme',
  severity,
  status,
  openedAt,
  joined: false,
  roomKey: '',
  webUrl: 'https://app.landfalls.ai/o/acme/incidents/' + id,
  ageMs,
  ...extra,
})
const LIST = {
  ok: true,
  org: 'acme',
  incidents: [
    row('i165', 'cdn-cert-renewal', 'sev4', 'mitigated', '2026-10-08T14:20:00Z', 7200000),
    row('i171', 'checkout-latency-p99', 'sev3', 'identified', '2026-10-08T16:02:00Z', 1080000),
    row('i172', 'orders-db-replica-lag', 'sev1', 'open', '2026-10-08T16:17:00Z', 180000),
    row('i168', 'cloudfront-5xx-high', 'sev2', 'investigating', '2026-10-08T15:38:00Z', 2520000, { joined: true, roomKey: 'k168', here: ['carol', 'bob', 'alice', 'you'], hereCount: 4, beacon: 'running' }),
  ],
}
const JOINED = { ok: true, roomKey: 'k172', incidentId: 'i172', displayId: 'Landfall 172', title: 'orders-db-replica-lag', link: 'https://app.landfalls.ai/j/abc123' }
const BRIEF = { ok: true, text: 'Landfall 172 · orders-db-replica-lag', asOfSeq: 9, counts: { established: 0, open: 0, participants: 2 }, established: [], open: [], participants: [] }

for (const surface of ['terminal', 'desktop'] as const) {
  test(`${surface}: in a room, Incidents lists the room you are in first, then the unjoined SEV1, and Enter joins through the CLI`, async ($, on) => {
    const w = await world($, on, { rooms: [roomOf()], surface, answers: { incidents: LIST, join: JOINED, brief: BRIEF } })
    await $.command.run({ command: 'landfall', args: 'incidents' })
    expect(w.runs.find((r) => r[1] === 'incidents')).toEqual(['landfall', 'incidents', '--host', 'claude-code'])
    const pane = await $.ui.mount(consolePane(surface) as never)
    const buttons = (await pane.findAll({ type: 'Button' })).map((b) => String(b.key)).filter((k) => k.startsWith('inc-'))
    expect(buttons).toEqual(['inc-i168', 'inc-i172', 'inc-i171', 'inc-i165'])
    const words = await texts(pane)
    expect(words).toContain('Open incidents')
    expect(words).toContain('acme · 4')
    expect(words.some((t) => t.includes('you are in it · 4 here: carol, bob, alice, you · Beacon running'))).toBe(true)
    // The severity and status are labels; a label is never pressable.
    expect(words).toContain('● SEV1')
    expect(words).toContain('● investigating')
    // The hint names what Enter does on the focused row: your own room opens.
    expect(words).toContain('Enter: open the room')

    await pane.press({ key: 'inc-i172' })
    expect(w.runs.find((r) => r[1] === 'join')).toEqual(['landfall', 'join', '--incident', 'i172', '--host', 'claude-code'])
    expect(w.toasts).toEqual(['Joined Landfall 172. Shared context added: a fresh incident, 2 participants.'])
    expect(w.counts.mcp).toBe(0)
  })

  test(`${surface}: brief only drafts a prompt and never sends it`, async ($, on) => {
    const w = await world($, on, { rooms: [roomOf()], surface, answers: { incidents: LIST } })
    await $.command.run({ command: 'landfall', args: 'incidents' })
    const pane = await $.ui.mount(consolePane(surface) as never)
    await pane.press({ key: surface === 'terminal' ? 'brief' : 'brief-i172' })
    // On the terminal `b` acts on the focused incident: the first one, until the ring moves.
    expect(w.filled).toEqual([surface === 'terminal' ? 'Give me the brief for Landfall 168' : 'Give me the brief for Landfall 172'])
    expect(w.runs.filter((r) => r[1] === 'join')).toEqual([])
  })

  test(`${surface}: the CLI's own sentence when it cannot list`, async ($, on) => {
    await world($, on, { rooms: [roomOf()], surface, answers: { incidents: { ok: false, error: 'The incident list did not answer: 502.' } } })
    await $.command.run({ command: 'landfall', args: 'incidents' })
    const pane = await $.ui.mount(consolePane(surface) as never)
    expect(await texts(pane)).toContain('The incident list did not answer: 502.')
  })

  test(`${surface}: o opens the room in the browser through the CLI, and says so in a short toast`, async ($, on) => {
    const w = await world($, on, { rooms: [roomOf()], surface, answers: { incidents: LIST, open: { ok: true } } })
    await $.command.run({ command: 'landfall', args: 'incidents' })
    const pane = await $.ui.mount(consolePane(surface) as never)
    await pane.press({ key: surface === 'terminal' ? 'open' : 'open-i168' })
    expect(w.runs.find((r) => r[1] === 'open')).toEqual(['landfall', 'open', 'https://app.landfalls.ai/o/acme/incidents/i168', '--host', 'claude-code'])
    expect(w.toasts).toEqual(['Opened Landfall 168 in your browser.'])
    expect(await pane.find({ type: 'Link' })).toBeUndefined()
  })

  test(`${surface}: when no browser answers, the toast says why and the row gains a link`, async ($, on) => {
    const w = await world($, on, { rooms: [roomOf()], surface, answers: { incidents: LIST, open: { ok: false, error: 'no browser answered' } } })
    await $.command.run({ command: 'landfall', args: 'incidents' })
    const pane = await $.ui.mount(consolePane(surface) as never)
    await pane.press({ key: surface === 'terminal' ? 'open' : 'open-i168' })
    expect(w.toasts).toEqual(['Could not open your browser: no browser answered'])
    expect((await pane.find({ type: 'Link' }))?.props).toMatchObject({ href: 'https://app.landfalls.ai/o/acme/incidents/i168', label: 'the war room in your browser' })
  })
}

test('desktop: Join is the primary, your own room reads Open the room, and chips only on the focused card', async ($, on) => {
  const w = await world($, on, { rooms: [roomOf()], surface: 'desktop', answers: { incidents: LIST, brief: BRIEF, join: JOINED } })
  await $.command.run({ command: 'landfall', args: 'incidents' })
  const pane = await $.ui.mount(consolePane('desktop') as never)
  expect((await pane.find({ key: 'inc-i168' }))?.props.label).toBe('Open the room')
  expect((await pane.find({ key: 'inc-i172' }))?.props.variant).toBe('primary')
  // Key chips only on the focused card's buttons.
  expect((await pane.find({ key: 'brief-i168' }))?.props.hotkey).toBe('b')
  expect((await pane.find({ key: 'brief-i172' }))?.props.hotkey).toBeUndefined()
  // Enter on your own room goes Home; nothing is joined.
  await pane.press({ key: 'inc-i168' })
  expect(w.runs.filter((r) => r[1] === 'join')).toEqual([])
  expect(await texts(pane)).toContain('▸ Home')
})

test('a failed join says why, in 80 characters or less, and the row stays', async ($, on) => {
  const w = await world($, on, { rooms: [roomOf()], answers: { incidents: LIST, join: { ok: false, error: 'Landfall has no incident i172 in acme, or it is closed and cannot be joined from here any more.' } } })
  await $.command.run({ command: 'landfall', args: 'incidents' })
  const pane = await $.ui.mount(consolePane('terminal') as never)
  await pane.press({ key: 'inc-i172' })
  expect(w.toasts).toHaveLength(1)
  expect(w.toasts[0].startsWith('Not joined: Landfall has no incident i172')).toBe(true)
  expect(w.toasts[0].length).toBeLessThanOrEqual(TOAST_MAX)
  expect(await pane.find({ key: 'inc-i172' })).toBeDefined()
})

const PRACTICE_LIST = {
  ok: true,
  org: 'acme',
  truncated: true,
  incidents: [row('i180', 'cascade-demo', 'sev2', 'open', '2026-10-08T16:18:00Z', 120000, { practice: true })],
}

for (const surface of ['terminal', 'desktop'] as const) {
  test(`${surface}: a practice incident says so in violet, and a cut list says so`, async ($, on) => {
    await world($, on, { rooms: [roomOf()], surface, answers: { incidents: PRACTICE_LIST } })
    await $.command.run({ command: 'landfall', args: 'incidents' })
    const pane = await $.ui.mount(consolePane(surface) as never)
    expect((await pane.find({ type: 'Text', text: '● practice' }))?.props.color).toBe('#8a5cd6')
    expect(await texts(pane)).toContain('Your organization has more incidents than this list shows. Open the web app to see the rest.')
  })
}

test('the picker leaves a blank row before its hint, as the list in a room does', async ($, on) => {
  await world($, on, { answers: { whoami: { ok: true, signedIn: true, org: 'acme' }, incidents: LIST } })
  await $.command.run({ command: 'landfall', args: '' })
  const pane = await $.ui.mount(consolePane('terminal') as never)
  const words = await texts(pane)
  const at = words.indexOf('Enter: join · your agent gets the shared context when you do')
  expect(at).toBeGreaterThan(0)
  expect(words[at - 1]).toBe(' ')
})

test('incidents in text, where no pane can be drawn: practice, the cut list, the old wording', () => {
  expect(incidentsText({ ok: true, org: 'acme', incidents: LIST.incidents })).toBe(
    [
      'Open incidents · acme',
      '  Landfall 168 cloudfront-5xx-high · SEV2 · investigating · 42m · you are in it · 4 here: carol, bob, alice, you · Beacon running',
      '  Landfall 172 orders-db-replica-lag · SEV1 · open · 3m · not joined',
      '  Landfall 171 checkout-latency-p99 · SEV3 · identified · 18m · not joined',
      '  Landfall 165 cdn-cert-renewal · SEV4 · mitigated · 2h · not joined',
    ].join('\n'),
  )
  expect(incidentsText(PRACTICE_LIST)).toBe(
    ['Open incidents · acme', '  Landfall 180 cascade-demo · SEV2 · open · practice · 2m · not joined', 'Your organization has more incidents than this list shows. Open the web app to see the rest.'].join('\n'),
  )
  expect(incidentsText(null)).toBe('The incident list did not answer.')
})

test('the open toasts fit the engine box', () => {
  expect(openedWords({ displayId: 'Landfall 171' })).toBe('Opened Landfall 171 in your browser.')
  expect(notOpenedWords('x'.repeat(200)).length).toBeLessThanOrEqual(TOAST_MAX)
})
