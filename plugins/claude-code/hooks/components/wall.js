// A mirror of the investigation wall, as the Wall tab of the console (spec
// §4.2): the room's widgets in the arrangement the incident commander shared,
// and, one dashboard selector away, what each colleague has built.
//
// `landfall wall` answers the shared wall: each widget with its data read
// through the person's own session, so the per-viewer credential wall holds.
// `landfall wall --person <humanActorId|me>` answers one person's dashboard
// (their `edge.widget` snapshots, data only) plus their trail and artifacts;
// every wall answer also lists the room's people (`people[]`), which is what
// the selector is made of. The tab draws every widget type natively (vector
// charts on the desktop, cell graphics on the terminal).
//
// While the console is open the shared wall is live (live.md FR-L2, FR-L3): it
// reads again every 15 s, as the web canvas does, and right away when a widget
// event lands (`widgetSeq` on the watch stream); a selected person's dashboard
// reads on the same cadence and on any new room event (their snapshots are
// events the watch stream counts in `maxSeq`). A failed read keeps the last
// good answer and says it is stale. A new widget is told as a toast (FR-L4),
// and the band offers `w` to open the wall for a minute after. `a` drafts a
// question about the selected widget; it never sends.
//
// THE TAB CONTRACT (core.js CONSOLE; console.js calls these by name):
//   tab(k, io, nowMs, args)  the body rows; args: a person's name, `mine` or `topology`
//   badge()                  null (the Wall carries no count)
//   warm(io)                 starts the reads; calling it again reads once more (it is also `r`)
//   keys(k, io)              the tab's own letter Buttons: a, n, d
//   footer(k, nowMs)         the live line of the dashboard shown, or null
//   text(io, args)           the tab as text, where no pane can be placed
//   sharedAnswer()           the last good shared wall answer (Home's tiles read it)
//   graphKey()               the key of the first graph widget, to scroll to
//
// The shapes are the Go CLI's own (internal/cli/wall.go flattenWidget): a
// widget with no data yet carries `empty: true`; a stat may carry trend,
// delta, deltaLabel and baselineLabel (drawn as the web app's StatBody does);
// a graph's nodes may carry kind and its edges direction; geo carries points
// by place and codeFinding a repo, path, permalink and snippet lines. When
// the wall holds more widgets than the CLI reads at once, `totalWidgets` says
// how many there are and the tab says so.

import { estRows } from '../console.js'
import { HOST, ago, clip, consoleState, currentOf, currentRoom, openConsole, reading, room, roomName } from '../core.js'
import { closed, drawn, due, liveFooter, livePane, readLive, WALL_MS, widgetSeqOf, opened as markOpen } from '../live.js'
import {
  chartAlt,
  chartSvg,
  clipText,
  eventLine,
  fmt,
  graphLines,
  graphView,
  hhmm,
  levelTag,
  levelTone,
  markerCaption,
  outcomeTone,
  seriesTone,
  tableRows,
  toneColor,
  values,
  windowWords,
} from '../views.js'

// The web canvas's own cadence (useLiveWidgets intervalMs): an open wall
// reads its data again this often, and within a tick of a widget event.
export const REFRESH_MS = WALL_MS
// New widgets are told at most this often; the band offers `w` this long after.
export const NEWS_GAP_MS = 10000
export const HINT_MS = 60000

const NO_ROOM = 'Not in a war room yet. Open Incidents above to join one, or open a share link from the room.'
const NO_GRAPH = 'The wall has no topology yet. Ask your agent or Beacon to map the services this incident touches.'
const MINE_EMPTY = 'You have not shared a dashboard yet. Ask your agent to share what it reads as a widget.'

// The shared wall's live read (live.js): the last good `landfall wall`
// answer, open or not, stale or not. Module state: a hot reload starts it
// over, and the next draw of the tab marks it open again.
const lp = livePane()
// The selected person's dashboard: `landfall wall --person`.
const pp = livePane()
const wall = {
  roomKey: '',
  // The selector (spec §4.2): 'shared', a member's key (their humanActorId, or
  // `name:<name>` while the CLI has not said who they are), or 'mine'.
  dashboard: 'shared',
  dashName: '',
  selected: 0,
  // What /landfall asked for (a name, `mine`), until the member is known.
  pending: '',
  // /landfall topology: the first graph is the selected widget, once.
  topology: false,
  topologyDone: false,
  // The target the person read in `pp` is of (`me` or a humanActorId).
  ppFor: '',
}

// New widgets (FR-L4): the newest widget seq seen per room (the first sight
// of a room is not news), one waiting while the toast gap runs, when the last
// toast was shown and until when the band offers `w`.
const news = { seen: {}, pending: null, toastAt: null, hintUntil: 0, hintTitle: '' }

// reset starts the tab's state over (tests; a hot reload does the same by itself).
export function reset() {
  Object.assign(lp, livePane())
  Object.assign(pp, livePane())
  Object.assign(wall, { roomKey: '', dashboard: 'shared', dashName: '', selected: 0, pending: '', topology: false, topologyDone: false, ppFor: '' })
  Object.assign(news, { seen: {}, pending: null, toastAt: null, hintUntil: 0, hintTitle: '' })
}

// The tab has no command or pane of its own: /landfall wall opens the console.
export function install(on) {}

export async function band(io, e, k) {
  return null
}

// ---------------------------------------------------------------------------
// People, keyed by humanActorId (spec §8). The watch stream's people rows carry
// it once the CLI says so; until then a row is matched to the wall's
// `people[].displayName` (the server's one string for both), never by position.

const norm = (s) => String(s ?? '').trim().toLowerCase()

// The order people are listed in: those here before those away, you last
// among them (you know what you said), then by name.
export function orderPeople(people) {
  const rank = (p) => (p.here ? 0 : 2) + (p.you ? 1 : 0)
  return [...people].sort((a, b) => rank(a) - rank(b) || String(a.name).localeCompare(String(b.name)))
}

// wallPeopleOf is the answer's people rows: {humanActorId, displayName, edgeAgentLabel, kind, widgets, trail, artifacts, latestSeq}.
export function wallPeopleOf(a) {
  return a && a.ok && Array.isArray(a.people) ? a.people : []
}

