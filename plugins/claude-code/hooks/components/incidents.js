// The Incidents tab of the console (spec §4.6, §2.6): the organization's open incidents, the
// rooms this session is in first and the SEV1s nobody here has joined next. Enter joins one the
// way a share link does (`landfall join --incident`), then adds the room's shared context to the
// person's conversation (context.js addSharedContext) and switches the console to Home. `b`
// drafts a prompt asking for its brief, `o` copies its link for the browser. A practice incident
// says so, and a list the CLI had to cut says that too. Every read and the join run the CLI.
//
// Not in a room, the tab is the way in (§2.6): the same list under `Join an incident`, or, with
// no sign-in, the sign-in state (signin.js). Warm, the list reads again every 30 s; a failed read
// keeps the last good list and says it is stale.

import { HOST, ago, clip, consoleState, currentRoom, openConsole, severityTone, statusTone, toastText } from '../core.js'
import { addSharedContext } from './context.js'
import { due, LIST_MS, livePane, readLive } from '../live.js'
import { noteRefusal, signin, signinBody, signedOut } from './signin.js'

export const PRACTICE = 'practice'
export const TRUNCATED = 'Your organization has more incidents than this list shows. Open the web app to see the rest.'
export const TAB = 'incidents'

// The last `landfall incidents` answer and its liveness (live.js), and which incident is being
// joined. Which one `b` and `o` act on is the console's focus ring (consoleState.focus).
const lp = livePane()
// `linkFor`: the incident whose browser open failed, which shows its link row until the focus
// moves off it (round 5 review, issue 1).
const board = { joining: '', linkFor: '', linkFocus: '' }

export function install(on) {}

export async function band(io, e, k) {
  return null
}

export function onSnapshot(io, snap, prev) {}

export function start(io) {}

// tick: while the console is open and this tab warm, the list reads again every 30 s.
export async function tick(io, nowMs) {
  if (!isWarm()) return
  if (due({ ...lp, open: true }, nowMs, LIST_MS) && !board.joining && !signedOut()) void load(io)
}

function isWarm() {
  return consoleState.open && consoleState.warm.has(TAB)
}

// THE CONSOLE CONTRACT (core.js CONSOLE): console.js calls these by name.

// warm starts the list's reads: one now, then every 30 s while the console is open.
export function warm(io) {
  consoleState.warm.add(TAB)
  if (!signedOut() && !lp.inFlight) void load(io)
}

// badge is the open incidents once read; the console draws it only in the full label set.
export function badge() {
  const a = lp.answer
  return a && a.ok ? (a.incidents || []).length : null
}

// tab draws the body: { rows, keys, live, footer, refresh } (console.js draws the footer, the
// keys row with `r: refresh` and `close (esc)` after these keys).
export function tab(k, io, nowMs, args) {
  const r = currentRoom()
  if (signedOut()) {
    const s = signinBody(k, io, nowMs, () => load(io))
    return { rows: s.rows, keys: s.keys, footer: s.footer, live: null, refresh: null, noRefresh: true }
  }
  const { Text } = k.els
  const answer = lp.answer
  const list = answer && answer.ok ? sortIncidents(answer.incidents || []) : []
  const focused = list.find((i) => 'inc-' + i.incidentId === consoleState.focus) || list[0] || null
  const rows = []
  const org = (answer && answer.ok && answer.org) || (signin.org || '')
  const heading = r ? 'Open incidents' : 'Join an incident'
  const tail = answer && answer.ok ? [org, r ? String(list.length) : list.length + ' open'].filter(Boolean).join(' · ') : ''
  rows.push(k.row([Text({ key: 'inc-h', bold: true, children: [heading] }), tail ? Text({ key: 'inc-h-d', dimColor: true, children: [tail] }) : null], 'inc-head', 2))

  if (!answer) rows.push(Text({ key: 'inc-wait', dimColor: true, children: ['Reading open incidents…'] }))
  else if (!answer.ok) rows.push(Text({ key: 'inc-err', children: [String(answer.error || 'The incident list did not answer.')] }))
  else if (list.length === 0) {
    rows.push(Text({ key: 'inc-none', dimColor: true, children: ['No open incidents in ' + (org || 'your organization') + '.'] }))
    if (!r) rows.push(Text({ key: 'inc-again', dimColor: true, children: ['This list reads again every 30 s.'] }))
  }

  list.forEach((inc, i) => {
    if (k.terminal) rows.push(...termRow(k, io, inc, i === 0))
    else rows.push(card(k, io, inc, focused && focused.incidentId === inc.incidentId))
  })
  if (answer && answer.ok && answer.truncated) rows.push(Text({ key: 'inc-cut', dimColor: true, children: [TRUNCATED] }))
  if (list.length > 0) {
    const mine = focused && focused.joined && r && focused.roomKey === r.roomKey
    const hint = r ? (mine ? 'Enter: open the room' : 'Enter: join') : 'Enter: join · your agent gets the shared context when you do'
    if (k.terminal) rows.push(Text({ key: 'inc-hint-gap', children: [' '] }))
    rows.push(Text({ key: 'inc-hint', dimColor: true, children: [hint] }))
  }

  const keys = []
  if (k.terminal && list.length > 0) {
    keys.push(k.button({ key: 'brief', label: 'brief only', hotkey: 'b', onPress: () => brief(io, focused) }))
    keys.push(k.button({ key: 'open', label: 'open in browser', hotkey: 'o', onPress: () => openIt(io, focused) }))
  }
  return { rows, keys, live: lp, footer: null, refresh: () => load(io) }
}

