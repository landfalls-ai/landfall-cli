import { expect, test } from 'claude-code/testing'
import * as tlMod from '../hooks/components/timeline.js'
import * as incMod from '../hooks/components/incidents.js'
import { signin, signinBody } from '../hooks/components/signin.js'
import { BUTTON_CHROME, textCells } from '../hooks/kit.js'
import { Drawn, fakeIo, kitFor, openConsoleOn, setRooms, settle } from './_tab'
import { labelIn } from './_label'

// Round 3: the round 2 design review's items for Timeline (13), Incidents (17) and the sign-in
// states (16, 20), one test per fix the test kit can see.

const ROOM = { roomKey: 'k168', incidentId: 'i168', displayId: '168', title: 'cloudfront-5xx-high', slug: 'acme', connection: 'live', count: 0, addressed: 0, votesAwaited: 0, maxSeq: 214, digest: [], status: { status: 'investigating', severity: 'SEV2', people: [] } }
const NOW = 1791472800000
const EVENTS = [
  { seq: 201, at: '2026-10-08T15:52:00Z', kind: 'status', glyph: '▲', tone: 'critical', text: 'Triggered by Datadog', who: 'Datadog', detail: '' },
  { seq: 203, at: '2026-10-08T15:53:00Z', kind: 'status', glyph: '●', tone: 'warning', text: 'Status set to investigating', who: 'carol', detail: '' },
  { seq: 204, at: '2026-10-08T15:53:20Z', kind: 'beacon', glyph: '◆', tone: 'violet', text: 'Beacon run 1 started', who: 'Beacon', detail: '' },
  { seq: 209, at: '2026-10-08T16:06:00Z', kind: 'findings', glyph: '✓', tone: 'good', text: '#209 admitted: rollback ready', who: 'carol', detail: '' },
]

test('issue 13: the desktop timeline chips carry no key chip and fit one row; the terminal keeps its letters', async () => {
  setRooms([ROOM])
  openConsoleOn('timeline')
  const io = fakeIo(() => ({ ok: true, events: EVENTS, hasMore: false, oldestSeq: 201 }))
  await tlMod.warm(io as never)
  await settle()
  const chips = (surface: 'terminal' | 'desktop') => new Drawn(tlMod.tab(kitFor(surface, 48), io as never, NOW, null)).all({ type: 'Button' }).filter((b) => /^kind-/.test(String(b.key)))
  const desk = chips('desktop')
  expect(desk.map((b) => b.props.label)).toEqual(['▸ All', 'Findings', 'Status', 'Beacon', 'People', 'Other'])
  expect(desk.every((b) => b.props.hotkey === undefined)).toBe(true)
  // Six chips of the short words, side by side, in a row that does not need a second line at 48 cells.
  const row = new Drawn(tlMod.tab(kitFor('desktop', 48), io as never, NOW, null)).find({ key: 'tl-kinds' })!
  const cells = row.children.reduce((n: number, b: any) => n + BUTTON_CHROME + textCells(String(b.props.label)), 0) + (row.children.length - 1)
  expect(cells).toBeLessThan(50)
  expect(chips('terminal').map((b) => b.props.hotkey)).toEqual(['a', 'f', 's', 'b', 'p', 'o'])
})

test('issue 13: every desktop event glyph sits in one fixed slot, so the text starts at one x; the terminal has none', async () => {
  setRooms([ROOM])
  openConsoleOn('timeline')
  const io = fakeIo(() => ({ ok: true, events: EVENTS, hasMore: false, oldestSeq: 201 }))
  await tlMod.warm(io as never)
  await settle()
  const desk = new Drawn(tlMod.tab(kitFor('desktop'), io as never, NOW, null))
  const slots = desk.all({ type: 'Box', key: 'gs' })
  expect(slots).toHaveLength(EVENTS.length)
  expect(new Set(slots.map((b) => b.props.width)).size).toBe(1)
  expect(slots.every((b) => b.props.flexShrink === 0 && b.props.justifyContent === 'center')).toBe(true)
  const term = new Drawn(tlMod.tab(kitFor('terminal'), io as never, NOW, null))
  expect(term.all({ type: 'Box', key: 'gs' })).toHaveLength(0)
})