// idOfPerson is a watch people row's humanActorId, or '' while nothing says.
export function idOfPerson(p, a) {
  if (p && p.humanActorId) return String(p.humanActorId)
  const want = norm(p && p.name)
  if (!want) return ''
  const row = wallPeopleOf(a).find((w) => w.humanActorId && norm(w.displayName) === want)
  return row ? String(row.humanActorId) : ''
}

// memberList is the selector's members after `Shared wall`: every person in the
// room as People orders them (here first, away last), then people who built
// something and are no longer in it, then `mine` last of all. You are not a
// member of your own: `mine` is you.
export function memberList(r, a) {
  const rows = wallPeopleOf(a)
  const watch = orderPeople((r && r.status && r.status.people) || [])
  const seen = new Set()
  const out = []
  const mine = { key: 'mine', id: 'me', name: 'mine', you: true, here: true, widgets: null }
  for (const p of watch) {
    const id = idOfPerson(p, a)
    if (id) seen.add(id)
    const row = id ? rows.find((w) => w.humanActorId === id) : null
    if (p.you) {
      if (row) mine.widgets = Number(row.widgets) || 0
      if (id) mine.humanActorId = id
      continue
    }
    out.push({ key: id || 'name:' + norm(p.name), id, name: String(p.name || 'someone'), here: !!p.here, widgets: row ? Number(row.widgets) || 0 : 0 })
  }
  for (const w of rows) {
    if (!w.humanActorId || seen.has(w.humanActorId)) continue
    out.push({ key: String(w.humanActorId), id: String(w.humanActorId), name: String(w.displayName || 'someone'), here: false, widgets: Number(w.widgets) || 0 })
  }
  out.push(mine)
  return out
}

// ---------------------------------------------------------------------------
// The selector (spec §4.2): `▸ Shared wall  alice 2  bob  carol 1  mine 2`.

function items(members) {
  const out = [{ key: 'shared', label: 'Shared wall', count: 0 }]
  for (const m of members) {
    let count = m.widgets || 0
    if (m.key === 'mine' && wall.dashboard === 'mine' && pp.answer && pp.answer.ok) count = (pp.answer.widgets || []).length
    out.push({ key: m.key, label: m.name, count })
  }
  return out
}

// fit says how the selector reads in `width` cells: whole, then names clipped
// to 8, then without counts; past that it wraps (it is content, not navigation).
function fit(list, current, width) {
  const label = (it, clipTo, counts) => (clipTo ? clip(it.label, clipTo) : it.label) + (counts && it.count > 0 ? ' ' + it.count : '')
  const cells = (clipTo, counts) => list.reduce((n, it) => n + label(it, clipTo, counts).length + (it.key === current ? 2 : 0), 0) + 2 * (list.length - 1)
  for (const [clipTo, counts] of [[0, true], [8, true], [8, false]]) {
    if (cells(clipTo, counts) <= width) return { texts: list.map((it) => label(it, clipTo, counts)), wrap: false }
  }
  return { texts: list.map((it) => label(it, 8, false)), wrap: true }
}

// The selector is drawn in the chip idiom the Timeline kinds and More use
// (round 4 review, issue 1): plain Buttons with no hotkey, two spaces apart,
// the active member `▸ <name>`, the others dim. Never inverse, never filled,
// never primary: the console's one filled segment is the switcher's.
function selectorRow(k, io, list, current) {
  const { Box, Button } = k.els
  const { texts, wrap } = fit(list, current, k.width)
  const kids = list.map((it, i) => {
    const active = it.key === current
    const props = { key: 'sel-' + it.key, label: (active ? '▸ ' : '') + texts[i], onPress: () => selectDashboard(io, it.key) }
    if (k.terminal) props.plain = true
    if (!active) props.dimColor = true
    return Button(props)
  })
  return Box({ key: 'wall-sel', flexDirection: 'row', columnGap: k.terminal ? 2 : 1, flexWrap: wrap ? 'wrap' : 'nowrap', children: kids })
}

// gap is one blank row between blocks on the terminal, nothing elsewhere.
function gap(k, key) {
  return k.terminal ? k.text(' ', { key }) : null
}

// readTarget is what `--person` takes for a dashboard: `me`, a humanActorId, or
// '' when there is nothing to read (the shared wall, or a person nobody has
// heard from, who has shared nothing).
function readTarget(key, r) {
  if (key === 'shared') return ''
  if (key === 'mine') return 'me'
  const m = memberList(r || currentRoom(), lp.answer).find((x) => x.key === key)
  return m && m.id ? m.id : ''
}

function memberOf(key, members) {
  return members.find((m) => m.key === key) || null
}

// selectDashboard shows one dashboard and reads it when it is a person's.
function selectDashboard(io, key) {
  if (wall.dashboard === key) return
  const r = currentRoom()
  const m = memberOf(key, memberList(r, lp.answer))
  wall.dashboard = key
  wall.dashName = m ? norm(m.name) : ''
  wall.selected = 0
  wall.topology = false
  Object.assign(pp, livePane())
  const target = key === 'shared' ? '' : readTarget(key, r)
  wall.ppFor = target
  if (target) {
    markOpen(pp)
    void loadPerson(io)
  }
  io.invalidate()
}

function nextDashboard(io) {
  const r = currentRoom()
  const list = [{ key: 'shared' }, ...memberList(r, lp.answer)]
  const at = Math.max(0, list.findIndex((x) => x.key === wall.dashboard))
  selectDashboard(io, list[(at + 1) % list.length].key)
}

// takeArgs reads what /landfall passed (`wall <name>`, `wall mine`, `wall topology`).
function takeArgs(args) {
  if (args == null || args === '') return
  if (consoleState.args === args) consoleState.args = null
  wall.pending = String(args).trim()
}

