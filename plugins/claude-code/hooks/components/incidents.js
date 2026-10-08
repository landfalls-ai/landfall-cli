// Incident switchboard (proposal item 02, FR-02): /incidents lists the
// organization's open incidents, the rooms this session is in first and the
// SEV1s nobody here has joined next. Enter joins one the way a share link does
// (`landfall join --incident`), `b` drafts a prompt asking for its brief, `o`
// opens it in the browser. A practice incident (a simulated one) says so, and
// a list the CLI had to cut says that too. Every read and the join run the
// landfall CLI.
//
// While open the list is live (live.md FR-L2): it reads again every 30 s. A
// failed read keeps the last good list and says it is stale. A join from
// here puts the person's agent in the room too (FR-L5): `landfall serve`
// adopts the room the daemon holds on its next step, and the toast says so.

import { HOST, addCommand, ago, clip, parseAnswer, room, severityTone, statusTone } from '../core.js'
import { kit, TONE } from '../kit.js'
import { closed, drawn, due, LIST_MS, liveFooter, livePane, readLive, opened as markOpen } from '../live.js'

export const PANE = 'landfall-incidents'
export const PRACTICE = 'practice'
export const TRUNCATED = 'Your organization has more incidents than this list shows. Open the web app to see the rest.'
const ROOM_PANE = 'landfall-room'

// What the pane draws: the last `landfall incidents` answer, whether one is
// running, which incident the focus is on, and which one is being joined.
// `lp` (live.js) holds the answer, whether a read runs and whether the pane is open.
const lp = livePane()
const board = { sel: '', joining: '' }

