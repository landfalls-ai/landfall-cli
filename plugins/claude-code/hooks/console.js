// The Landfall console (spec §2, §3, §8): one command, `/landfall`, and one pane, id `landfall`.
//
// THE PANE. A segmented switcher pinned as the pane's first row, then the room header, the
// active tab's body, the footer (`live · updated 4s ago`) and the keys row. Only the switcher is
// pinned: the engine's window moves over the whole tree, so the console takes the scroll itself
// (a `ui.scroll` hook answering without `next`), keeps its own offset into the body's rows and
// draws the switcher first and the body from that offset. `ui.focus` keeps the focus ring's
// element in view and in `consoleState.focus`, which every tab reads (the incident `b` acts on,
// the artifact Context previews).
//
// THE TABS. Each component exports `tab(k, io, nowMs, args)`, `badge()` and `warm(io)`, called
// here by name. The console draws every footer and keys row itself, so a tab is always asked for
// its body alone (`args.chrome === false`) and answers either the body's rows (an array) or
// `{ rows, keys, live, footer, refresh }`: `keys` are the tab's own letters (drawn first in the
// keys row, before `r: refresh` and `close (esc)`), `live` its live.js read for the footer (or
// `footer`, `{ text, tone }` or an element of its own), `refresh` what `r` runs (warm(io) when
// absent). Context answers rows and exports footer(), keys() and refresh(), wired below.
// Home is drawn here (§3) from the wall, load balancer and timeline reads, which every
// component behind them exposes as `homeFeed()` ({ lp, data }).
//
// THE ENTRY FLOW (§2.6). `/landfall` alone lands on Home in a room; otherwise it asks
// `landfall whoami --json` and lands on Incidents, drawn as the picker, or as the sign-in state
// (signin.js) when there is no sign-in.
//
// Nothing here starts a turn: keys draft prompts or run the `landfall` CLI, never $.mcp.call.
// `$` never leaves this file: tabs get `io`, closures that spell `$` here (consoleIo).

import {
  CONSOLE,
  CONSOLE_COLUMNS,
  consoleTitle,
  HOST,
  TABS,
  addCommand,
  clip,
  consoleState,
  currentRoom,
  openConsole,
  parseAnswer,
  quoteDraft,
  room,
  roomIsOver,
  roomName,
  severityTone,
  statusTone,
  whereIs,
} from './core.js'
import { BUTTON_CHROME, kit, textCells, TONE } from './kit.js'
import { notLive, RECONNECTING, since } from './live.js'
import { fiveXxTone, fmt, healthyTone, hhmm, lastPct, pctCell, toneColor, values, windowWords } from './views.js'
import { AGENT_IN, AGENT_OUT, roomText } from './components/room.js'
import { tab as tabVote, badge as badgeVote, warm as warmVote, author, castVote, positionsText, ringSvg, timeLeft, waiting } from './components/vote.js'
import * as voteNs from './components/vote.js'
import {
  tab as tabContext,
  badge as badgeContext,
  warm as warmContext,
  footer as footerContext,
  keys as keysContext,
  refresh as refreshContext,
  contextText,
} from './components/context.js'
import { tab as tabWall, badge as badgeWall, warm as warmWall, deltaText, deltaTone, geoName } from './components/wall.js'
import * as wallNs from './components/wall.js'
import { tab as tabPeople, badge as badgePeople, warm as warmPeople, latestTone, seqAge, sortPeople, stateWord } from './components/roster.js'
import * as peopleNs from './components/roster.js'
import { tab as tabTimeline, badge as badgeTimeline, warm as warmTimeline, KINDS as TIMELINE_KINDS, timelineArgs } from './components/timeline.js'
import * as timelineNs from './components/timeline.js'
import { tab as tabLb, badge as badgeLb, warm as warmLb, lbArgs, lbText } from './components/lb.js'
import * as lbNs from './components/lb.js'
import { tab as tabIncidents, badge as badgeIncidents, warm as warmIncidents, incidentsText } from './components/incidents.js'
import * as moreNs from './components/more.js'
import { tab as tabMore, badge as badgeMore, warm as warmMore } from './components/more.js'
import { commsText } from './components/comms.js'
import { brainText } from './components/brain.js'
import { claimLine, lineLabel, roomLines } from './components/lines.js'
import { chart, pinChart } from './components/chart.js'
import { sound, switchText } from './components/sound.js'
import { cancelSignin, noteWhoami, signedOut, signin } from './components/signin.js'

export const DESCRIPTION = 'Landfall war room console: home, vote, context, wall, people, timeline, load balancers, incidents, comms, brain'
export const UNKNOWN = 'Landfall tabs: home, vote, context, wall, people, timeline, lb, incidents, more (comms, brain). Also: lines <label>, chart, sound.'
export const NO_ROOM = 'Not in a war room yet. Open Incidents above to join one, or open a share link from the room.'
export const NOT_PLACED = 'Widen the window to see the Landfall console.'

// The full label set needs this many body cells; below it the docked set (§2.2).
export const FULL_CELLS = 98
// Two columns on Home from this many body cells, on the terminal (§3.2, §6).
export const TWO_COLUMNS = 150

// What the last drawing laid out, for the scroll and focus hooks: each body row's estimated
// height and the keys drawn in it, and how many rows the body window shows.
const view = { rows: [], keys: [], visible: 20, openedAt: 0 }

// The commands the console replaced (§1.3): register.js no longer registers them.
export const REMOVED = ['room', 'vote', 'incidents', 'who', 'wall', 'lb', 'timeline', 'topology', 'lines', 'comms', 'brain', 'landfall-sound', 'chart']

export function install(on) {
  addCommand({ name: 'landfall', description: DESCRIPTION, argumentHint: '[tab] [text]', immediate: true })

  on('command.run', { command: 'landfall' }, async ($, e) => {
    const io = consoleIo($, null)
    const asked = parseArgs(e.args)
    if (asked.unknown) return { text: UNKNOWN }
    if (asked.act === 'lines') {
      const got = await claimLine(io, asked.label)
      io.invalidate()
      return { text: got.text }
    }
    if (asked.act === 'chart') {
      if (!chart.ready) return { text: 'No metric read to chart yet. Ask your agent to read a metric from the room, then run /landfall chart.' }
      await pinChart(io)
      return {}
    }
    if (asked.act === 'sound') {
      if (sound.on === undefined) sound.on = await readSoundSwitch($)
      return { text: switchText(sound.on) }
    }
    const tab = asked.tab || (await landing(io))
    if (asked.scrollTo) consoleState.scrollTo = scrollKey(asked.scrollTo)
    const got = await openConsole(io, tab, asked.args, { focus: true })
    if (got && got.isPlaced === false) {
      consoleState.open = false
      await io.toast(NOT_PLACED, 6000)
      return { text: await tabText(io, tab, asked.args) }
    }
    warmShown(io)
    // A successful open leaves no row in the person's transcript (round 2 review, decision 2): the
    // pane opening is the answer, and a row that tells them nothing is read again by the model.
    return {}
  })

  // The person's close (Esc, the mark, ctrl+x x) stops every tab's reads and a sign-in in
  // flight: a sign-in running with nothing showing it is interference.
  on('ui.close', { id: CONSOLE }, async ($, e, next) => {
    consoleState.open = false
    consoleState.closed = true
    consoleState.warm.clear()
    view.openedAt = 0
    cancelSignin(consoleIo($, null), false)
    return next(e)
  })

  // The switcher stays pinned: the console moves its own body, never the engine's window.
  on('ui.scroll', { requestId: CONSOLE }, async ($, e) => {
    const to = clampOffset(consoleState.offset + (Number(e.by) || 0), view.rows, view.visible)
    if (to !== consoleState.offset) {
      consoleState.offset = to
      await $.ui.invalidate('ui.render')
    }
    return {}
  })

  // The focus ring: remembered for the tabs (the incident `b` acts on), and kept in view.
  on('ui.focus', { requestId: CONSOLE }, async ($, e, next) => {
    const key = e.element || ''
    const before = consoleState.offset
    if (key) consoleState.offset = offsetShowing(key, consoleState.offset, view)
    const moved = key !== consoleState.focus
    consoleState.focus = key
    if (moved || before !== consoleState.offset) await $.ui.invalidate('ui.render')
    return next(e)
  })

  on('ui.render', { component: 'Pane', requestId: CONSOLE }, async ($, e) => {
    const k = kit($.ui.resolve(e), e)
    const io = consoleIo($, e.surface)
    // Drawn means open: after a hot reload the engine draws the pane again and the tabs go on
    // reading. A draw after the person's close does not reopen it.
    if (!consoleState.closed) consoleState.open = true
    warmShown(io)
    let nowMs = 0
    try {
      nowMs = Number(await $.clock.now())
    } catch {
      // No clock: ages read as of the last read.
    }
    if (!view.openedAt && nowMs) view.openedAt = nowMs
    return drawConsole(k, io, nowMs, e)
  })
}