// resolvePending selects the dashboard /landfall named, once its member is known.
function resolvePending(io, members) {
  const want = norm(wall.pending)
  if (!want) return
  if (want === 'topology') {
    wall.pending = ''
    if (wall.dashboard !== 'shared') selectDashboard(io, 'shared')
    wall.topology = true
    wall.topologyDone = false
    return
  }
  if (want === 'shared') {
    wall.pending = ''
    selectDashboard(io, 'shared')
    return
  }
  const m = members.find((x) => norm(x.name) === want) || members.find((x) => norm(x.name).startsWith(want))
  if (m) {
    wall.pending = ''
    selectDashboard(io, m.key)
    return
  }
  // Nobody by that name, once the room has been heard from: stay where we are.
  if (lp.answer || members.length > 1) wall.pending = ''
}

// ---------------------------------------------------------------------------
// The console contract.

export function tab(k, io, nowMs, args) {
  // Drawn means open: after a hot reload the engine draws the console again
  // and the wall goes on reading.
  drawn(lp)
  const r = currentRoom()
  if (!r) return [k.text(NO_ROOM, { key: 'wall-noroom', dimColor: true })]
  takeArgs(args)
  let members = memberList(r, lp.answer)
  resolvePending(io, members)
  members = memberList(r, lp.answer)
  // A person whose id arrived since they were chosen is the same person.
  if (wall.dashboard !== 'shared' && !memberOf(wall.dashboard, members)) {
    const same = wall.dashName ? members.find((m) => norm(m.name) === wall.dashName) : null
    if (same) wall.dashboard = same.key
    else wall.dashboard = 'shared'
  }
  if (wall.dashboard !== 'shared') {
    drawn(pp)
    // The wall's people[] can say who a person is after they were chosen: read them then.
    const target = readTarget(wall.dashboard, r)
    if (target && wall.ppFor !== target) {
      wall.ppFor = target
      Object.assign(pp, livePane())
      markOpen(pp)
      void loadPerson(io)
    }
  }
  const rows = [selectorRow(k, io, items(members), wall.dashboard)]
  // The terminal spends one blank row between blocks (round 2 issue 16); cards space themselves.
  if (k.terminal) rows.push(gap(k, 'wall-g-sel'))
  if (wall.dashboard === 'shared') rows.push(...sharedBody(k, io, nowMs))
  else rows.push(...personBody(k, io, nowMs, memberOf(wall.dashboard, members)))
  return rows
}

export function badge() {
  return null
}

// warm starts the reads; asked again it reads once more, which is what `r` does.
export function warm(io) {
  markOpen(lp)
  void loadWall(io)
  if (wall.dashboard !== 'shared') {
    markOpen(pp)
    void loadPerson(io)
  }
}

// keys are the tab's letters: a ask, n next widget, d next dashboard.
export function keys(k, io) {
  if (!currentRoom()) return []
  const list = shownWidgets()
  const sel = list[clampSel(list)]
  return [
    sel ? k.button({ key: 'wall-ask', label: 'ask about ' + clipText(sel.title || sel.type || 'widget', 28), hotkey: 'a', primary: true, onPress: () => askAbout(io, sel, ownerWords()) }) : null,
    list.length > 1 ? k.button({ key: 'wall-next', label: 'next widget', hotkey: 'n', onPress: () => selectNext(io) }) : null,
    k.button({ key: 'wall-dash', label: 'next dashboard', hotkey: 'd', onPress: () => nextDashboard(io) }),
  ].filter(Boolean)
}

// footer is how old the dashboard shown is, or null before the first good read.
export function footer(k, nowMs) {
  const r = currentRoom()
  if (wall.dashboard !== 'shared' && readTarget(wall.dashboard, r)) return liveFooter(k, pp, nowMs, r, 'wall-live')
  return liveFooter(k, lp, nowMs, r, 'wall-live')
}

// sharedAnswer is the last good shared wall (Home draws its tiles from it).
export function sharedAnswer() {
  return lp.answer && lp.answer.ok ? lp.answer : null
}

// graphKey is the key of the shared wall's first graph widget, for the console to scroll to.
export function graphKey() {
  const a = sharedAnswer()
  const g = graphOf(a)
  return g ? 'w-' + (g.id || (a.widgets || []).indexOf(g)) : ''
}

// text is the tab where no pane can be placed (the old /wall, /topology).
export async function text(io, args) {
  await loadWall(io)
  const r = currentRoom()
  if (norm(args) === 'topology') return topologyText(lp.last)
  return wallText(lp.last, r)
}

// ---------------------------------------------------------------------------
// The bodies.

function sharedBody(k, io, nowMs) {
  const rows = []
  const a = lp.answer
  if (!a) {
    rows.push(k.text(lp.inFlight ? 'Reading the wall through your session…' : 'Press r to read the wall.', { key: 'wall-empty', dimColor: true }))
    return rows
  }
  if (!a.ok) {
    rows.push(k.text(clipText(a.error || 'The wall could not be read.', k.width), { key: 'wall-err' }))
    return rows
  }
  const tail = [a.windowMs ? windowWords(a.windowMs) : '', lp.inFlight ? 'reading…' : ''].filter(Boolean)
  rows.push(heading(k, 'wall-h', a.sharedBy ? 'Wall · shared by ' + a.sharedBy : 'Wall', tail.length ? ' · ' + tail.join(' · ') : ''))
  const widgets = fitRows(k, visibleWidgets(a), { prefix: 'w', gone: (a.unavailable || []).length, kind: 'shared' })
  if (wall.topology) {
    const g = graphOf(a)
    if (!g) rows.push(k.text(NO_GRAPH, { key: 'wall-nograph', dimColor: true }))
    else if (!wall.topologyDone) {
      wall.selected = Math.max(0, widgets.indexOf(g))
      wall.topologyDone = true
    }
  }
  if (widgets.length === 0) rows.push(k.text('Nothing is on the wall yet.', { key: 'wall-none', dimColor: true }))
  const two = k.rich && k.width >= 90
  rows.push(...widgetCards(k, io, widgets, { prefix: 'w', selected: clampSel(widgets), onSelect: (i) => selectWidget(io, i), nowMs, two }))
  const gone = a.unavailable || []
  if (gone.length > 0) {
    rows.push(gap(k, 'un-g'))
    rows.push(k.text('Not shown here', { key: 'un-h', bold: true }))
    gone.forEach((u, i) => rows.push(k.text(unavailableWords(u), { key: 'un-' + i, dimColor: true })))
  }
  const more = moreWords(a, widgets.length)
  if (more) {
    rows.push(gap(k, 'wall-g-more'))
    rows.push(k.text(more, { key: 'wall-more', dimColor: true }))
  }
  return rows.filter(Boolean)
}