export function install(on) {
  addCommand({ name: 'incidents', description: "List your organization's open incidents and join one" })

  on('command.run', { command: 'incidents' }, async ($) => {
    const opened = await $.ui.open({ id: PANE, title: 'Open incidents', focus: true, closeOnEscape: true })
    if (opened.isPlaced) markOpen(lp)
    await load(paneIo($))
    if (!opened.isPlaced) return { text: incidentsText(lp.last) }
    return {}
  })

  // The person's close (esc, the close mark) reaches this hook; the mod's own
  // closes go through closePane, since a plugin's own $.ui.close is not
  // raised to its own hooks.
  on('ui.close', { id: PANE }, async ($, e, next) => {
    closed(lp)
    return next(e)
  })

  // The focus ring names the incident `b` and `o` act on.
  on('ui.focus', { requestId: PANE }, async ($, e, next) => {
    if (e.element && e.element.startsWith('inc-') && e.element.slice(4) !== board.sel) {
      board.sel = e.element.slice(4)
      $.ui.invalidate('ui.render')
    }
    return next(e)
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const k = kit($.ui.resolve(e), e)
    const { Box, Text, Button, Link } = k.els
    drawn(lp)
    let nowMs = lp.goodAt
    try {
      nowMs = Number(await $.clock.now())
    } catch {
      // No clock: the age reads as of the last read.
    }
    const answer = lp.answer
    const list = answer && answer.ok ? sortIncidents(answer.incidents || []) : []
    const selected = list.find((i) => i.incidentId === board.sel) || list[0] || null

    const join = async (inc) => {
      if (inc.joined) {
        await closePane($)
        await $.ui.open({ id: ROOM_PANE, title: 'Landfall', focus: true, closeOnEscape: true })
        return
      }
      board.joining = inc.incidentId
      $.ui.invalidate('ui.render')
      const got = await cli($, ['join', '--incident', inc.incidentId])
      board.joining = ''
      if (got.ok) {
        inc.joined = true
        inc.roomKey = got.roomKey || inc.roomKey
        await $.ui.toast(joinedWords({ ...inc, ...pick(got) }), { timeoutMs: 8000 })
      } else {
        await $.ui.toast('Not joined: ' + clip(got.error || 'no answer', 200), { timeoutMs: 8000 })
      }
      $.ui.invalidate('ui.render')
    }
    const brief = async (inc) => {
      if (!inc) return
      await closePane($)
      await $.prompt.fill({ text: 'Give me the brief for ' + (inc.displayId || inc.title) })
    }
    const openIt = async (inc, surface) => {
      if (!inc || !inc.webUrl) return
      const copied = await $.ui.copy({ text: inc.webUrl, surface })
      await $.ui.toast(copied.isCopied ? 'Copied the link to ' + incName(inc) + ': ' + inc.webUrl : 'Open ' + incName(inc) + ': ' + inc.webUrl, {
        timeoutMs: 8000,
      })
    }
    const refresh = async () => {
      await load(paneIo($))
    }

    const pills = []
    if (answer && answer.ok) pills.push({ text: (answer.org || 'org') + ' · ' + list.length, tone: 'neutral' })
    const rows = [k.header({ key: 'inc-hdr', title: 'Open incidents', pills, dim: lp.inFlight ? 'reading…' : '' })]

    if (!answer) rows.push(Text({ key: 'inc-wait', dimColor: true, children: ['Reading open incidents…'] }))
    else if (!answer.ok) rows.push(Text({ key: 'inc-err', children: [String(answer.error || 'The incident list did not answer.')] }))
    else if (list.length === 0) rows.push(Text({ key: 'inc-none', dimColor: true, children: ['No open incidents in ' + (answer.org || 'your organization') + '.'] }))

    list.forEach((inc, i) => {
      const id = inc.incidentId
      const isSel = selected && selected.incidentId === id
      const sev = sevLabel(inc.severity)
      const note = incidentNote(inc, board.joining === id)
      if (k.terminal) {
        rows.push(
          k.row(
            [
              Text({ key: 'dot-' + id, color: TONE[severityTone(inc.severity)], children: ['●'] }),
              Button({
                key: 'inc-' + id,
                label: clip(incName(inc), Math.max(16, k.width - 34)),
                plain: true,
                onPress: () => join(inc),
                ...(i === 0 ? { autoFocus: true } : {}),
              }),
              sev ? Text({ key: 'sev-' + id, color: TONE[severityTone(inc.severity)], children: [sev] }) : null,
              inc.status ? Text({ key: 'st-' + id, children: [inc.status] }) : null,
              inc.practice ? Text({ key: 'pr-' + id, color: TONE.violet, children: [PRACTICE] }) : null,
              Text({ key: 'age-' + id, dimColor: true, children: [ago(inc.ageMs)] }),
            ],
            'r-' + id,
            1,
          ),
        )
        const noteProps = inc.joined ? { color: TONE.info } : { dimColor: true }
        rows.push(Text({ key: 'n-' + id, ...noteProps, children: ['    ' + clip(note, k.width - 4)] }))
        if (isSel && inc.webUrl && Link) rows.push(Box({ key: 'lk-' + id, paddingLeft: 4, children: [Link({ href: inc.webUrl, label: 'open in browser' })] }))
        return
      }
      // Desktop, VS Code, mobile: a card with a severity stripe and Join.
      const tone = severityTone(inc.severity)
      const head = k.row(
        [
          Text({ key: 't-' + id, bold: true, children: [incName(inc)] }),
          sev ? k.pill(sev, tone, 'sp-' + id) : null,
          inc.status ? k.pill(inc.status, statusTone(inc.status), 'stp-' + id) : null,
          inc.practice ? k.pill(PRACTICE, 'violet', 'prp-' + id) : null,
          Text({ key: 'age-' + id, dimColor: true, children: [ago(inc.ageMs)] }),
        ],
        'h-' + id,
        1,
      )
      const actions = k.row(
        [
          k.button({
            key: 'inc-' + id,
            label: board.joining === id ? 'Joining…' : inc.joined ? 'Open the room' : 'Join',
            primary: !inc.joined,
            onPress: () => join(inc),
          }),
          k.button({ key: 'brief-' + id, label: 'Brief only', onPress: () => brief(inc) }),
          inc.webUrl && Link ? Link({ href: inc.webUrl, label: 'Open in browser' }) : null,
        ],
        'a-' + id,
        1,
      )
      rows.push(
        Box({
          key: 'card-' + id,
          flexDirection: 'row',
          columnGap: 1,
          alignItems: 'stretch',
          borderStyle: 'round',
          borderColor: TONE[tone],
          children: [
            Box({ key: 'stripe-' + id, width: 1, backgroundColor: TONE[tone] }),
            k.col([head, Text({ key: 'n-' + id, dimColor: true, children: [note] }), actions], 'c-' + id),
          ],
        }),
      )
    })

    if (answer && answer.ok && answer.truncated) rows.push(Text({ key: 'inc-cut', dimColor: true, children: [TRUNCATED] }))
    // The list is the organization's, not the room's: no reconnecting line.
    const foot = liveFooter(k, lp, nowMs, null, 'inc-live')
    if (foot) rows.push(foot)

    const keys = []
    if (k.terminal && list.length > 0) {
      keys.push(Text({ key: 'enter', dimColor: true, children: ['enter: join'] }))
      keys.push(k.button({ key: 'brief', label: 'brief only', hotkey: 'b', onPress: () => brief(selected) }))
      keys.push(k.button({ key: 'open', label: 'open in browser', hotkey: 'o', onPress: (press) => openIt(selected, press.surface) }))
    }
    keys.push(k.button({ key: 'refresh', label: 'refresh', hotkey: 'r', onPress: refresh }))
    keys.push(k.button({ key: 'close', label: k.terminal ? 'close (esc)' : 'Close', dim: true, onPress: () => closePane($) }))
    rows.push(k.row(keys, 'inc-keys'))
    return Box({ flexDirection: 'column', rowGap: k.terminal ? 0 : 1, children: rows })
  })
}