export async function band(io, e, k) {
  return null
}

export function onSnapshot(io, snap, prev) {
  // A room arrived after a join from the picker: the header names it from the stream now.
  if (consoleState.joined && currentRoom()) consoleState.joined = null
}

export function start(io) {}

// tick: an open console redraws every 5 s, so ages and the sign-in clock move. The tabs read
// on their own cadence in their own tick.
export function tick(io, nowMs) {
  if (consoleState.open) io.invalidate()
}

// ---------- arguments (§1.2) ----------

const TAB_WORDS = {
  home: 'home',
  overview: 'home',
  vote: 'vote',
  context: 'context',
  wall: 'wall',
  topology: 'wall',
  people: 'people',
  who: 'people',
  lines: 'people',
  timeline: 'timeline',
  lb: 'lb',
  incidents: 'incidents',
  more: 'more',
  comms: 'more',
  brain: 'more',
}

// parseArgs reads `/landfall [tab] [text]`: { tab, args } to open a tab, { act, label } for the
// three words that act, { unknown } for a word it does not know, {} for nothing (the entry flow).
// A tab's args are the words after the tab word as typed ('' for none): `wall mine`, `people
// alice`, `timeline findings`, `lb web-edge-alb`, `brain origin pool`. The short forms say which
// tab and what to show: `comms` and `brain …` open More on that chip (args `comms`,
// `brain origin pool`), `topology` opens the Wall at its graph, `context artifacts` the Context
// tab at its artifacts.
export function parseArgs(raw) {
  const text = String(raw || '').trim()
  if (!text) return {}
  const space = text.search(/\s/)
  const word = (space < 0 ? text : text.slice(0, space)).toLowerCase()
  const rest = space < 0 ? '' : text.slice(space + 1).trim()
  if (word === 'chart' && !rest) return { act: 'chart' }
  if (word === 'sound' && !rest) return { act: 'sound' }
  if (word === 'lines' && rest) return { act: 'lines', label: rest }
  const tab = TAB_WORDS[word]
  if (!tab) return { unknown: true }
  switch (word) {
    case 'topology':
      return { tab, args: '', scrollTo: 'graph' }
    case 'context':
      return rest.toLowerCase() === 'artifacts' ? { tab, args: '', scrollTo: 'artifacts' } : { tab, args: rest }
    case 'who':
    case 'lines':
      return { tab, args: '' }
    case 'comms':
      return { tab, args: 'comms' }
    case 'brain':
      return { tab, args: rest ? 'brain ' + rest : 'brain' }
    case 'timeline': {
      const kind = rest.toLowerCase()
      return { tab, args: TIMELINE_KINDS.some((x) => x.kind === kind) ? kind : '' }
    }
    default:
      return { tab, args: rest }
  }
}

// landing is where `/landfall` alone lands (§2.6): the console's tab in a room (Home, or the tab
// it was last on in this session); otherwise Incidents, the picker or the sign-in state, once
// `landfall whoami --json` has said which.
async function landing(io) {
  if (currentRoom()) {
    if (consoleState.open || consoleState.chosen) return consoleState.tab
    return 'home'
  }
  if (signin.phase !== 'waiting') noteWhoami(await io.run(['whoami', '--json', '--host', HOST], { timeoutMs: 10000 }))
  return 'incidents'
}

async function readSoundSwitch($) {
  try {
    const rows = await $.config.list()
    const row = rows.find((r) => /^landfall(@[^.]*)?\.sound$/.test(r.key))
    return row ? row.value === true : false
  } catch {
    return false
  }
}

// ---------- the tabs, by name ----------

// warmShown starts the reads the open console needs (§2.5): the wall, the load balancers and
// the timeline from the moment it opens (Home draws from them), and the shown tab once.
function warmShown(io) {
  if (!consoleState.open) return
  for (const t of ['wall', 'lb', 'timeline', consoleState.tab]) warmOnce(io, t)
}

function warmOnce(io, t) {
  if (consoleState.warm.has(t)) return
  consoleState.warm.add(t)
  try {
    warmTab(io, t)
  } catch {
    // One tab's read never stops the console.
  }
}

function warmTab(io, t) {
  switch (t) {
    case 'vote':
      return warmVote(io)
    case 'context':
      return warmContext(io)
    case 'wall':
      return warmWall(io)
    case 'people':
      return warmPeople(io)
    case 'timeline':
      return warmTimeline(io)
    case 'lb':
      return warmLb(io)
    case 'incidents':
      return warmIncidents(io)
    case 'more':
      return warmMore(io)
    default:
      return undefined
  }
}

function badgeOf(t) {
  try {
    switch (t) {
      case 'vote':
        return badgeVote()
      case 'context':
        return badgeContext()
      case 'wall':
        return badgeWall()
      case 'people':
        return badgePeople()
      case 'timeline':
        return badgeTimeline()
      case 'lb':
        return badgeLb()
      case 'incidents':
        return badgeIncidents()
      case 'more':
        return badgeMore()
      default:
        return null
    }
  } catch {
    return null
  }
}

// tabOf draws one tab: its body (`tab`, body only), and its optional `keys(k, io)` and
// `footer(k, nowMs)`; the console adds `r: refresh` (the tab's `refresh(io)`, else `warm(io)`)
// and close. Context takes `{ chrome: false }` and its keys and footer also take `io`.
function tabOf(t, k, io, nowMs, args) {
  switch (t) {
    case 'vote':
      return withChrome(tabVote(k, io, nowMs, args), voteNs, k, io, nowMs)
    case 'context':
      return {
        rows: tabContext(k, io, nowMs, { chrome: false, args }),
        keys: keysContext(k, io, nowMs),
        footer: footerContext(k, io, nowMs),
        refresh: () => refreshContext(io),
      }
    case 'wall':
      return withChrome(tabWall(k, io, nowMs, args), wallNs, k, io, nowMs)
    case 'people':
      return withChrome(tabPeople(k, io, nowMs, args), peopleNs, k, io, nowMs)
    case 'timeline':
      return withChrome(tabTimeline(k, io, nowMs, args), timelineNs, k, io, nowMs)
    case 'lb':
      return withChrome(tabLb(k, io, nowMs, args), lbNs, k, io, nowMs)
    case 'incidents':
      return tabIncidents(k, io, nowMs, args)
    case 'more':
      return withChrome(tabMore(k, io, nowMs, args), moreNs, k, io, nowMs)
    default:
      return []
  }
}

// withChrome takes a tab's body and its component's optional keys(k, io), footer(k, nowMs) and
// refresh(io). A tab that answers the object form already said them.
function withChrome(got, ns, k, io, nowMs) {
  if (got && !Array.isArray(got)) return got
  let keys = []
  let footer = null
  try {
    if (typeof ns.keys === 'function') keys = ns.keys(k, io) || []
  } catch {
    keys = []
  }
  try {
    if (typeof ns.footer === 'function') footer = ns.footer(k, nowMs) || null
  } catch {
    footer = null
  }
  const refresh = typeof ns.refresh === 'function' ? () => ns.refresh(io) : null
  return { rows: got || [], keys, footer, refresh }
}

// scrollKey is the element `/landfall topology` and `/landfall context artifacts` bring into view.
function scrollKey(what) {
  if (what === 'graph') {
    try {
      return typeof wallNs.graphKey === 'function' ? wallNs.graphKey() || 'wall' : 'wall'
    } catch {
      return 'wall'
    }
  }
  if (what === 'artifacts') return 'cx-arts'
  return what
}

// wallFeed is the shared wall Home draws its tiles from: wall.js's sharedAnswer() (the last good
// shared wall), with its live read when the wall exposes one (homeFeed).
function wallFeed() {
  const f = homeFeedOf(wallNs)
  if (f) return f
  try {
    const a = typeof wallNs.sharedAnswer === 'function' ? wallNs.sharedAnswer() : null
    return { lp: null, data: a, readSince: view.openedAt }
  } catch {
    return null
  }
}

// lbFeed and timelineFeed are Home's load balancer and Latest blocks: the tab's last good answer
// and its live read (lb.js answerOf/readState, timeline.js latestRows/readState).
function lbFeed() {
  try {
    if (typeof lbNs.answerOf !== 'function') return homeFeedOf(lbNs)
    return { lp: typeof lbNs.readState === 'function' ? lbNs.readState() : null, data: lbNs.answerOf() }
  } catch {
    return null
  }
}