// unavailableWords is one widget the wall cannot show here, widget first and the reason in plain
// words, with at most one colon (round 2 issue 19): `RDS replica lag · needs your sign-in. Run
// landfall login.`
export function unavailableWords(u) {
  const name = u.title || u.type || u.id || 'A widget'
  const reason = String(u.reason || 'not available').trim()
  if (/sign in|signed in|sign-in|login/i.test(reason)) return name + ' · needs your sign-in. Run landfall login.'
  return name + ' · ' + reason.replace(/:\s+/g, ', ').replace(/^./, (c) => c.toLowerCase())
}

function personBody(k, io, nowMs, m) {
  const rows = []
  const mine = wall.dashboard === 'mine'
  const name = mine ? 'Your' : (m ? m.name : 'Someone') + "'s"
  const who = readTarget(wall.dashboard, currentRoom())
  const none = mine ? MINE_EMPTY : (m ? m.name : 'Someone') + ' has not shared a dashboard yet.'
  if (!who) {
    rows.push(k.text(none, { key: 'wall-pnone', dimColor: true }))
    return rows
  }
  const a = pp.answer
  if (!a) {
    rows.push(k.text(mine ? 'Reading your dashboard…' : 'Reading ' + (m ? m.name : 'their') + "'s dashboard…", { key: 'wall-pread', dimColor: true }))
    return rows
  }
  if (!a.ok) {
    rows.push(k.text(clipText(a.error || 'The wall could not be read.', k.width), { key: 'wall-err' }))
    return rows
  }
  const widgets = fitRows(k, visibleWidgets(a), { prefix: 'pw', snapshot: true, gone: (a.unavailable || []).length, kind: 'person' })
  if (widgets.length === 0) {
    rows.push(k.text(none, { key: 'wall-pnone', dimColor: true }))
    return rows
  }
  rows.push(heading(k, 'wall-h', name + ' dashboard', dashboardTail(widgets, a.totalWidgets, mine, nowMs, pp.inFlight)))
  rows.push(...widgetCards(k, io, widgets, { prefix: 'pw', selected: clampSel(widgets), onSelect: (i) => selectWidget(io, i), nowMs, snapshot: true, two: k.rich && k.width >= 90 }))
  const more = moreWords(a, widgets.length)
  if (more) {
    rows.push(gap(k, 'wall-g-more'))
    rows.push(k.text(more, { key: 'wall-more', dimColor: true }))
  }
  return rows.filter(Boolean)
}

// dashboardTail is the dim words after a person's heading:
// ` · 2 widgets · snapshots · newest 9m ago`.
export function dashboardTail(widgets, total, mine, nowMs, reading) {
  const n = Math.max(total || 0, widgets.length)
  const parts = [n + (n === 1 ? ' widget' : ' widgets'), 'snapshots']
  if (!mine) {
    const newest = newestAge(widgets, nowMs)
    if (newest) parts.push('newest ' + newest)
  }
  if (reading) parts.push('reading…')
  return ' · ' + parts.join(' · ')
}

// newestAge is how long ago the newest snapshot was taken: "9m ago", or "just now".
export function newestAge(widgets, nowMs) {
  let at = 0
  for (const w of widgets) {
    const t = Date.parse(w.capturedAt || '')
    if (isFinite(t) && t > at) at = t
  }
  return at ? ageWords(nowMs - at) : ''
}

export function ageWords(ms) {
  const a = ago(ms)
  if (!a) return ''
  return a === 'now' ? 'just now' : a + ' ago'
}

// heading is a bold title with dim words after it, on one row.
export function heading(k, key, title, tail) {
  const { Text } = k.els
  return k.row([Text({ key: key + '-t', bold: true, children: [title] }), tail ? Text({ key: key + '-d', dimColor: true, children: [tail] }) : null], key, 0)
}

// widgetCards draws widgets as cards (one column, two on a wide desktop). The
// selected one's title carries `▸`; a press on a title selects it.
//   o: { prefix, selected, onSelect(i), nowMs, snapshot, two }
export function widgetCards(k, io, widgets, o) {
  const cards = widgets.map((w, i) => card(k, w, i, o))
  if (o.two) return [k.els.Box({ key: o.prefix + '-grid', flexDirection: 'row', flexWrap: 'wrap', columnGap: 2, rowGap: 1, children: cards })]
  return [k.col(cards, o.prefix + '-list', 1)]
}

// card is one widget: its title (a press selects it), then its body.
function card(k, w, i, o) {
  const key = o.prefix + '-' + (w.id || i)
  const chosen = i === o.selected
  const inner = o.two ? Math.max(20, Math.floor(k.width / 2) - 4) : k.width
  const when = o.snapshot && w.capturedAt ? ageWords(o.nowMs - Date.parse(w.capturedAt)) : ''
  const head = k.row(
    [
      k.els.Button({ key: key + '-t', label: (chosen ? '▸ ' : '') + clipText(w.title || w.type || 'widget', inner - 4), plain: true, onPress: () => o.onSelect(i) }),
      w.tone && w.tone !== 'neutral' ? k.pill(w.tone, w.tone, key + '-p') : null,
      when ? k.text('snapshot ' + when, { key: key + '-sn', dimColor: true }) : null,
    ],
    key + '-head',
    1,
  )
  const body = widgetBody(k, w, key, o.two ? 300 : 560, inner)
  const props = { key, flexDirection: 'column', children: [head, ...body].filter(Boolean) }
  if (k.rich) {
    props.borderStyle = 'round'
    props.paddingX = 1
    if (o.two) props.width = '48%'
  }
  return k.els.Box(props)
}

// ---------------------------------------------------------------------------
// Selection.

// shownWidgets are the widgets of the dashboard shown.
function shownWidgets() {
  const a = wall.dashboard === 'shared' ? lp.answer : pp.answer
  if (!a || !a.ok) return []
  const n = drawnN[wall.dashboard === 'shared' ? 'shared' : 'person']
  const list = visibleWidgets(a)
  return typeof n === 'number' ? list.slice(0, Math.max(1, n)) : list
}

