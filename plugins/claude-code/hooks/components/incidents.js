// Incident switchboard (proposal item 02, FR-02): /incidents lists the
// organization's open incidents, the rooms this session is in first and the
// SEV1s nobody here has joined next. Enter joins one the way a share link does
// (`landfall join --incident`), `b` drafts a prompt asking for its brief, `o`
// opens it in the browser. A practice incident (a simulated one) says so, and
// a list the CLI had to cut says that too. Every read and the join run the
// landfall CLI.

import { HOST, addCommand, ago, clip, parseAnswer, room, severityTone, statusTone } from '../core.js'
import { kit, TONE } from '../kit.js'

export const PANE = 'landfall-incidents'
export const PRACTICE = 'practice'
export const TRUNCATED = 'Your organization has more incidents than this list shows. Open the web app to see the rest.'
const ROOM_PANE = 'landfall-room'

// What the pane draws: the last `landfall incidents` answer, whether one is
// running, which incident the focus is on, and which one is being joined.
const board = { answer: null, loading: false, sel: '', joining: '' }

export function install(on) {
  addCommand({ name: 'incidents', description: "List your organization's open incidents and join one" })

  on('command.run', { command: 'incidents' }, async ($) => {
    const opened = await $.ui.open({ id: PANE, title: 'Open incidents', focus: true, closeOnEscape: true })
    await load($)
    if (!opened.isPlaced) return { text: incidentsText(board.answer) }
    return {}
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
    const answer = board.answer
    const list = answer && answer.ok ? sortIncidents(answer.incidents || []) : []
    const selected = list.find((i) => i.incidentId === board.sel) || list[0] || null

    const join = async (inc) => {
      if (inc.joined) {
        await $.ui.close({ id: PANE })
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
        await $.ui.toast('Joined ' + incName({ ...inc, ...pick(got) }) + '. Room news reaches this session from now on.', { timeoutMs: 8000 })
      } else {
        await $.ui.toast('Not joined: ' + clip(got.error || 'no answer', 200), { timeoutMs: 8000 })
      }
      $.ui.invalidate('ui.render')
    }
    const brief = async (inc) => {
      if (!inc) return
      await $.ui.close({ id: PANE })
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
      await load($)
    }

    const pills = []
    if (answer && answer.ok) pills.push({ text: (answer.org || 'org') + ' · ' + list.length, tone: 'neutral' })
    const rows = [k.header({ key: 'inc-hdr', title: 'Open incidents', pills, dim: board.loading ? 'reading…' : '' })]

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

    const keys = []
    if (k.terminal && list.length > 0) {
      keys.push(Text({ key: 'enter', dimColor: true, children: ['enter: join'] }))
      keys.push(k.button({ key: 'brief', label: 'brief only', hotkey: 'b', onPress: () => brief(selected) }))
      keys.push(k.button({ key: 'open', label: 'open in browser', hotkey: 'o', onPress: (press) => openIt(selected, press.surface) }))
    }
    keys.push(k.button({ key: 'refresh', label: 'refresh', hotkey: 'r', onPress: refresh }))
    keys.push(k.button({ key: 'close', label: k.terminal ? 'close (esc)' : 'Close', dim: true, onPress: () => $.ui.close({ id: PANE }) }))
    rows.push(k.row(keys, 'inc-keys'))
    return Box({ flexDirection: 'column', rowGap: k.terminal ? 0 : 1, children: rows })
  })
}

export async function band(io, e, k) {
  return null
}

export function onSnapshot(io, snap, prev) {}

export function start(io) {}

// load runs `landfall incidents` and redraws.
async function load($) {
  board.loading = true
  $.ui.invalidate('ui.render')
  board.answer = await cli($, ['incidents'])
  board.loading = false
  if (board.answer.ok && !(board.answer.incidents || []).some((i) => i.incidentId === board.sel)) {
    const first = sortIncidents(board.answer.incidents || [])[0]
    board.sel = first ? first.incidentId : ''
  }
  $.ui.invalidate('ui.render')
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

// tick runs every TICK_MS while the session lives: a component with an open
// pane refreshes it here on its own cadence (nothing to do by default).
export function tick(io, nowMs) {}