function timelineFeed() {
  try {
    if (typeof timelineNs.latestRows !== 'function') return homeFeedOf(timelineNs)
    return { lp: typeof timelineNs.readState === 'function' ? timelineNs.readState() : null, data: timelineNs.latestRows(3) }
  } catch {
    return null
  }
}

// stillReading: a warm source that has not answered yet. A source with no live read of its own
// (the shared wall) counts as reading for its first 30 s, then as quiet.
function stillReading(f, nowMs) {
  if (!f) return false
  if (f.lp) return !f.lp.answer
  return !f.data && view.openedAt > 0 && nowMs - view.openedAt < 30000
}

// homeFeedOf is a component's `homeFeed()` ({ lp, data }), or null from one that has none yet.
function homeFeedOf(ns) {
  try {
    const f = typeof ns.homeFeed === 'function' ? ns.homeFeed() : null
    return f && f.lp ? f : null
  } catch {
    return null
  }
}

// ---------- the switcher (§2.2) ----------

export const LABELS = { home: 'Home', vote: 'Vote', context: 'Context', wall: 'Wall', people: 'People', timeline: 'Timeline', lb: 'Load balancers', incidents: 'Incidents', more: 'More' }

// segmentLabels is the switcher's labels for a width (chosen from the width alone, so the row
// never flips as counts come and go): the full set from 98 cells, else `LB` and no Incidents
// count; an inactive count drops only when a two-digit one would overflow the row.
export function segmentLabels(tabs, active, counts, cells, rich = false) {
  if (rich) return richSegmentLabels(tabs, active, counts, cells)
  const full = cells >= FULL_CELLS
  const name = (t) => (t === 'lb' && !full ? 'LB' : LABELS[t])
  const countOf = (t) => {
    const n = counts[t]
    if (t === 'incidents' && !full) return null
    return typeof n === 'number' && n > 0 ? n : null
  }
  const width = (withCounts) =>
    tabs.reduce((w, t) => {
      const n = countOf(t)
      return w + name(t).length + 2 + (n != null && (withCounts || t === active) ? 1 + String(n).length : 0)
    }, 0) + Math.max(0, tabs.length - 1) + 2
  const keep = width(true) <= cells
  return tabs.map((t) => {
    const n = countOf(t)
    const shown = n != null && (keep || t === active)
    return { id: t, label: name(t) + (shown ? ' ' + n : ''), active: t === active }
  })
}

// ---- the switcher off the terminal: measured, not counted ----
// A native button is drawn in a proportional font with its own padding, so a label is not
// `length + 2` cells there (round 1: "Incidents" clipped to "Incide", "More" off the edge at 50
// cells). The width of a segment is estimated from the glyphs (calibrated on the Claude desktop
// app's Code tab: 1.3 cells of button chrome plus 1.8 cells per em of text), and the label set is
// the first rung of a ladder whose row fits the pane's body columns (round 2 review, decision 1):
//   - never an invented word: a label is the full word or the word cut to its start;
//   - the active segment keeps its fill and its `▸` at every rung;
//   - counts go first: below the full set the row draws Vote's count and no other, and Vote's
//     count is reserved (one digit) in every fit, so the row never flips as a vote comes and goes;
//   - the estimate under-reads the real pane by about 4 percent (SWITCHER_SLACK, the capture where
//     the fully shortened row filled all 613 px), so a rung fits when its estimate times that fits.
// The full word is named in the pane's title (`Landfall · People`). The row never wraps.
// segmentCells is one segment's estimated width in cells; the filled active segment is a Box
// with a cell of padding each side, a little wider than a button's chrome.
export function segmentCells(label, active) {
  return (active ? 2 : BUTTON_CHROME) + textCells(label) * (active ? 1.06 : 1)
}
export const SWITCHER_SLACK = 1.04
const NAMES_DOCKED = { ...LABELS, lb: 'LB' }
const NAMES_B = { ...NAMES_DOCKED, context: 'Ctx' }
const NAMES_C = { ...NAMES_B, timeline: 'Time' }
const NAMES_D = { ...NAMES_C, incidents: 'Inc' }
const NAMES_E = { ...NAMES_D, people: 'Ppl' }
// The rung before A is the roomy one: full names and every count.
export const RUNGS = [
  { id: 'full', names: LABELS, all: true },
  { id: 'A', names: NAMES_DOCKED },
  { id: 'B', names: NAMES_B },
  { id: 'C', names: NAMES_C },
  { id: 'D', names: NAMES_D },
  { id: 'E', names: NAMES_E },
]
function richSegmentLabels(tabs, active, counts, cells) {
  const build = (r, reserve) =>
    tabs.map((t) => {
      const n = counts[t]
      let shown = typeof n === 'number' && n > 0 && (r.all || t === 'vote')
      const count = shown ? ' ' + n : reserve && t === 'vote' ? ' 1' : ''
      return { id: t, label: r.names[t] + count, active: t === active, shown }
    })
  const wide = (items) => items.reduce((w, s) => w + segmentCells(s.active ? '▸ ' + s.label : s.label, s.active), 0) * SWITCHER_SLACK
  let rung = RUNGS[RUNGS.length - 1]
  for (const r of RUNGS) {
    if (wide(build(r, true)) <= cells) {
      rung = r
      break
    }
  }
  return build(rung, false).map(({ shown, ...item }) => item)
}

function switcher(k, io, e) {
  const tabs = k.mobile ? ['home', 'vote'] : TABS
  const counts = {}
  for (const t of tabs) counts[t] = badgeOf(t)
  const cells = Number((e.props && e.props.bodyColumns) || k.width + 2)
  const items = segmentLabels(tabs, consoleState.tab, counts, cells, k.rich).map((s) => ({ ...s, onPress: () => showTab(io, s.id) }))
  return k.segments(items, { key: 'seg' })
}

// showTab switches the body; the switcher stays where it is.
export function showTab(io, t) {
  if (!TABS.includes(t)) return
  const changed = t !== consoleState.tab
  if (changed) consoleState.offset = 0
  consoleState.tab = t
  consoleState.args = null
  consoleState.chosen = true
  consoleState.focus = ''
  warmOnce(io, t)
  // The switcher may show a cut word, so the pane's title names the tab in full. Opening an open id
  // only retitles it, and without `focus` it never takes the keyboard.
  if (changed) {
    try {
      void Promise.resolve(io.open(CONSOLE, consoleTitle(t), { columns: CONSOLE_COLUMNS, closeOnEscape: true })).catch(() => {})
    } catch {
      // The title is a courtesy: a pane that cannot be retitled still switches.
    }
  }
  io.invalidate()
}

// ---------- the pane ----------

function drawConsole(k, io, nowMs, e) {
  const { Box } = k.els
  blankText = k.els.Text
  const r = currentRoom()
  const t = TABS.includes(consoleState.tab) ? consoleState.tab : 'home'
  const body = []
  // The pane's rows, for a tab that fits its body to them (the terminal's Wall and Home).
  consoleState.bodyRows = Number((e.props && e.props.scroll && e.props.scroll.bodyRows) || 0)
  let got
  if (t === 'home') got = drawHome(k, io, nowMs, e, r)
  else {
    try {
      got = normalize(tabOf(t, k, io, nowMs, consoleState.args))
    } catch (err) {
      got = normalize([k.text(clip('This tab could not be drawn: ' + String(err), 200), { key: 'tab-err' })])
    }
  }
  // A short terminal pane (Home drops its own blank rows first) keeps the room's row and the keys.
  if (k.terminal && !got.compact) body.push(blank('b-top'))
  body.push(...header(k, r))
  if (k.terminal && !got.compact) body.push(blank('b-hdr'))
  body.push(...got.rows.filter(Boolean))
  const foot = got.footer || (got.live ? footerWords(got.live, nowMs, r) : null)
  if (foot) {
    if (k.terminal) body.push(blank('b-foot'))
    body.push(foot.type ? foot : footerRow(k, foot))
  } else if (k.terminal && !got.compact && got.rows.some(Boolean)) {
    // No footer to sit the keys on: one blank row before them, never two (round 2 review, issue 16).
    body.push(blank('b-keys'))
  }
  body.push(...keysRows(k, io, t, got))

  const rows = k.terminal ? flatten(body) : body
  const bodyRows = Number((e.props && e.props.scroll && e.props.scroll.bodyRows) || 0)
  const width = Math.max(20, Number((e.props && e.props.bodyColumns) || 80))
  view.rows = rows.map((x) => estRows(x, width, k.terminal))
  view.keys = rows.map((x) => keysIn(x))
  view.visible = Math.max(1, (bodyRows || 40) - 1)
  if (consoleState.scrollTo) {
    const i = view.keys.findIndex((set) => [...set].some((key) => key === consoleState.scrollTo || key.startsWith(consoleState.scrollTo)))
    if (i >= 0) consoleState.offset = i
    consoleState.scrollTo = ''
  }
  consoleState.offset = clampOffset(consoleState.offset, view.rows, view.visible)
  const shown = rows.slice(consoleState.offset)
  return Box({
    flexDirection: 'column',
    rowGap: k.terminal ? 0 : 1,
    children: [switcher(k, io, e), Box({ key: 'body', flexDirection: 'column', rowGap: k.terminal ? 0 : 1, children: shown })],
  })
}