// How many widgets the last draw of each dashboard showed (the terminal fits them to the pane).
const drawnN = {}

// fitRows is the widgets that fit the terminal pane with the footer and keys row still in view:
// cards are measured, and the tab stops before one that would push them off. At least one is
// always drawn; the rest read "Showing N of M widgets". Off the terminal the pane scrolls as a
// page does, so the cap alone applies.
function fitRows(k, widgets, o) {
  if (!k.terminal || !consoleState.bodyRows || widgets.length < 2) {
    drawnN[o.kind] = undefined
    return widgets
  }
  // Rows the tab spends on everything but cards: the switcher, header, selector, heading, the
  // unavailable list, the 'Showing' line, the blank rows between those blocks, footer and keys.
  const budget = Math.max(8, consoleState.bodyRows - 18 - (o.gone ? o.gone + 2 : 0))
  const out = []
  let used = 0
  for (let i = 0; i < widgets.length; i++) {
    const h = estRows(card(k, widgets[i], i, { prefix: o.prefix, selected: -1, onSelect() {}, nowMs: 0, snapshot: !!o.snapshot, two: false }), k.width, true) + 1
    if (out.length >= 1 && used + h > budget) break
    out.push(widgets[i])
    used += h
  }
  drawnN[o.kind] = out.length
  return out
}

// The tab draws at most this many widgets (spec §4.2, round 6): a wall of eight is two screens of
// cards and pushed the keys row and footer off the pane. The rest read "Showing 6 of 8 widgets".
export const WALL_CAP = 6
let cap = WALL_CAP
// setWallCap is for tests that draw every widget type at once; no caller in the mod uses it.
export function setWallCap(n = WALL_CAP) {
  cap = n
}

// visibleWidgets are the answer's widgets the tab draws: the first WALL_CAP, with the topology
// graph kept in view when a `/landfall topology` asked for one that fell past the cap.
export function visibleWidgets(a) {
  const all = (a && a.widgets) || []
  if (all.length <= cap) return all
  const out = all.slice(0, cap)
  if (wall.topology) {
    const g = graphOf(a)
    if (g && !out.includes(g)) out[cap - 1] = g
  }
  return out
}

function clampSel(list) {
  return Math.max(0, Math.min(wall.selected, list.length - 1))
}

function selectWidget(io, i) {
  wall.selected = i
  wall.topology = false
  io.invalidate()
}

function selectNext(io) {
  const n = shownWidgets().length
  if (n > 0) wall.selected = (clampSel(shownWidgets()) + 1) % n
  wall.topology = false
  io.invalidate()
}

// ownerWords says whose dashboard the shown widget is on, for the question.
function ownerWords() {
  if (wall.dashboard === 'shared') return ''
  if (wall.dashboard === 'mine') return ' on your dashboard'
  const m = memberOf(wall.dashboard, memberList(currentRoom(), lp.answer))
  return m ? ' on ' + m.name + "'s dashboard" : ''
}

async function askAbout(io, w, owner) {
  const r = currentRoom()
  const where = r ? r.displayId || roomName(r) : 'the war room'
  await io.fill('Tell me about the ' + (w.title || w.type) + ' widget' + (owner || '') + ' in ' + where + '.')
}

// ---------------------------------------------------------------------------
// News and the live reads.

// onSnapshot: a widget landed on the wall. Told as a toast (FR-L4), and an
// open wall reads again right away (FR-L2: within 2 s of the event).
export function onSnapshot(io, snap, prev) {
  for (const r of snap.rooms || []) {
    const nw = r.newestWidget
    const seq = nw && typeof nw.seq === 'number' ? nw.seq : null
    const seen = news.seen[r.roomKey]
    if (seen == null) {
      // The first sight of a room is what is already there, not news.
      news.seen[r.roomKey] = seq ?? 0
      continue
    }
    if (seq == null || seq <= seen) continue
    news.seen[r.roomKey] = seq
    news.pending = { title: nw.title || nw.type || 'a widget', by: nw.by || '' }
  }
  if (news.pending) void announce(io)
  if (!lp.open || !reading('wall')) return
  const r = wallRoom(snap)
  const ws = widgetSeqOf(r)
  if (movedRoom(r) || (ws != null && ws > lp.seq)) void loadWall(io)
  if (personDue(r) && !pp.inFlight && r && typeof r.maxSeq === 'number' && r.maxSeq > pp.seq) void loadPerson(io)
}

export function start(io) {}

// tick: the open wall reads again every 15 s, and the age under it moves; a
// new-widget toast held by the gap is told once the gap has run.
export async function tick(io, nowMs) {
  if (news.pending) await announce(io, nowMs)
  if (news.hintUntil && nowMs >= news.hintUntil) {
    news.hintUntil = 0
    io.invalidate()
  }
  // The console closed: the wall stops reading.
  if ((lp.open || pp.open) && !reading('wall')) {
    closed(lp)
    closed(pp)
  }
  if (!lp.open || !reading('wall')) return
  io.invalidate()
  const r = wallRoom(room.snapshot)
  const ws = widgetSeqOf(r)
  if (due(lp, nowMs, REFRESH_MS) || ((movedRoom(r) || (ws != null && ws > lp.seq)) && !lp.inFlight)) void loadWall(io)
  if (personDue(r) && (due(pp, nowMs, REFRESH_MS) || (!pp.inFlight && r && typeof r.maxSeq === 'number' && r.maxSeq > pp.seq))) void loadPerson(io)
}

// personDue: a person's dashboard is the one shown, so it is the one read.
function personDue(r) {
  return pp.open && consoleState.tab === 'wall' && wall.dashboard !== 'shared' && !!readTarget(wall.dashboard, r)
}

// announce shows the waiting new-widget toast, unless one was shown within
// the gap; then the next tick tells it.
async function announce(io, nowMs) {
  const now = nowMs ?? (await ioNow(io))
  if (!news.pending) return
  if (news.toastAt != null && now - news.toastAt < NEWS_GAP_MS) return
  const w = news.pending
  news.pending = null
  news.toastAt = now
  news.hintUntil = now + HINT_MS
  news.hintTitle = w.title
  io.toast(newWidgetWords(w), 6000)
  io.invalidate()
}

