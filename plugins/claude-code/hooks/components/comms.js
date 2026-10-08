// Stakeholder update preview (proposal item 14, FR-14): /comms shows the
// room's latest stakeholder updates, newest first as the CLI lists them, what
// kind each is, whether it was approved and sent, and who approved it, so an
// engineer knows what customers were told. An update the room only simulated
// says so. The server keeps no time per update, so none is shown. Read only:
// approving an update is the one approval in the product and stays in the war
// room in the browser, so this pane has no approve button.
//
// While open the list is live (live.md FR-L2): it reads again every 30 s. A
// failed read keeps the last good list and says it is stale.

import { HOST, addCommand, clip, currentRoom, parseAnswer, room } from '../core.js'
import { kit } from '../kit.js'
import { closed, drawn, due, LIST_MS, liveFooter, livePane, readLive, opened as markOpen } from '../live.js'

export const PANE = 'landfall-comms'
export const APPROVAL_NOTE = 'Approving an update happens in the war room, in the browser.'
const SHOWN = 6

// The live read (live.js): the last good `landfall comms` answer, whether
// one is running, open or not; and the room it reads.
const lp = livePane()
const view = { roomKey: '' }

export function install(on) {
  addCommand({ name: 'comms', description: 'Read the stakeholder updates drafted in the war room (read only)' })

  on('command.run', { command: 'comms' }, async ($) => {
    const r = currentRoom()
    if (!r) return { text: 'This folder is not in a war room. Open a share link from the room, or run /incidents to join one.' }
    const opened = await $.ui.open({ id: PANE, title: 'Stakeholder updates', focus: true, closeOnEscape: true })
    if (opened.isPlaced) markOpen(lp)
    await load(paneIo($), r.roomKey)
    if (!opened.isPlaced) return { text: commsText(lp.last) }
    return {}
  })

  // The person's close (esc, the close mark) reaches this hook; the mod's own
  // closes go through closePane, since a plugin's own $.ui.close is not
  // raised to its own hooks.
  on('ui.close', { id: PANE }, async ($, e, next) => {
    closed(lp)
    return next(e)
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const k = kit($.ui.resolve(e), e)
    const { Box, Text } = k.els
    drawn(lp)
    let nowMs = lp.goodAt
    try {
      nowMs = Number(await $.clock.now())
    } catch {
      // No clock: the age reads as of the last read.
    }
    const r = currentRoom()
    const answer = lp.answer
    const messages = answer && answer.ok ? (answer.messages || []).slice(0, SHOWN) : []

    const rows = [k.header({ key: 'cm-hdr', title: 'Stakeholder updates', pills: [], dim: lp.inFlight ? 'reading…' : r ? r.displayId || '' : '' })]
    if (!answer) rows.push(Text({ key: 'cm-wait', dimColor: true, children: ['Reading stakeholder updates…'] }))
    else if (!answer.ok) rows.push(Text({ key: 'cm-err', children: [String(answer.error || 'The stakeholder updates did not answer.')] }))
    else if (messages.length === 0) rows.push(Text({ key: 'cm-none', dimColor: true, children: ['No stakeholder update has been drafted yet.'] }))

    messages.forEach((m, i) => {
      const id = m.id || 'm' + i
      const meta = metaWords(m)
      const head = k.row(
        [
          k.pill(m.state || 'draft', commsTone(m.state), 'cs-' + id),
          m.simulated ? k.pill('simulated', 'violet', 'csim-' + id) : null,
          meta ? Text({ key: 'cmeta-' + id, dimColor: true, children: [meta] }) : null,
        ],
        'ch-' + id,
        1,
      )
      const body = Text({ key: 'ct-' + id, children: [String(m.text || '')] })
      if (k.terminal) {
        rows.push(head)
        rows.push(Box({ key: 'cb-' + id, paddingLeft: 2, children: [body] }))
        return
      }
      rows.push(Box({ key: 'card-' + id, flexDirection: 'column', rowGap: 1, borderStyle: 'round', borderColor: '#d9d9d6', paddingX: 1, children: [head, body] }))
    })

    rows.push(Text({ key: 'cm-note', dimColor: true, children: [APPROVAL_NOTE] }))
    const foot = liveFooter(k, lp, nowMs, r, 'cm-live')
    if (foot) rows.push(foot)
    rows.push(
      k.row(
        [
          r ? k.button({ key: 'refresh', label: 'refresh', hotkey: 'r', onPress: () => load(paneIo($), r.roomKey) }) : null,
          k.button({ key: 'close', label: k.terminal ? 'close (esc)' : 'Close', dim: true, onPress: () => closePane($) }),
        ],
        'cm-keys',
      ),
    )
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
  const r = currentRoom()
  if (due(lp, nowMs, LIST_MS) && r) void load(io, r.roomKey)
}

// load runs `landfall comms` for the room and redraws, one read at a time.
async function load(io, roomKey) {
  view.roomKey = roomKey
  const fetch = () => {
    io.invalidate()
    return io.run(['comms', '--room', view.roomKey, '--host', HOST], { timeoutMs: 20000 })
  }
  await readLive(lp, fetch, () => ioNow(io))
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

// commsTone is the pill's tone for an update's state.
export function commsTone(state) {
  return { draft: 'neutral', approved: 'info', sent: 'good', failed: 'critical' }[state] || 'neutral'
}

// kindWords is an update's kind as a person says it: "status-update" -> "status update".
export function kindWords(kind) {
  return String(kind || '').replace(/[-_]+/g, ' ').trim()
}

// metaWords is the line beside the pill: "status update · slack · approved by carol".
export function metaWords(m) {
  const out = []
  const kind = kindWords(m.kind)
  if (kind) out.push(kind)
  if (m.channel) out.push(m.channel)
  if (m.approvedBy) out.push('approved by ' + m.approvedBy)
  return out.join(' · ')
}

// commsText is /comms where no pane can be drawn.
export function commsText(answer) {
  if (!answer) return 'The stakeholder updates did not answer.'
  if (!answer.ok) return String(answer.error || 'The stakeholder updates did not answer.')
  const messages = (answer.messages || []).slice(0, SHOWN)
  if (messages.length === 0) return 'No stakeholder update has been drafted yet. ' + APPROVAL_NOTE
  const out = []
  for (const m of messages) {
    out.push([m.state || 'draft', m.simulated ? 'simulated' : '', metaWords(m)].filter(Boolean).join(' · '))
    out.push('  ' + String(m.text || ''))
  }
  out.push(APPROVAL_NOTE)
  return out.join('\n')
}