// normalize takes either form a tab answers.
function normalize(got) {
  if (Array.isArray(got)) return { rows: got, keys: [], live: null, footer: null, refresh: null }
  if (!got || typeof got !== 'object') return { rows: [], keys: [], live: null, footer: null, refresh: null }
  return { rows: got.rows || [], keys: got.keys || [], live: got.live || null, footer: got.footer || null, refresh: got.refresh || null, noRefresh: !!got.noRefresh, compact: !!got.compact }
}

// blank is one empty row between blocks (the terminal's spacing; cards space themselves).
let blankText = null
function blank(key) {
  return blankText ? blankText({ key, children: [' '] }) : ' '
}

// header is the room's row (§2.2): mark, name, severity, status, the agent word; a second row
// while the room reconnects. Off the terminal two rows, with who is here at the right.
function header(k, r) {
  const { Box, Text } = k.els
  if (!r) {
    const joined = consoleState.joined
    const words = joined ? 'reading the room…' : signedOut() ? 'not signed in' : 'not in a war room'
    // Off the terminal the pane's title already says Landfall and the mark carries the name, so the
    // row reads only its dim words (round 2 review, issue 20); the terminal draws the pane's own frame.
    const name = joined ? joined.name : k.terminal ? 'Landfall' : ''
    return [
      k.row(
        [k.mark('hdr-m', 20), name ? Text({ key: 'hdr-n', bold: true, children: [name] }) : null, Text({ key: 'hdr-d', dimColor: true, children: [words] })].filter(Boolean),
        'hdr',
        k.terminal ? 2 : 1,
      ),
    ]
  }
  const st = r.status || {}
  const labels = []
  if (st.severity) labels.push(k.pill(st.severity, severityTone(st.severity), 'hdr-sev'))
  if (st.status) labels.push(k.pill(st.status, statusTone(st.status), 'hdr-st'))
  const a = r.agent
  if (a && typeof a.inRoom === 'boolean') labels.push(a.inRoom ? k.pill(AGENT_IN, 'good', 'hdr-agent') : Text({ key: 'hdr-agent', dimColor: true, children: [AGENT_OUT] }))
  const out = []
  if (k.terminal) {
    out.push(k.row([Text({ key: 'hdr-n', bold: true, children: ['◆ ' + roomName(r)] }), ...labels], 'hdr', 2))
  } else {
    const people = (st.people || []).filter((p) => p.here)
    const title = k.row([k.mark('hdr-m', 20), Text({ key: 'hdr-n', bold: true, children: [roomName(r)] })], 'hdr-t', 1)
    out.push(Box({ key: 'hdr', flexDirection: 'row', justifyContent: 'space-between', flexWrap: 'wrap', columnGap: 2, children: [title, people.length ? k.avatars(people, { key: 'hdr-avs', max: 5 }) : null].filter(Boolean) }))
    if (labels.length) out.push(k.row(labels, 'hdr-l', 1))
  }
  if (notLive(r)) out.push(Text({ key: 'hdr-conn', color: TONE.warning, children: [RECONNECTING] }))
  return out
}

// ---------- footer and keys (§4.9) ----------

// footerWords is the footer for one read: null before its first good read.
export function footerWords(lp, nowMs, r, name) {
  if (!lp || !lp.answer || !lp.answer.ok) return null
  const age = since(Math.max(0, nowMs - lp.goodAt))
  if (notLive(r)) return { text: 'updated ' + age + ' ago', tone: 'dim' }
  if (lp.stale) return { text: 'stale · ' + (name ? name + ' · ' : '') + 'updated ' + age + ' ago · ' + lp.stale, tone: 'warning' }
  return { text: 'live · updated ' + age + ' ago' + (lp.inFlight ? ' · reading…' : ''), tone: 'dim' }
}

function footerRow(k, foot) {
  if (foot.tone === 'warning') return k.text(foot.text, { key: 'foot', color: TONE.warning })
  return k.text(foot.text, { key: 'foot', dimColor: true })
}

// keysRows is the keys row: the tab's letters, then `r: refresh`, then `close (esc)`; it breaks
// once, before `r: refresh`, when it would not fit (§4.9).
function keysRows(k, io, t, got) {
  const own = got.keys.filter(Boolean)
  const refresh = got.refresh || (t === 'home' ? () => refreshHome(io) : () => warmTab(io, t))
  // A state with nothing to read again (signed out, waiting for the browser) draws no `r: refresh`.
  const tail = [
    got.noRefresh ? null : k.button({ key: 'refresh', label: k.terminal ? 'refresh' : 'Refresh', hotkey: 'r', onPress: async () => void (await refresh()) }),
    k.button({ key: 'close', label: k.terminal ? 'close (esc)' : 'Close', dim: true, dismiss: true, onPress: () => closeConsole(io) }),
  ].filter(Boolean)
  if (own.length === 0) return [k.row(tail, 'keys')]
  const cells = [...own, ...tail].reduce((w, b) => w + keyCells(b) + 2, 0) - 2
  if (cells <= k.width + 2) return [k.row([...own, ...tail], 'keys')]
  return [k.row(own, 'keys-own'), k.row(tail, 'keys')]
}

function keyCells(el) {
  const p = (el && el.props) || {}
  return String(p.label || '').length + (p.hotkey ? 3 : 0)
}

async function closeConsole(io) {
  consoleState.open = false
  consoleState.closed = true
  consoleState.warm.clear()
  cancelSignin(io, false)
  await io.close(CONSOLE)
}

// ---------- Home (§3) ----------

const BLOCK_ORDER = ['vote', 'beacon', 'people', 'wall', 'lb', 'latest']
const DROPPABLE = ['latest', 'lb', 'wall'] // dropped first to last
const DROP_NAMES = { wall: 'Wall', lb: 'Load balancers', latest: 'Timeline' }

// planHome decides which blocks Home draws in `avail` rows (§3 "When space is short"): blocks
// are dropped from the bottom (Latest, then Load balancers, then Wall), then People shrinks to
// the people who are here. `blocks` maps an id to { rows, rowsHere? } for the blocks that have
// something to say; `fixed` is every other row the body takes (header, footer, keys, the
// reading line); `columns` 2 lays Vote, Beacon, People and Latest left, Wall and LB right.
// Answers { shown: ids in order, dropped: ids, hereOnly, moreLine }.
export function planHome(blocks, avail, fixed, columns = 1) {
  const present = BLOCK_ORDER.filter((id) => blocks[id])
  let shown = [...present]
  let hereOnly = false
  const height = (ids, extra) => {
    const rowsOf = (id) => (id === 'people' && hereOnly && blocks.people.rowsHere != null ? blocks.people.rowsHere : blocks[id].rows)
    const stack = (list) => list.reduce((n, id) => n + rowsOf(id), 0) + Math.max(0, list.length - 1)
    if (columns === 2) {
      const left = ids.filter((id) => !['wall', 'lb'].includes(id))
      const right = ids.filter((id) => ['wall', 'lb'].includes(id))
      return fixed + Math.max(stack(left), stack(right)) + extra
    }
    return fixed + stack(ids) + extra
  }
  const moreRow = (ids) => (present.some((id) => !ids.includes(id)) ? 2 : 0)
  for (const id of DROPPABLE) {
    if (height(shown, moreRow(shown)) <= avail) break
    shown = shown.filter((x) => x !== id)
  }
  if (height(shown, moreRow(shown)) > avail && blocks.people && blocks.people.rowsHere != null) hereOnly = true
  const dropped = present.filter((id) => !shown.includes(id))
  return { shown, dropped, hereOnly, moreLine: moreWords(dropped) }
}

// moreWords names the tabs whose blocks were dropped: "More in Wall, Load balancers and Timeline."
export function moreWords(dropped) {
  const names = ['wall', 'lb', 'latest'].filter((id) => dropped.includes(id)).map((id) => DROP_NAMES[id])
  if (names.length === 0) return ''
  if (names.length === 1) return 'More in ' + names[0] + '.'
  return 'More in ' + names.slice(0, -1).join(', ') + ' and ' + names[names.length - 1] + '.'
}