// newWidgetWords is the toast: "New on the wall: 5xx by target group · by bob".
export function newWidgetWords(w) {
  return 'New on the wall: ' + w.title + (w.by ? ' · by ' + w.by : '')
}

// wallHint is what the band offers for a minute after a new widget: the
// widget's title, or null.
export function wallHint(nowMs) {
  if (!news.hintUntil || nowMs >= news.hintUntil) return null
  return { title: news.hintTitle }
}

// openWall is the `w` key and the new-widget offer: the person asked, so the console takes the keyboard.
export async function openWall(io) {
  news.hintUntil = 0
  await openConsole(io, 'wall', null, { focus: true })
}

async function ioNow(io, p = lp) {
  try {
    return Number(await io.now())
  } catch {
    return p.triedAt
  }
}

// wallRoom is the room the wall reads: the room this session is in now
// (core.js currentOf), never simply the stream's first.
function wallRoom(snap) {
  return currentOf(snap)
}

// movedRoom: the session is in another room than the one the wall last read
// (its agent joined a new room), so an open wall reads that one.
function movedRoom(r) {
  return !!(r && wall.roomKey && r.roomKey !== wall.roomKey)
}

// loadWall runs `landfall wall` for the wall's room, one read at a time; a
// widget event seen mid-read reads once more after it.
async function loadWall(io) {
  const fetch = () => {
    const r = wallRoom(room.snapshot)
    wall.roomKey = r ? r.roomKey : ''
    const ws = widgetSeqOf(r)
    lp.seq = ws == null ? -1 : ws
    io.invalidate()
    return io.run(wallArgs(r), { timeoutMs: 30000 })
  }
  await readLive(lp, fetch, () => ioNow(io, lp))
  const n = lp.answer && lp.answer.ok ? (lp.answer.widgets || []).length : 0
  if (wall.dashboard === 'shared' && wall.selected >= n) wall.selected = 0
  io.invalidate()
}

// loadPerson runs `landfall wall --person` for the dashboard shown.
async function loadPerson(io) {
  const fetch = () => {
    const r = wallRoom(room.snapshot)
    const who = readTarget(wall.dashboard, r)
    pp.seq = r && typeof r.maxSeq === 'number' ? r.maxSeq : -1
    io.invalidate()
    return who ? io.run(wallArgs(r, who), { timeoutMs: 30000 }) : { ok: false, error: 'Nobody to read.' }
  }
  await readLive(pp, fetch, () => ioNow(io, pp))
  const n = pp.answer && pp.answer.ok ? (pp.answer.widgets || []).length : 0
  if (wall.dashboard !== 'shared' && wall.selected >= n) wall.selected = 0
  io.invalidate()
}

// graphOf is the wall's first graph widget.
export function graphOf(a) {
  if (!a || !a.ok) return null
  const graphs = (a.widgets || []).filter((w) => w.type === 'graph')
  return graphs.find((w) => !w.empty && (w.nodes || []).length > 0) || graphs[0] || null
}

// topologyText is the topology as text, where no pane can be placed.
export function topologyText(a) {
  if (!a) return 'The wall could not be read.'
  if (!a.ok) return a.error || 'The wall could not be read.'
  const g = graphOf(a)
  if (!g) return NO_GRAPH
  return [g.title || 'Topology', ...graphLines(g.nodes, g.edges).map((l) => '  ' + l)].join('\n')
}

// wallArgs is `landfall wall` for a room, and for one person's dashboard (a humanActorId or `me`).
export function wallArgs(r, person) {
  const args = ['wall', '--host', HOST]
  if (r && r.roomKey) args.push('--room', r.roomKey)
  if (person) args.push('--person', person)
  return args
}