// termRow is one incident on the terminal: the severity label, the name as a plain Button that
// joins, the status label, practice, the age; then its note, under the name.
function termRow(k, io, inc, first) {
  const { Text, Button } = k.els
  const id = inc.incidentId
  const sev = sevLabel(inc.severity)
  const head = k.row(
    [
      sev ? k.pill(sev, severityTone(inc.severity), 'sev-' + id) : null,
      Button({ key: 'inc-' + id, label: clip(incName(inc), Math.max(16, k.width - 34)), plain: true, onPress: () => join(io, inc), ...(first ? { autoFocus: true } : {}) }),
      inc.status ? k.pill(inc.status, statusTone(inc.status), 'st-' + id) : null,
      inc.practice ? k.pill(PRACTICE, 'violet', 'pr-' + id) : null,
      Text({ key: 'age-' + id, dimColor: true, children: [ago(inc.ageMs)] }),
    ],
    'r-' + id,
    1,
  )
  const note = rowNote(inc, board.joining === id)
  const out = [head]
  if (note) out.push(Text({ key: 'n-' + id, dimColor: true, children: [' '.repeat(NOTE_INDENT) + clip(note, k.width - NOTE_INDENT)] }))
  const link = linkRow(k, inc)
  if (link) out.push(link)
  return out
}

// The note sits under the name column (round 4 review, issue 14): the severity label's cells.
const NOTE_INDENT = 7

// card is one incident off the terminal: neutral border, no stripe (the severity label carries
// it), `Join` primary (`Open the room` once in it), `Brief only`, `Open in browser`; key chips
// only on the focused card's buttons. Joining, only the button says so; the note stays.
function card(k, io, inc, focused) {
  const { Text } = k.els
  const id = inc.incidentId
  const sev = sevLabel(inc.severity)
  const joining = board.joining === id
  const head = k.row(
    [
      Text({ key: 't-' + id, bold: true, children: [incName(inc)] }),
      sev ? k.pill(sev, severityTone(inc.severity), 'sp-' + id) : null,
      inc.status ? k.pill(inc.status, statusTone(inc.status), 'stp-' + id) : null,
      inc.practice ? k.pill(PRACTICE, 'violet', 'prp-' + id) : null,
      Text({ key: 'age-' + id, dimColor: true, children: [ago(inc.ageMs)] }),
    ],
    'h-' + id,
    1,
  )
  const note = rowNote(inc, false)
  const actions = k.row(
    [
      k.button({ key: 'inc-' + id, label: joining ? 'Joining…' : isMine(inc) ? 'Open the room' : 'Join', primary: !isMine(inc), onPress: () => join(io, inc) }),
      k.button({ key: 'brief-' + id, label: 'Brief only', ...(focused ? { hotkey: 'b' } : {}), onPress: () => brief(io, inc) }),
      k.button({ key: 'open-' + id, label: 'Open in browser', ...(focused ? { hotkey: 'o' } : {}), onPress: () => openIt(io, inc) }),
    ],
    'a-' + id,
    1,
  )
  return k.card([head, note ? Text({ key: 'n-' + id, dimColor: true, children: [note] }) : null, linkRow(k, inc), actions], { key: 'card-' + id })
}