// readingWords names the warm sources still in flight: "Reading the wall, load balancers and
// timeline…", "Reading the timeline…".
export function readingWords(ids) {
  const words = { wall: ['the wall', 'wall'], lb: ['load balancers', 'load balancers'], timeline: ['the timeline', 'timeline'] }
  const list = ['wall', 'lb', 'timeline'].filter((id) => ids.includes(id))
  if (list.length === 0) return ''
  if (list.length === 1) return 'Reading ' + words[list[0]][0] + '…'
  const parts = list.map((id, i) => (i === 0 ? words[id][0] : words[id][1]))
  return 'Reading ' + parts.slice(0, -1).join(', ') + ' and ' + parts[parts.length - 1] + '…'
}

async function refreshHome(io) {
  for (const t of ['wall', 'lb', 'timeline']) {
    consoleState.warm.add(t)
    try {
      warmTab(io, t)
    } catch {
      // One read failing never stops the others.
    }
  }
}

function drawHome(k, io, nowMs, e, r) {
  const { Text } = k.els
  if (!r) {
    return { rows: [Text({ key: 'home-none', children: [NO_ROOM] })], keys: [], live: null, footer: null, refresh: null }
  }
  const feeds = { wall: wallFeed(), lb: lbFeed(), timeline: timelineFeed() }
  const cells = Number((e.props && e.props.bodyColumns) || k.width + 2)
  const two = k.terminal ? cells >= TWO_COLUMNS : k.width >= 118
  const st = r.status || {}

  const blocks = {}
  const draw = {}
  const v = waiting(r)
  if (v.length > 0) {
    blocks.vote = { rows: k.terminal ? 4 : 6 }
    draw.vote = () => voteBlock(k, io, r, v)
  }
  const beacon = beaconWords(st)
  if (beacon) {
    const w = two ? 70 : k.width
    blocks.beacon = { rows: beacon.done ? Math.min(3, Math.ceil(beacon.text.length / Math.max(20, w))) : 1 }
    draw.beacon = () => beaconBlock(k, beacon, w)
  }
  const people = sortPeople(st.people || [])
  if (people.length > 0) {
    const here = people.filter((p) => p.here)
    const shownN = Math.min(6, people.length)
    blocks.people = { rows: 1 + shownN + (people.length > shownN ? 1 : 0), rowsHere: 1 + Math.min(6, Math.max(1, here.length)), total: people.length, hereTotal: here.length }
    draw.people = (hereOnly) => peopleBlock(k, io, r, hereOnly ? here : people, two ? 70 : k.width)
  }
  const tiles = wallTiles(feeds.wall, two || (!k.terminal && k.width >= 118) ? 6 : k.terminal ? 3 : 4)
  if (tiles.length > 0) {
    blocks.wall = { rows: 1 + tiles.length }
    draw.wall = () => wallBlock(k, feeds.wall.data, tiles)
  }
  const lbs = lbRows(feeds.lb)
  if (lbs.length > 0) {
    blocks.lb = { rows: 1 + lbs.length }
    draw.lb = () => lbBlock(k, feeds.lb.data, lbs)
  }
  const latest = latestEvents(feeds.timeline)
  if (latest.length > 0) {
    blocks.latest = { rows: 1 + latest.length }
    draw.latest = () => latestBlock(k, latest)
  }

  const reading = ['wall', 'lb', 'timeline'].filter((id) => stillReading(feeds[id], nowMs))
  const readLine = readingWords(reading)
  const resolved = roomIsOver(r)
  // The header (2 to 3 rows with its blank lines), the footer, the keys and the reading line.
  const fixed = 3 + (notLive(r) ? 1 : 0) + (resolved ? 1 : 0) + 3 + (readLine ? 2 : 0)
  const bodyRows = Number((e.props && e.props.scroll && e.props.scroll.bodyRows) || 0)
  const avail = k.terminal && bodyRows > 0 ? bodyRows - 1 : Infinity
  const plan = planHome(blocks, avail, fixed, two ? 2 : 1)
  // Still too tall on a short pane: the Vote, Beacon and People blocks are never dropped before
  // everything cheaper is (round 2 review, issue 8), so shrink in this order: the blank rows
  // around the room's row, the Beacon line, People down to as many rows as fit, the footer.
  const squeeze = k.terminal && !two ? squeezeHome(plan, blocks, avail, fixed, !!footerNow(feeds, nowMs, r)) : {}

  const rows = []
  if (resolved) rows.push(Text({ key: 'home-resolved', dimColor: true, children: ['This incident is resolved.'] }))
  const peopleRows = () => {
    let list = draw.people(plan.hereOnly)
    if (squeeze.peopleN != null) {
      const here = plan.hereOnly ? sortPeople(st.people || []).filter((p) => p.here) : sortPeople(st.people || [])
      list = peopleBlock(k, io, r, here.slice(0, squeeze.peopleN), k.width)
      const left = here.length - Math.min(here.length, squeeze.peopleN)
      if (left > 0) list.push(Text({ key: 'hp-more', dimColor: true, children: ['+' + left + ' more'] }))
    }
    return squeeze.noHeading ? list.slice(1) : list
  }
  const drawn = (id) => (id === 'people' ? peopleRows() : draw[id]())
  const stack = (ids, key) => {
    const out = []
    ids.filter((id) => !(id === 'beacon' && squeeze.noBeacon)).forEach((id, i) => {
      if (i > 0 && k.terminal && !squeeze.tight) out.push(blank(key + '-gap-' + id))
      out.push(...drawn(id))
    })
    return out
  }
  if (two) {
    const left = plan.shown.filter((id) => !['wall', 'lb'].includes(id))
    const right = plan.shown.filter((id) => ['wall', 'lb'].includes(id))
    rows.push(
      k.els.Box({
        key: 'home-cols',
        flexDirection: 'row',
        columnGap: 4,
        children: [
          k.els.Box({ key: 'home-left', flexDirection: 'column', rowGap: k.terminal ? 0 : 1, width: k.terminal ? 70 : '48%', children: stack(left, 'l') }),
          right.length ? k.els.Box({ key: 'home-right', flexDirection: 'column', rowGap: k.terminal ? 0 : 1, flexShrink: 1, children: stack(right, 'r') }) : null,
        ].filter(Boolean),
      }),
    )
  } else rows.push(...stack(plan.shown, 'h'))
  if (plan.moreLine) {
    if (k.terminal && !squeeze.tight) rows.push(blank('home-more-gap'))
    rows.push(Text({ key: 'home-more', dimColor: true, children: [plan.moreLine] }))
  }
  if (readLine) {
    if (k.terminal) rows.push(blank('home-read-gap'))
    rows.push(Text({ key: 'home-reading', dimColor: true, children: [readLine] }))
  }
  return { rows, keys: [], live: null, footer: squeeze.noFooter ? null : homeFooter(feeds, nowMs, r), refresh: () => refreshHome(io), compact: !!squeeze.compact }
}

// footerNow is whether Home has a footer to draw (it is two rows when it does).
function footerNow(feeds, nowMs, r) {
  return homeFooter(feeds, nowMs, r)
}

// squeezeHome decides what else a short terminal pane gives up once planHome has dropped what it
// can, by exact rows. `plan` is planHome's answer; `fixed` its count of the header's three rows,
// the footer's two and the keys' one (plus the room being over, the room reconnecting and the
// reading line). Answers { compact, noBeacon, peopleN, noHeading, noFooter } for what it took, {}
// when nothing more was needed. The last two rows are always the more line and the keys.
export function squeezeHome(plan, blocks, avail, fixed, hasFooter) {
  if (!isFinite(avail)) return {}
  const more = plan.moreLine ? 2 : 0
  const pb = blocks.people
  const total = pb ? (plan.hereOnly ? pb.hereTotal : pb.total) : 0
  const peopleRows = (o) => {
    if (o.peopleN == null) return plan.hereOnly && pb.rowsHere != null ? pb.rowsHere : pb.rows
    return (o.noHeading ? 0 : 1) + o.peopleN + (total > o.peopleN ? 1 : 0)
  }
  const height = (o) => {
    const list = plan.shown.filter((id) => !(id === 'beacon' && o.noBeacon))
    const gaps = o.tight ? 0 : Math.max(0, list.length - 1)
    const stack = list.reduce((n, id) => n + (id === 'people' ? peopleRows(o) : blocks[id].rows), 0) + gaps
    return fixed - (o.compact ? 2 : 0) - (o.noFooter ? 2 : 0) + stack + more - (o.tight && more ? 1 : 0)
  }
  if (height({}) <= avail) return {}
  let o = { compact: true }
  if (height(o) <= avail) return o
  o = { compact: true, noBeacon: true }
  if (height(o) <= avail || !pb) return o
  // The footer (two rows) goes before any person does: who is here matters more than how old the read is.
  if (hasFooter) {
    o = { compact: true, noBeacon: true, noFooter: true }
    if (height(o) <= avail) return o
  }
  // Then the blank rows between blocks: everyone who is here reads before the rhythm does.
  o = { ...o, tight: true }
  if (height(o) <= avail) return o
  const have = Math.min(total, 6)
  for (let n = have; n >= 1; n--) {
    const t = { ...o, peopleN: n }
    if (height(t) <= avail) return t
  }
  return { ...o, peopleN: 1, noHeading: true }
}