// widgetBody draws one widget by type; a type this mod does not know is its
// title alone.
export function widgetBody(k, w, key, px, cells) {
  const { Text } = k.els
  if (w.empty) return [k.text(EMPTY, { key: key + '-empty', dimColor: true })]
  switch (w.type) {
    case 'stat': {
      const value = Text({ key: key + '-v', bold: true, color: toneColor(w.tone || 'neutral'), children: [String(w.value ?? 'no value')] })
      const unit = w.unit ? Text({ key: key + '-u', dimColor: true, children: [String(w.unit)] }) : null
      const delta = deltaText(w)
      // A change is a label only when the widget carries a tone, in that tone; a rise nobody toned is
      // dim text, as Home draws it (round 2 issue 6).
      const toned = w.tone && w.tone !== 'neutral'
      const deltaEl = delta ? (toned ? k.pill(delta, deltaTone(w), key + '-d') : Text({ key: key + '-d', dimColor: true, children: [delta] })) : null
      const out = [k.row([k.row([value, unit], key + '-vu', 0), deltaEl], key + '-vr', 2)]
      if (w.baselineLabel) out.push(k.text(baselineWords(w.baselineLabel), { key: key + '-b', dimColor: true }))
      out.push(k.spark(w.spark, { key: key + '-s', tone: toned ? w.tone : 'neutral', width: Math.min(40, cells), px, height: 34, label: (w.title || 'stat') + ' trend' }))
      return out
    }
    case 'geo': {
      const pts = w.points || []
      if (pts.length === 0) return [k.text('No places in this window.', { key: key + '-none', dimColor: true })]
      // A place is its name plain and its share as a label, as the load balancer row draws it.
      const out = pts.slice(0, 8).map((p, i) =>
        k.row(
          [
            Text({ key: 'gp', children: [clipText(geoName(p), Math.max(10, cells - 16))] }),
            typeof p.value === 'number'
              ? p.tone && p.tone !== 'neutral'
                ? k.pill(withUnit(p.value, p.unit), p.tone, 'gv')
                : Text({ key: 'gv', bold: true, children: [withUnit(p.value, p.unit)] })
              : null,
          ],
          key + '-g' + i,
          1,
        ),
      )
      if (pts.length > 8) out.push(k.text(pts.length - 8 + ' more places', { key: key + '-gmore', dimColor: true }))
      return out
    }
    case 'codeFinding': {
      const where = [w.repo, w.path].filter(Boolean).join(' · ')
      const out = [k.text(clipText(where || 'code', cells), { key: key + '-cf', bold: true })]
      const lines = (w.lines || []).slice(0, 8)
      const start = typeof w.startLine === 'number' ? w.startLine : 1
      const numW = String(start + Math.max(0, lines.length - 1)).length
      lines.forEach((l, i) => {
        out.push(
          k.row(
            [Text({ key: 'ln', dimColor: true, children: [String(start + i).padStart(numW)] }), Text({ key: 'lc', children: [clipText(String(l), Math.max(10, cells - numW - 2))] })],
            key + '-c' + i,
            1,
          ),
        )
      })
      if ((w.lines || []).length > 8) out.push(k.text((w.lines.length - 8) + ' more lines', { key: key + '-cmore', dimColor: true }))
      if (w.permalink && k.els.Link) out.push(k.els.Link({ key: key + '-link', href: w.permalink, label: 'open on GitHub' }))
      return out
    }
    case 'chart': {
      const series = (w.series || []).filter((s) => values(s).length > 0)
      if (series.length === 0) return [k.text('No points in this window.', { key: key + '-none', dimColor: true })]
      if (k.rich) {
        const h = 90
        return [
          k.svg(chartSvg(series, w.markers, w.threshold, { width: px, height: h, tone: w.tone }), { key: key + '-svg', alt: chartAlt(w.title, series, w.markers, w.threshold), width: px, height: h, hover: true }),
          caption(k, w, key),
        ]
      }
      const out = []
      const cols = Math.min(60, Math.max(10, cells - 2))
      series.slice(0, 4).forEach((s, i) => {
        const v = values(s)
        const tone = seriesTone(i, w.tone, Math.min(series.length, 4))
        out.push(k.text(clipText((s.label || 'series ' + (i + 1)) + '  last ' + fmt(v[v.length - 1]) + '  peak ' + fmt(Math.max(...v)), cells), { key: key + '-l' + i, dimColor: true }))
        const over = typeof w.threshold === 'number' ? (x) => (x >= w.threshold ? 'critical' : tone) : undefined
        out.push(k.spark(v, { key: key + '-s' + i, tone, width: cols, toneFor: over, label: s.label }))
        if (i === 0) {
          markerCaption(s, w.markers, Math.min(cols, v.length), cells).forEach((line, j) => out.push(k.text(line, { key: key + '-m' + j, dimColor: true })))
        }
      })
      if (series.length > 4) out.push(k.text(series.length - 4 + ' more series', { key: key + '-more', dimColor: true }))
      if (typeof w.threshold === 'number') out.push(k.text('threshold ' + fmt(w.threshold), { key: key + '-thr', dimColor: true }))
      return out
    }
    case 'logView': {
      const lines = (w.lines || []).slice(-8)
      if (lines.length === 0) return [k.text('No log lines in this window.', { key: key + '-none', dimColor: true })]
      return lines.map((l, i) => {
        if (!hasLevel(l)) {
          const p = proseLine(l.text)
          return k.text(clipText(p.text, cells), { key: key + '-ln' + i, bold: p.heading })
        }
        return k.row(
          [
            Text({ key: 'lv', bold: true, color: toneColor(levelTone(l.level)), children: [levelTag(l.level)] }),
            Text({ key: 'lt', children: [clipText(l.text, Math.max(10, cells - 4))] }),
          ],
          key + '-ln' + i,
          1,
        )
      })
    }
    case 'table': {
      const lines = tableRows(w.columns, w.rows, cells)
      if (lines.length === 0) return [k.text('No rows.', { key: key + '-none', dimColor: true })]
      return lines.map((l, i) => k.text(l, { key: key + '-r' + i, bold: i === 0, dimColor: i > 0 && i === lines.length - 1 && /more rows$/.test(l) }))
    }
    case 'events': {
      const evs = (w.events || []).slice(-8)
      if (evs.length === 0) return [k.text('No events in this window.', { key: key + '-none', dimColor: true })]
      return evs.map((ev, i) => {
        const tone = outcomeTone(ev.outcome)
        const props = { key: key + '-e' + i, children: [clipText(eventLine(ev), cells)] }
        if (tone !== 'neutral') props.color = toneColor(tone)
        return Text(props)
      })
    }
    case 'timeline': {
      const items = (w.items || []).slice(-8)
      if (items.length === 0) return [k.text('Nothing on this timeline yet.', { key: key + '-none', dimColor: true })]
      return items.map((it, i) =>
        k.row(
          [
            Text({ key: 'ta', dimColor: true, children: [hhmm(it.at)] }),
            Text({ key: 'tg', color: toneColor(it.tone || 'neutral'), children: ['●'] }),
            Text({ key: 'tl', children: [clipText(it.label, Math.max(10, cells - 9))] }),
          ],
          key + '-i' + i,
          1,
        ),
      )
    }
    case 'graph':
      return graphView(k, w, key)
    default:
      return [k.text((w.type ? w.type + ' widgets' : 'This widget') + ' draw in the web app.', { key: key + '-x', dimColor: true })]
  }
}

export const EMPTY = 'No data in this window yet.'

// deltaText is the stat's change pill, as the web app's StatBody words it:
// the CLI's deltaLabel verbatim, else an arrow by trend and the size.
export function deltaText(w) {
  if (w.deltaLabel) return String(w.deltaLabel)
  if (typeof w.delta !== 'number') return ''
  const trend = w.trend || (w.delta > 0 ? 'up' : w.delta < 0 ? 'down' : 'flat')
  const arrow = trend === 'up' ? '↑ ' : trend === 'down' ? '↓ ' : ''
  return arrow + fmt(Math.abs(w.delta))
}

// deltaTone follows StatBody: a toned stat colors its change; a neutral one
// reads up as bad and down as a warning.
export function deltaTone(w) {
  const tone = w.tone || 'neutral'
  if (tone === 'neutral') {
    const trend = w.trend || (typeof w.delta === 'number' ? (w.delta > 0 ? 'up' : w.delta < 0 ? 'down' : 'flat') : '')
    return trend === 'up' ? 'critical' : trend === 'down' ? 'warning' : 'neutral'
  }
  if (tone === 'good') return 'good'
  if (tone === 'warning' || tone === 'serious') return 'warning'
  return 'critical'
}