// isMine: the incident is the room this folder is in now.
function isMine(inc) {
  const r = currentRoom()
  return !!(inc.joined && r && (!inc.roomKey || inc.roomKey === r.roomKey))
}

// join: Enter on an incident. The one this folder is in opens Home; any other is joined, the
// shared context added, and the console switches to Home. Nothing here starts a turn.
async function join(io, inc) {
  if (board.joining) return
  if (isMine(inc)) {
    await openConsole(io, 'home', null, { focus: true })
    return
  }
  board.joining = inc.incidentId
  io.invalidate()
  const got = (await io.run(['join', '--incident', inc.incidentId, '--host', HOST])) || {}
  if (!got.ok) {
    board.joining = ''
    if (noteRefusal(got.error)) io.toast(toastText('Not joined: ' + (got.error || 'no answer')), 8000)
    else io.toast(notJoinedWords(got.error), 8000)
    io.invalidate()
    return
  }
  inc.joined = true
  inc.roomKey = got.roomKey || inc.roomKey
  const named = { ...inc, ...pick(got) }
  // The shared context into the conversation (§2.6, §4.10); context.js raises the one join toast
  // ("Joined Landfall 171. Shared context added: 2 established, 3 open."), so nothing here does.
  try {
    await addSharedContext(io, { roomKey: inc.roomKey, joined: clip(named.displayId || named.title || 'the incident', 24) })
  } catch (err) {
    io.toast(toastText('Joined ' + clip(named.displayId || named.title || 'the incident', 24) + '. The shared context could not be read; add it from Context.'), 8000)
  }
  board.joining = ''
  consoleState.joined = { roomKey: inc.roomKey, name: incName(named) }
  await openConsole(io, 'home', null, { focus: true })
}

export function notJoinedWords(error) {
  return toastText('Not joined: ' + (error || 'no answer'))
}

async function brief(io, inc) {
  if (!inc) return
  await io.fill('Give me the brief for ' + (inc.displayId || inc.title))
}

// openIt opens the incident in the person's browser through `landfall open` (the CLI opens it,
// as `landfall login` does; the mod never opens anything itself). When no browser answers, the
// toast says why and the focused row gains a link the person can use.
async function openIt(io, inc) {
  if (!inc || !inc.webUrl) return
  const got = (await io.run(['open', inc.webUrl, '--host', HOST])) || {}
  if (got.ok) {
    board.linkFor = ''
    io.toast(openedWords(inc), 6000)
  } else {
    board.linkFor = inc.incidentId
    board.linkFocus = consoleState.focus
    io.toast(notOpenedWords(got.error), 8000)
  }
  io.invalidate()
}

export function openedWords(inc) {
  return toastText('Opened ' + clip(inc.displayId || inc.title || 'the incident', 24) + ' in your browser.')
}

export function notOpenedWords(error) {
  return toastText('Could not open your browser: ' + (error || 'no answer'))
}

// linkRow is the focused incident's link after a browser open failed: the engine's Link (OSC 8 on
// the terminal, else the label in ink and the URL dim; an anchor on the desktop).
function linkRow(k, inc) {
  if (board.linkFor !== inc.incidentId || !inc.webUrl) return null
  if (consoleState.focus !== board.linkFocus) {
    board.linkFor = ''
    return null
  }
  const { Box, Link, Text } = k.els
  const link = Link ? Link({ href: inc.webUrl, label: 'the war room in your browser' }) : Text({ children: ['the war room in your browser  ' + inc.webUrl] })
  return Box({ key: 'lk-' + inc.incidentId, paddingLeft: k.terminal ? NOTE_INDENT : 0, children: [link] })
}