// homeFooter reports the oldest of Home's warm sources, and names one that is stale (§4.9).
function homeFooter(feeds, nowMs, r) {
  const names = { wall: 'wall', lb: 'load balancers', timeline: 'timeline' }
  const live = Object.entries(feeds).filter(([, f]) => f && f.lp && f.lp.answer && f.lp.answer.ok)
  if (live.length === 0) return null
  const stale = live.find(([, f]) => f.lp.stale)
  if (stale) return footerWords(stale[1].lp, nowMs, r, names[stale[0]])
  const oldest = live.reduce((a, b) => (b[1].lp.goodAt < a[1].lp.goodAt ? b : a))
  return footerWords(oldest[1].lp, nowMs, r)
}

function voteBlock(k, io, r, list) {
  const { Text } = k.els
  const v = list[0]
  const left = timeLeft(v)
  const more = list.length > 1 ? '· ' + (list.length - 1) + ' more waiting' : ''
  const head = k.row(
    [Text({ key: 'hv-h', bold: true, children: ['Your vote is waiting'] }), left ? k.pill(left, 'warning', 'hv-t') : null, more ? Text({ key: 'hv-m', dimColor: true, children: [more] }) : null],
    'hv-head',
    k.terminal ? 2 : 1,
  )
  const later = typeof voteNs.setAside === 'function' ? () => voteNs.setAside(io, r) : null
  const keys = k.row(
    [
      k.button({ key: 'hv-c', label: k.terminal ? 'corroborate' : 'Corroborate', hotkey: 'c', primary: true, onPress: () => castVote(io, r.roomKey, v, 'corroborate') }),
      k.button({ key: 'hv-x', label: k.terminal ? 'contest…' : 'Contest…', hotkey: 'x', onPress: () => openConsole(io, 'vote', 'contest', { focus: true }) }),
      later ? k.button({ key: 'hv-l', label: k.terminal ? 'later' : 'Later', hotkey: 'l', dim: true, onPress: later }) : null,
    ],
    'hv-keys',
    k.terminal ? 2 : 1,
  )
  if (k.terminal) {
    return [
      head,
      Text({ key: 'hv-s', children: [clip(author(v) + ': “' + v.statement + '”', k.width)] }),
      Text({ key: 'hv-p', dimColor: true, children: [positionsText(v)] }),
      keys,
    ]
  }
  const n = Math.max(0, v.positionsSoFar ?? 0)
  const total = Math.max(1, v.needed ?? n + (v.shortfall ?? 1))
  return [
    k.card(
      [
        head,
        k.dim(author(v) + ' · staged #' + v.claimSeq, 'hv-by'),
        k.quote('“' + v.statement + '”', 'hv-s'),
        k.row([k.svg(ringSvg(n, total), { key: 'hv-ring', alt: n + ' of ' + total + ' positions', width: 20, height: 20 }), k.dim(positionsText(v), 'hv-p')], 'hv-pos', 1),
        keys,
      ],
      { key: 'hv-card', tone: 'warning' },
    ),
  ]
}

function beaconWords(st) {
  if (st.beaconStep && st.beaconStep.text) return { done: false, text: 'Beacon step ' + (st.beaconStep.step ?? '?') + ' · ' + st.beaconStep.text }
  if (st.beaconConclusion && st.beaconConclusion.text) return { done: true, text: 'Beacon concluded · ' + st.beaconConclusion.text }
  return null
}

function beaconBlock(k, b, width) {
  const { Text } = k.els
  if (k.terminal) {
    const text = b.done ? clip('◆ ' + b.text, Math.max(20, width) * 3) : clip('◆ ' + b.text, width)
    return [Text({ key: 'hb', color: TONE.violet, children: [text] })]
  }
  return [k.row([k.mark('hb-m'), Text({ key: 'hb', color: TONE.violet, children: [b.done ? clip(b.text, 300) : b.text] })], 'hb-row', 1)]
}

// lineOf is the line a person holds, from the watch stream's lines.
function lineOf(r, p) {
  const l = roomLines(r).find((x) => (p.you && x.you) || (x.owner && x.owner === p.name))
  return l ? lineLabel(l) : ''
}

function peopleBlock(k, io, r, list, width) {
  const { Box, Text } = k.els
  const all = sortPeople((r.status && r.status.people) || [])
  const hereN = all.filter((p) => p.here).length
  const shown = list.slice(0, 6)
  const away = list.length - shown.length
  const heading = Text({ key: 'hp-h', bold: true, children: ['Here · ' + hereN] })
  if (k.terminal) {
    const third = Math.max(10, width - 40)
    const tableRows = shown.map((p, i) => {
      const nameCell = Box({
        key: 'hp-n' + i,
        flexDirection: 'row',
        children: [
          Text({ key: 'd', ...(p.here ? { color: TONE.good } : { dimColor: true }), children: [p.here ? '● ' : '○ '] }),
          Text({ key: 'n', bold: true, dimColor: !p.here, wrap: 'truncate-end', children: [p.name] }),
          p.you ? Text({ key: 'y', dimColor: !p.here, children: [' (you)'] }) : null,
        ].filter(Boolean),
      })
      const line = lineOf(r, p)
      const where = [whereIs(p) || (p.here ? '' : 'away'), line ? 'on ' + line : ''].filter(Boolean).join(' · ')
      const whereCell = Text({ key: 'hp-w' + i, dimColor: !p.here, wrap: 'truncate-end', children: [clip(where, 26)] })
      return [nameCell, whereCell, latestCell(k, p, i, third)]
    })
    const out = [heading, k.table(tableRows, { key: 'hp-t', widths: [13, 27] })]
    if (away > 0) out.push(Text({ key: 'hp-away', dimColor: true, children: ['+' + away + ' away'] }))
    return out
  }
  const kids = [heading]
  shown.forEach((p, i) => {
    const line = lineOf(r, p)
    const where = [whereIs(p) || (p.here ? '' : 'away'), line ? 'on ' + line : ''].filter(Boolean).join(' · ')
    kids.push(k.row([k.avatar(p.name, !!p.here, { key: 'hp-av' + i, you: !!p.you }), Text({ key: 'hp-n' + i, bold: true, children: [p.name + (p.you ? ' (you)' : '')] }), where ? k.dim(where, 'hp-w' + i) : null], 'hp-r' + i, 1))
    const l = p.latest
    if (l && l.text) {
      kids.push(
        k.row(
          [
            k.pill(stateWord(l.state), latestTone(l.state), 'hp-s' + i),
            k.dim(seqAge(l), 'hp-a' + i),
            k.button({ key: 'hp-q' + i, label: '“' + clip(l.text, 40) + '”', onPress: () => io.fill(quoteDraft(l.text)) }),
          ],
          'hp-l' + i,
          1,
        ),
      )
    } else kids.push(k.dim('nothing shared yet', 'hp-none' + i))
  })
  if (away > 0) kids.push(k.dim('+' + away + ' away', 'hp-away'))
  return [k.card(kids, { key: 'hp-card' })]
}

function latestCell(k, p, i, width) {
  const { Box, Text } = k.els
  const l = p.latest
  if (!l || !l.text) return Text({ key: 'hp-l' + i, dimColor: true, children: ['nothing shared yet'] })
  const word = stateWord(l.state)
  const tail = ' ' + seqAge(l)
  const room = width - (2 + word.length) - tail.length - 1
  const kids = [k.pill(word, latestTone(l.state), 'p'), Text({ key: 'a', dimColor: true, children: [tail] })]
  if (room >= 12) kids.push(Text({ key: 'q', children: [' “' + clip(l.text, room - 3) + '”'] }))
  return Box({ key: 'hp-l' + i, flexDirection: 'row', children: kids })
}

// wallTiles is Home's tiles: every stat first, then the first chart, then a geo; a widget with
// no data is not a tile.
export function wallTiles(feed, max) {
  const a = feed && feed.data
  if (!a || !a.ok) return []
  const widgets = (a.widgets || []).filter((w) => w && !w.empty)
  const stats = widgets.filter((w) => w.type === 'stat')
  const chartW = widgets.find((w) => w.type === 'chart' && (w.series || []).some((s) => values(s).length > 0))
  const geo = widgets.find((w) => w.type === 'geo' && (w.points || []).length > 0)
  return [...stats, chartW, geo].filter(Boolean).slice(0, max)
}