// baselineWords is the stat's comparison line, as the CLI sends it (`6 an hour ago`: the value the
// stat had, then when), said without joining a count to a humanized phrase: `6 · 1h ago`, `6 · 6m
// ago`, `as of 2h ago` (round 2 issue 7). A line that does not have that shape is said as it came.
export function baselineWords(label) {
  const text = String(label ?? '').trim()
  const m = /^(?:(.*?)\s+)?(?:(an?|\d+)\s+(second|minute|hour|day)s?\s+ago)$/i.exec(text)
  if (!m) return text
  const n = /^an?$/i.test(m[2]) ? 1 : Number(m[2])
  const unit = { second: 's', minute: 'm', hour: 'h', day: 'd' }[m[3].toLowerCase()]
  const age = n + unit + ' ago'
  return m[1] ? m[1] + ' · ' + age : 'as of ' + age
}

// withUnit is a value and its unit: "34%", "120 ms".
function withUnit(v, unit) {
  if (!unit) return fmt(v)
  return fmt(v) + (unit === '%' ? '' : ' ') + unit
}

// geoName is a place as a person reads it: "us-east-1 · N. Virginia". A
// label that already starts with the place ("eu-west-1 · 13.1%", the server's
// own label for a critical place) is said once, not "eu-west-1 · eu-west-1 · …".
export function geoName(p) {
  const place = String(p.place || '')
  const label = String(p.label || '')
  if (!label || label === place) return place || '?'
  if (!place || label.startsWith(place)) return label
  return place + ' · ' + label
}

// moreWords says the wall holds more widgets than were read.
export function moreWords(a, cap = Infinity) {
  const drawn = Math.min((a.widgets || []).length, cap)
  const shown = drawn + (a.unavailable || []).length
  const total = Math.max(a.totalWidgets || 0, (a.widgets || []).length + (a.unavailable || []).length)
  if (!total || total <= shown) return ''
  return 'Showing ' + shown + ' of ' + total + ' widgets. Open the war room in the browser for the rest.'
}

function caption(k, w, key) {
  const parts = (w.markers || []).map((m) => m.label + ' at ' + hhmm(m.atMs) + 'Z')
  if (typeof w.threshold === 'number') parts.push('threshold ' + fmt(w.threshold))
  if (parts.length === 0) return null
  return k.text(parts.join(' · '), { key: key + '-cap', dimColor: true })
}


// wallText is /wall's answer where no pane can be drawn.
export function wallText(a, r) {
  if (!a) return 'The wall could not be read.'
  if (!a.ok) return a.error || 'The wall could not be read.'
  const out = [(a.sharedBy ? 'Wall · shared by ' + a.sharedBy : 'Wall') + (r ? ' · ' + roomName(r) : '') + (a.windowMs ? ' · ' + windowWords(a.windowMs) : '')]
  const widgets = a.widgets || []
  if (widgets.length === 0) out.push('Nothing is on the wall yet.')
  for (const w of widgets) {
    out.push('')
    out.push(w.title || w.type)
    for (const line of widgetLines(w)) out.push('  ' + line)
  }
  const gone = a.unavailable || []
  if (gone.length > 0) {
    out.push('')
    out.push('Not shown here:')
    for (const u of gone) out.push('  ' + unavailableWords(u))
  }
  const more = moreWords(a)
  if (more) {
    out.push('')
    out.push(more)
  }
  return out.join('\n')
}

function widgetLines(w) {
  if (w.empty) return [EMPTY]
  switch (w.type) {
    case 'stat': {
      const out = [[String(w.value ?? 'no value') + (w.unit || ''), deltaText(w)].filter(Boolean).join(' ')]
      if (w.baselineLabel) out.push(baselineWords(w.baselineLabel))
      return out
    }
    case 'geo':
      return (w.points || []).slice(0, 8).map((p) => geoName(p) + (typeof p.value === 'number' ? ' ' + withUnit(p.value, p.unit) : '') + (p.tone && p.tone !== 'neutral' ? ' (' + p.tone + ')' : ''))
    case 'codeFinding': {
      const start = typeof w.startLine === 'number' ? w.startLine : 1
      return [[w.repo, w.path].filter(Boolean).join(' · '), ...(w.lines || []).slice(0, 8).map((l, i) => String(start + i) + '  ' + l), ...(w.permalink ? [w.permalink] : [])]
    }
    case 'chart': {
      const out = []
      for (const s of w.series || []) {
        const v = values(s)
        if (v.length) out.push((s.label || 'series') + ': last ' + fmt(v[v.length - 1]) + ', peak ' + fmt(Math.max(...v)))
      }
      for (const m of w.markers || []) out.push('marker: ' + m.label + ' at ' + hhmm(m.atMs) + 'Z')
      if (typeof w.threshold === 'number') out.push('threshold ' + fmt(w.threshold))
      return out.length ? out : ['No points in this window.']
    }
    case 'logView':
      return (w.lines || []).slice(-8).map((l) => (hasLevel(l) ? levelTag(l.level) + ' ' + l.text : proseLine(l.text).text))
    case 'table':
      return tableRows(w.columns, w.rows, 100)
    case 'events':
      return (w.events || []).slice(-8).map(eventLine)
    case 'timeline':
      return (w.items || []).slice(-8).map((it) => hhmm(it.at) + ' ' + it.label)
    case 'graph':
      return graphLines(w.nodes, w.edges)
    default:
      return [(w.type ? w.type + ' widgets' : 'This widget') + ' draw in the web app.']
  }
}

// A log line with no level is prose, not a debug line: Beacon's status and
// remediation cards are logView widgets whose lines carry only a message.
// They draw as plain text, and a markdown heading ("## FAST FIX") as bold
// text without its marks.
function hasLevel(l) {
  return String(l?.level ?? '').trim() !== ''
}

function proseLine(text) {
  const m = /^\s{0,3}#{1,6}\s+(.*)$/.exec(String(text ?? ''))
  return m ? { text: m[1].trim(), heading: true } : { text: String(text ?? ''), heading: false }
}