export async function band(io, e, k) {
  return null
}

export function onSnapshot(io, snap, prev) {}

export function start(io) {}

// tick: an open list reads again every 30 s, and the age under it moves.
export async function tick(io, nowMs) {
  if (!lp.open) return
  io.invalidate()
  if (due(lp, nowMs, LIST_MS) && !board.joining) void load(io)
}

// load runs `landfall incidents` and redraws, one read at a time.
async function load(io) {
  const fetch = () => {
    io.invalidate()
    return io.run(['incidents', '--host', HOST], { timeoutMs: 20000 })
  }
  await readLive(lp, fetch, () => ioNow(io))
  const a = lp.answer
  if (a && a.ok && !(a.incidents || []).some((i) => i.incidentId === board.sel)) {
    const first = sortIncidents(a.incidents || [])[0]
    board.sel = first ? first.incidentId : ''
  }
  io.invalidate()
}

// closePane closes the pane and stops its reads.
async function closePane($) {
  closed(lp)
  await $.ui.close({ id: PANE })
}

// paneIo is what this file's reads take, from the hook's own `$`, shaped as
// register.js shapes `io`.
function paneIo($) {
  return {
    run: async (args, opts) => {
      try {
        return parseAnswer(await $.process.run([room.bin, ...args], { timeoutMs: 20000, ...(opts || {}) }))
      } catch (err) {
        return { ok: false, error: clip(String(err), 200) }
      }
    },
    invalidate: () => $.ui.invalidate('ui.render'),
    now: () => $.clock.now(),
  }
}

async function ioNow(io) {
  try {
    return Number(await io.now())
  } catch {
    return lp.triedAt
  }
}

// joinedWords is the toast after a join from here: the mod is in the room
// now, and the person's agent follows on its next step (FR-L5).
export function joinedWords(inc) {
  return 'Joined ' + incName(inc) + '. Your agent joins on its next step.'
}

// cli runs `landfall <args> --host claude-code` and reads its one JSON line.
async function cli($, args) {
  try {
    return parseAnswer(await $.process.run([room.bin, ...args, '--host', HOST], { timeoutMs: 20000 }))
  } catch (err) {
    return { ok: false, error: clip(String(err), 200) }
  }
}

function pick(got) {
  const out = {}
  if (got.displayId) out.displayId = got.displayId
  if (got.title) out.title = got.title
  return out
}

// sortIncidents: the rooms this session is in, then SEV1s nobody here has
// joined, then by severity, newest first within one.
export function sortIncidents(list) {
  const rank = (i) => (i.joined ? 0 : sevRank(i.severity) === 1 ? 1 : 2)
  return [...list].sort((a, b) => rank(a) - rank(b) || sevRank(a.severity) - sevRank(b.severity) || (a.ageMs ?? 0) - (b.ageMs ?? 0))
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

export function incName(inc) {
  return [inc.displayId, inc.title].filter(Boolean).join(' ') || 'incident'
}

// incidentNote is the line under an incident: whether this session is in it,
// who is there and Beacon's state (the CLI knows those only for joined rooms).
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

// incidentsText is /incidents where no pane can be drawn.
export function incidentsText(answer) {
  if (!answer) return 'The incident list did not answer.'
  if (!answer.ok) return String(answer.error || 'The incident list did not answer.')
  const list = sortIncidents(answer.incidents || [])
  if (list.length === 0) return 'No open incidents in ' + (answer.org || 'your organization') + '.'
  const out = ['Open incidents · ' + (answer.org || 'your organization')]
  for (const inc of list) {
    const parts = [incName(inc), sevLabel(inc.severity), inc.status, inc.practice ? PRACTICE : '', ago(inc.ageMs)].filter(Boolean)
    out.push('  ' + parts.join(' · ') + ' · ' + incidentNote(inc, false))
  }
  if (answer.truncated) out.push(TRUNCATED)
  return out.join('\n')
}