function tileOf(w) {
  const toned = w.tone && w.tone !== 'neutral'
  if (w.type === 'stat') {
    const value = String(w.value ?? '') + (w.unit && w.unit !== String(w.value ?? '').slice(-w.unit.length) ? (w.unit === '%' || w.unit.startsWith('/') ? '' : ' ') + w.unit : '')
    return { title: w.title || 'stat', tone: toned ? w.tone : '', value, delta: deltaText(w), deltaTone: toned ? deltaTone(w) : '', spark: w.spark || [] }
  }
  if (w.type === 'chart') {
    const s = (w.series || []).find((x) => values(x).length > 0)
    const v = values(s)
    return { title: w.title || 'chart', tone: toned ? w.tone : '', value: fmt(v[v.length - 1]), delta: 'peak ' + fmt(Math.max(...v)), deltaTone: '', spark: v }
  }
  const pts = w.points || []
  const place = (p) => (p ? geoName(p) + (typeof p.value === 'number' ? ' ' + fmt(p.value) + (p.unit || '') : '') : '')
  return { title: w.title || 'places', tone: toned ? w.tone : '', value: place(pts[0]), delta: place(pts[1]), deltaTone: '', spark: [] }
}

function wallBlock(k, a, tiles) {
  const { Text, Box } = k.els
  const head = k.row(
    [Text({ key: 'hw-h', bold: true, children: [a.sharedBy ? 'Wall · shared by ' + a.sharedBy : 'Wall'] }), a.windowMs ? Text({ key: 'hw-d', dimColor: true, children: ['· ' + windowWords(a.windowMs)] }) : null],
    'hw-head',
    1,
  )
  if (k.terminal) {
    const rows = tiles.map((w, i) => {
      const t = tileOf(w)
      return [
        Text({ key: 'hw-t' + i, wrap: 'truncate-end', children: [clip(t.title, 20)] }),
        t.tone ? k.pill(t.tone, t.tone, 'hw-p' + i) : Text({ key: 'hw-p' + i, children: [' '] }),
        Text({ key: 'hw-v' + i, wrap: 'truncate-end', children: [clip(t.value, 15)] }),
        t.delta ? (t.deltaTone ? k.pill(clip(t.delta, 12), t.deltaTone, 'hw-d' + i) : Text({ key: 'hw-d' + i, dimColor: true, wrap: 'truncate-end', children: [clip(t.delta, 14)] })) : Text({ key: 'hw-d' + i, children: [' '] }),
        k.spark(t.spark, { key: 'hw-s' + i, width: 18, tone: t.tone || 'neutral', label: t.title }) || Text({ key: 'hw-s' + i, children: [' '] }),
      ]
    })
    return [head, k.table(rows, { key: 'hw-tiles', widths: [21, 12, 16, 15] })]
  }
  // Two tiles across need a docked pane of 90 cells or more (spec §6); narrower, one tile a row.
  const wide = k.width + 2 >= 90
  const cards = tiles.map((w, i) => {
    const t = tileOf(w)
    return Box({
      key: 'hw-c' + i,
      flexDirection: 'column',
      borderStyle: 'round',
      borderColor: TONE.neutral,
      paddingX: 1,
      width: wide ? '48%' : '100%',
      children: [
        // The title left and its tone label right, on one row.
        Box({
          key: 'hw-h' + i,
          flexDirection: 'row',
          justifyContent: 'space-between',
          alignItems: 'center',
          columnGap: 1,
          children: [Text({ key: 'hw-t', bold: true, children: [t.title] }), t.tone ? k.pill(t.tone, t.tone, 'hw-p') : null].filter(Boolean),
        }),
        k.row([Text({ key: 'hw-v', bold: true, children: [t.value] }), t.delta ? (t.deltaTone ? k.pill(t.delta, t.deltaTone, 'hw-d') : k.dim(t.delta, 'hw-d')) : null], 'hw-vr' + i, 1),
        // A plain image: transparent on any theme (an interactive frame is white on a dark page).
        k.spark(t.spark, { key: 'hw-s' + i, px: 260, height: 44, tone: t.tone || 'neutral', label: t.title }),
      ].filter(Boolean),
    })
  })
  return [head, Box({ key: 'hw-grid', flexDirection: 'row', flexWrap: 'wrap', columnGap: 2, rowGap: 1, children: cards })]
}

function lbRows(feed) {
  const a = feed && feed.data
  if (!a || !a.ok) return []
  return (a.loadBalancers || []).filter(Boolean)
}

function lbBlock(k, a, lbs) {
  const { Text } = k.els
  const rows = [Text({ key: 'hl-h', bold: true, children: ['Load balancers'] })]
  lbs.forEach((one, i) => {
    const kids = [Text({ key: 'n', children: [one.name || 'load balancer'] }), k.pill('healthy ' + (one.healthy ?? 0) + ' of ' + (one.total ?? 0), healthyTone(one.healthy ?? 0, one.total ?? 0), 'h')]
    if (!a.metricsUnavailable) {
      ;(one.targetGroups || []).forEach((tg, j) => {
        const pct = lastPct(tg)
        if (pct == null) return
        kids.push(k.row([Text({ key: 'g', children: [tg.name || 'target group'] }), k.pill(fmt(pct) + '%', fiveXxTone(pct) || 'good', 'p')], 'hl-g' + i + '-' + j, 1))
      })
    }
    rows.push(k.row(kids, 'hl-r' + i, k.terminal ? 3 : 1))
    if (!k.terminal && !a.metricsUnavailable) {
      ;(one.targetGroups || []).forEach((tg, j) => {
        const grid = [(tg.fiveXxPct || []).map(pctCell)]
        if (grid[0].length) rows.push(k.heat(grid, { key: 'hl-heat' + i + '-' + j, cell: 10, label: '5xx per minute, ' + (tg.name || 'target group') }))
      })
    }
  })
  return k.terminal ? rows : [k.card(rows, { key: 'hl-card' })]
}

function latestEvents(feed) {
  const list = feed && Array.isArray(feed.data) ? feed.data : []
  return list
    .filter(Boolean)
    .slice()
    .sort((a, b) => (b.seq ?? 0) - (a.seq ?? 0))
    .slice(0, 3)
}

function latestBlock(k, events) {
  const { Text } = k.els
  const rows = [Text({ key: 'ht-h', bold: true, children: ['Latest'] })]
  events.forEach((ev, i) => {
    const text = String(ev.text || '')
    const words = ev.who && !text.includes(ev.who) ? text + ' · ' + ev.who : text
    rows.push(
      k.row(
        [
          Text({ key: 't', dimColor: true, children: [hhmm(ev.at)] }),
          Text({ key: 'g', ...(ev.tone && ev.tone !== 'neutral' ? { color: toneColor(ev.tone) } : { dimColor: true }), children: [ev.glyph || '·'] }),
          Text({ key: 'x', wrap: 'truncate-end', children: [clip(words, Math.max(10, k.width - 9))] }),
        ],
        'ht-r' + i,
        1,
      ),
    )
  })
  return k.terminal ? rows : [k.card(rows, { key: 'ht-card' })]
}

// ---------- no pane: the tab's text (§1.2) ----------

async function tabText(io, t, args) {
  const r = currentRoom()
  const none = 'This folder is not in a war room. Open a share link from the room, or run /landfall incidents to join one.'
  try {
    switch (t) {
      case 'home':
        return r ? roomText() : none
      case 'vote':
        if (typeof voteNs.text === 'function') return await voteNs.text(io, args || '')
        return r ? votesText(r) : none
      case 'context':
        return r ? await contextText(io) : none
      case 'wall':
        return await wallNs.text(io, args || '')
      case 'people':
        return await peopleNs.text(io, args || '')
      case 'timeline':
        if (typeof timelineNs.text === 'function') return await timelineNs.text(io, args || '')
        return timelineWords(await io.run(timelineArgs(r, args || 'all'), { timeoutMs: 30000 }), r)
      case 'lb':
        if (typeof lbNs.text === 'function') return await lbNs.text(io, args || '')
        return lbText(await io.run(lbArgs(r, args || ''), { timeoutMs: 30000 }))
      case 'incidents': {
        const a = await io.run(['incidents', '--host', HOST], { timeoutMs: 20000 })
        if (a && !a.ok && signin.signedIn === false) return 'Sign in to Landfall: run landfall login in a terminal.'
        return incidentsText(a)
      }
      case 'more': {
        if (typeof moreNs.text === 'function') return await moreNs.text(io, args || '')
        if (/^brain\b/.test(String(args || ''))) {
          const q = String(args).slice(5).trim()
          const run = ['brain']
          if (r) run.push('--room', r.roomKey)
          if (q) run.push('--q', q)
          return brainText(await io.run([...run, '--host', HOST]), q)
        }
        if (!r) return none
        return commsText(await io.run(['comms', '--room', r.roomKey, '--host', HOST]))
      }
      default:
        return UNKNOWN
    }
  } catch (err) {
    return clip(String(err), 200)
  }
}