const row = (id: string, title: string, severity: string, status: string, extra: Record<string, unknown> = {}) => ({
  incidentId: id, displayId: 'Landfall ' + id.slice(1), title, slug: 'acme', severity, status, openedAt: '2026-10-08T15:38:00Z', joined: false, roomKey: '', webUrl: 'https://app.landfalls.ai/o/acme/incidents/' + id, ageMs: 2520000, ...extra,
})
const LIST = {
  ok: true,
  org: 'acme',
  incidents: [
    row('i168', 'cloudfront-5xx-high-with-a-very-long-name-that-cannot-fit-on-one-row-of-the-dock', 'sev2', 'investigating', { hereCount: 0, practice: true }),
    row('i171', 'checkout-latency-p99', 'sev3', 'identified', { hereCount: 0 }),
  ],
}

test('issue 17 and round 3 issue 10: an incident card head is SEV, title, status on one row; the age rides on the meta line; practice sits there too', async () => {
  setRooms([ROOM])
  openConsoleOn('incidents')
  const io = fakeIo(() => LIST)
  await incMod.load(io as never)
  const got: any = incMod.tab(kitFor('desktop', 48), io as never, NOW, null)
  const pane = new Drawn(got.rows)
  const head = pane.find({ type: 'Box', key: 'h-i168' })!
  expect(head.props.flexWrap).toBe('nowrap')
  // Order: severity, title, status. The age is on the meta line (round 3 review, issue 10).
  expect(head.children.map((c: any) => c.props.key)).toEqual(['sp-w-i168', 't-w-i168', 'stp-w-i168'])
  expect(labelIn(head.children[0], 'SEV2')?.tone).toBe('#ec835a')
  const title = pane.find({ type: 'Text', key: 't-i168' })!
  expect(String(title.children[0]).endsWith('…')).toBe(true)
  expect(title.props.wrap).toBe('truncate-end')
  expect(head.children[1].props.flexShrink).toBe(1)
  expect(head.children[2].props.flexShrink).toBe(0)
  // A short title is kept whole in a roomier pane, and its card has no practice label.
  const roomy: any = incMod.tab(kitFor('desktop', 84), io as never, NOW, null)
  expect(new Drawn(roomy.rows).find({ type: 'Text', key: 't-i171' })!.children[0]).toBe('Landfall 171 · checkout-latency-p99')
  expect(pane.find({ key: 'prp-i171' })).toBeUndefined()
  // The practice label is on the meta line, beside the note, never in the head.
  const meta = pane.find({ type: 'Box', key: 'meta-i168' })!
  expect(meta).toBeDefined()
  expect(pane.all({ key: 'prp-i168' })).toHaveLength(1)
  expect(head.children.some((c: any) => c.props.key === 'prp-i168')).toBe(false)
  expect(new Drawn(meta).words().join(' ')).toContain('nobody here yet · 42m')
  // Its age follows the people on a card with no practice label too.
  expect(new Drawn(pane.find({ type: 'Box', key: 'meta-i171' })).words().join(' ')).toContain('42m')
})

test('issue 16: the sign-in states leave the one blank row before the keys to the console, so the rows never end in a blank', () => {
  const was = { ...signin }
  try {
    for (const phase of ['idle', 'failed', 'waiting'] as const) {
      Object.assign(signin, { phase, signedIn: false, reason: 'The sign-in did not answer.', startedAt: NOW })
      for (const surface of ['terminal', 'desktop'] as const) {
        const body = signinBody(kitFor(surface), fakeIo() as never, NOW, async () => {})
        expect(body.rows.some((r: any) => r.children && r.children.join('').trim() === '')).toBe(false)
      }
    }
  } finally {
    Object.assign(signin, was)
  }
})

test('issue 20: no sign-in row repeats the Landfall name directly under the pane title', () => {
  const was = { ...signin }
  try {
    Object.assign(signin, { phase: 'idle', signedIn: false })
    const words = new Drawn(signinBody(kitFor('desktop'), fakeIo() as never, NOW, async () => {}).rows).words()
    expect(words.filter((w) => w.trim() === 'Landfall')).toEqual([])
  } finally {
    Object.assign(signin, was)
  }
})