// load runs `landfall incidents` and redraws, one read at a time. A `Sign in to …` refusal
// turns the tab into the sign-in state.
export async function load(io) {
  const fetch = () => {
    io.invalidate()
    return io.run(['incidents', '--host', HOST], { timeoutMs: 20000 })
  }
  await readLive(lp, fetch, () => ioNow(io))
  const last = lp.last
  if (last && !last.ok && noteRefusal(last.error)) {
    lp.answer = null
    lp.stale = ''
  } else if (last && last.ok && signin.signedIn !== true) {
    signin.signedIn = true
    if (last.org) signin.org = String(last.org)
  }
  io.invalidate()
}

async function ioNow(io) {
  try {
    return Number(await io.now())
  } catch {
    return lp.triedAt
  }
}

// incidentsAnswer is the last answer the list read (the no-pane text uses it).
export function incidentsAnswer() {
  return lp.last
}

function pick(got) {
  const out = {}
  if (got.displayId) out.displayId = got.displayId
  if (got.title) out.title = got.title
  return out
}

// sortIncidents: the rooms this session is in, then SEV1s nobody here has
// joined, then by severity; within one the CLI's own order stands (the sort is stable, and round 1
// found the mod re-sorting equal severities by age against the CLI's and the mockup's order).
export function sortIncidents(list) {
  const rank = (i) => (i.joined ? 0 : sevRank(i.severity) === 1 ? 1 : 2)
  return [...list].sort((a, b) => rank(a) - rank(b) || sevRank(a.severity) - sevRank(b.severity))
}

export function sevRank(sev) {
  const s = String(sev || '').toLowerCase()
  const m = /sev\s*(\d)/.exec(s)
  if (m) return Number(m[1])
  if (/critical/.test(s)) return 1
  if (/high/.test(s)) return 2
  return 9
}

export function sevLabel(sev) {
  return String(sev || '').toUpperCase()
}

// incName is the room as every other place names it: "Landfall 171 · cache-stampede".
export function incName(inc) {
  return [inc.displayId, inc.title].filter(Boolean).join(' · ') || 'incident'
}

// rowNote is the dim line under an incident in the console: whether this session is in it, who
// is there and Beacon's state (the CLI knows those only for joined rooms); `Joining…` on the
// terminal while it joins.
export function rowNote(inc, joining) {
  if (joining) return 'Joining…'
  const out = []
  if (isMine(inc) || (inc.joined && !currentRoom())) out.push('you are in it')
  if (Array.isArray(inc.here) && inc.here.length > 0) out.push(inc.here.length + ' here: ' + inc.here.slice(0, 4).join(', '))
  else if (typeof inc.hereCount === 'number') out.push(inc.hereCount === 0 ? 'nobody here yet' : inc.hereCount + ' here')
  if (inc.beacon) out.push('Beacon ' + inc.beacon)
  return out.join(' · ')
}

// incidentNote is the text answer's line under an incident (the old /incidents text).
export function incidentNote(inc, joining) {
  if (joining) return 'Joining…'
  const out = []
  if (inc.joined) out.push('you are in it')
  if (Array.isArray(inc.here) && inc.here.length > 0) out.push(inc.here.length + ' here: ' + inc.here.slice(0, 4).join(', '))
  else if (typeof inc.hereCount === 'number') out.push(inc.hereCount === 0 ? 'nobody here yet' : inc.hereCount + ' here')
  if (inc.beacon) out.push('Beacon ' + inc.beacon)
  if (!inc.joined) out.push('not joined')
  return out.join(' · ')
}

// incidentsText is the tab where no pane can be drawn (`claude -p`).
export function incidentsText(answer) {
  if (!answer) return 'The incident list did not answer.'
  if (!answer.ok) return String(answer.error || 'The incident list did not answer.')
  const list = sortIncidents(answer.incidents || [])
  if (list.length === 0) return 'No open incidents in ' + (answer.org || 'your organization') + '.'
  const out = ['Open incidents · ' + (answer.org || 'your organization')]
  for (const inc of list) {
    const parts = [[inc.displayId, inc.title].filter(Boolean).join(' '), sevLabel(inc.severity), inc.status, inc.practice ? PRACTICE : '', ago(inc.ageMs)].filter(Boolean)
    out.push('  ' + parts.join(' · ') + ' · ' + incidentNote(inc, false))
  }
  if (answer.truncated) out.push(TRUNCATED)
  return out.join('\n')
}