function votesText(r) {
  const list = waiting(r)
  if (list.length === 0) return 'No vote is waiting on you.'
  const out = [roomName(r) + ' · ' + (list.length === 1 ? '1 vote waiting on you' : list.length + ' votes waiting on you')]
  for (const v of list) {
    const left = timeLeft(v)
    out.push('#' + v.claimSeq + ' ' + author(v) + ': “' + v.statement + '”')
    out.push('  ' + positionsText(v) + (left ? ' · ' + left : ''))
    if (v.evidence) out.push('  Evidence: ' + v.evidence)
  }
  out.push('Vote as yourself: landfall vote --claim <seq> --position corroborate|contest [--reason "<why>"]')
  return out.join('\n')
}

function timelineWords(a, r) {
  if (!a || !a.ok) return String((a && a.error) || 'The timeline could not be read.')
  const head = 'Timeline' + (r ? ' · ' + roomName(r) : '')
  const events = (a.events || []).slice().sort((x, y) => x.seq - y.seq)
  if (events.length === 0) return head + '\n  Nothing on the timeline yet.'
  const lines = events.map((ev) => {
    const text = String(ev.text || '')
    return '  ' + hhmm(ev.at) + ' ' + (ev.glyph || '·') + ' ' + (ev.who && !text.includes(ev.who) ? text + ' · ' + ev.who : text)
  })
  if (a.hasMore) lines.unshift('  (earlier events not shown)')
  return [head, ...lines].join('\n')
}

// ---------- the body's rows: flatten, estimate, clamp ----------

// flatten opens plain column Boxes (no border, padding, gap, size or fill) into their rows, so
// the console scrolls a row at a time rather than a whole tab at once.
export function flatten(rows) {
  const out = []
  for (const el of rows) {
    if (el && typeof el === 'object' && el.type === 'Box' && isPlainColumn(el.props || {})) out.push(...flatten(el.children || []))
    else if (el) out.push(el)
  }
  return out
}

function isPlainColumn(p) {
  if (p.flexDirection !== 'column') return false
  for (const key of Object.keys(p)) {
    if (key === 'flexDirection' || key === 'key') continue
    if ((key === 'rowGap' || key === 'gap') && !p[key]) continue
    return false
  }
  return true
}

// estRows is an element's height in rows, estimated on the high side, so the body can always be
// scrolled to its end (a little past it at worst, never short of it).
export function estRows(el, width, terminal = true) {
  if (el == null || el === false) return 0
  if (typeof el === 'string' || typeof el === 'number') return Math.max(1, Math.ceil(String(el).length / Math.max(1, width)))
  const p = el.props || {}
  const kids = (el.children || []).filter((c) => c != null && c !== false)
  switch (el.type) {
    case 'Text': {
      if (p.wrap && String(p.wrap).startsWith('truncate')) return 1
      const len = textLen(el)
      return Math.max(1, ...String(textOf(el)).split('\n').map((l) => Math.ceil(l.length / Math.max(1, width)))) + (len === 0 ? 0 : 0)
    }
    case 'Raster':
      return Number(p.rows) || 1
    case 'Svg':
    case 'Image':
      return Math.max(1, Math.ceil((Number(p.height) || 16) / 16))
    case 'Button':
    case 'Input':
    case 'Select':
      return terminal ? 1 : 2
    case 'Box': {
      const frame = (p.borderStyle ? 2 : 0) + 2 * (Number(p.paddingY) || Number(p.padding) || 0) + (Number(p.marginTop) || 0) + (Number(p.marginBottom) || 0)
      const inner = Math.max(1, width - (p.borderStyle ? 2 : 0) - 2 * (Number(p.paddingX) || Number(p.padding) || 0))
      if (p.flexDirection === 'column') {
        const gap = Number(p.rowGap ?? p.gap ?? 0) || 0
        return frame + kids.reduce((n, c) => n + estRows(c, inner, terminal), 0) + gap * Math.max(0, kids.length - 1)
      }
      const tallest = Math.max(1, ...kids.map((c) => estRows(c, inner, terminal)))
      if (p.flexWrap === 'wrap') {
        const cells = kids.reduce((n, c) => n + Math.max(1, textLen(c)) + (Number(p.columnGap ?? p.gap ?? 1) || 1), 0)
        return frame + Math.max(tallest, Math.ceil(cells / inner) * tallest)
      }
      return frame + tallest
    }
    default:
      return 1
  }
}

function textOf(el) {
  if (el == null) return ''
  if (typeof el === 'string' || typeof el === 'number') return String(el)
  const p = el.props || {}
  if (el.type === 'Button') return String(p.label || '') + (p.hotkey ? 'x: ' : '')
  if (el.type === 'Link') return String(p.label || p.href || '')
  return (el.children || []).map(textOf).join('')
}

function textLen(el) {
  return textOf(el).length
}

// keysIn is every `key` drawn in an element, for the focus hook and scroll-to.
function keysIn(el, out = new Set()) {
  if (!el || typeof el !== 'object') return out
  if (el.props && typeof el.props.key === 'string') out.add(el.props.key)
  for (const c of el.children || []) keysIn(c, out)
  return out
}

// clampOffset keeps the offset where the window is still full: from 0 to the last row at which
// the rows below still fill `visible`.
export function clampOffset(offset, rows, visible) {
  let below = 0
  let last = 0
  for (let i = rows.length - 1; i >= 0; i--) {
    below += rows[i]
    if (below >= visible) {
      last = i
      break
    }
  }
  return Math.max(0, Math.min(Math.round(Number(offset) || 0), last))
}

// offsetShowing is the offset that keeps the row holding `key` in the window: unchanged when it
// shows, else the least move that shows it.
export function offsetShowing(key, offset, v) {
  const i = v.keys.findIndex((set) => set.has(key))
  if (i < 0) return offset
  if (i < offset) return i
  let used = 0
  for (let j = offset; j <= i; j++) used += v.rows[j] || 1
  let at = offset
  while (used > v.visible && at < i) {
    used -= v.rows[at] || 1
    at++
  }
  return at
}

// ---------- io ----------

// consoleIo is what the tabs get in place of `$`: closures that each spell `$` here, shaped as
// register.js shapes `io`, with `spawn` for the sign-in's handoff.
function consoleIo($, surface) {
  return {
    surface: surface || 'terminal',
    fill: (text, mode) => $.prompt.fill(mode ? { text, mode } : { text }),
    suggest: (text) => $.prompt.suggest({ text }),
    toast: (text, ms) => $.ui.toast(text, ms ? { timeoutMs: ms } : undefined),
    status: (text) => $.ui.status(text),
    open: (id, title, opts) => $.ui.open({ id, title, ...(opts || {}) }),
    close: (id) => $.ui.close({ id }),
    invalidate: () => $.ui.invalidate('ui.render'),
    process: (args, opts) => $.process.run(args, opts),
    run: async (args, opts) => {
      try {
        return parseAnswer(await $.process.run([room.bin, ...args], { timeoutMs: 20000, ...(opts || {}) }))
      } catch (err) {
        return { ok: false, error: String(err).slice(0, 200) }
      }
    },
    // spawn starts `landfall <args>` and hands each piece to onPiece; `stop()` ends the loop,
    // which kills the child; `done` settles with how it ended.
    spawn: (args, onPiece) => {
      const stream = $.process.spawn({ argv: [room.bin, ...args] })
      const done = (async () => {
        try {
          for (;;) {
            const step = await stream.next()
            if (step.done) {
              const v = step.value
              return (v && v.value) || v || { code: null, signal: null }
            }
            try {
              onPiece(step.value)
            } catch {
              // A piece the reader could not take never ends the child.
            }
          }
        } catch (err) {
          return { code: null, signal: null, error: String(err).slice(0, 200) }
        }
      })()
      return {
        stop: () => {
          try {
            void Promise.resolve(stream.return(undefined)).catch(() => {})
          } catch {
            // Already ended.
          }
        },
        done,
      }
    },
    play: (asset) => $.audio.play({ asset }),
    storeGet: (key) => $.store.get(key),
    storeSet: (key, value) => $.store.set(key, value),
    copy: (text, s) => $.ui.copy({ text, surface: s || surface || undefined }),
    now: () => $.clock.now(),
    invalidateContext: () => $.ui.invalidate('prompt.context'),
    append: async (text) => {
      try {
        const got = await $.session.append({ message: { type: 'user', content: [{ type: 'text', text }] } })
        return got && got.deny ? String(got.deny) : null
      } catch (err) {
        return String(err).slice(0, 200)
      }
    },
  }
}
