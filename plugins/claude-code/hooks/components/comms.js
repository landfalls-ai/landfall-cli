// Stakeholder update preview (proposal item 14, FR-14): the Comms chip inside the console's More
// tab (spec 4.7, 4.11) shows the room's latest stakeholder updates, newest first as the CLI lists
// them, what kind each is, whether it was approved and sent, and who approved it, so an engineer
// knows what customers were told. An update the room only simulated says so. The server keeps no
// time per update, so none is shown. Read only: approving an update is the one approval in the
// product and stays in the war room in the browser, so this tab has no approve button and no
// action keys.
//
// Once shown the list is live (spec 2.5): it reads again every 30 s while the console is open. A
// failed read keeps the last good list and says it is stale. More draws the chip row and calls
// `tab` for the body; the chip's count is `count()`.

import { HOST, consoleState, currentRoom, reading } from '../core.js'
import { closed, drawn, due, LIST_MS, livePane, readLive, opened as markOpen } from '../live.js'
import { centerRow, footerOf, nowOf, resetLive } from './tabparts.js'

export const APPROVAL_NOTE = 'Approving an update happens in the war room, in the browser.'
const SHOWN = 6

// The live read (live.js): the last good `landfall comms` answer, whether one is running, open or
// not; and the room it reads.
const lp = livePane()
const view = { roomKey: '' }

// forRoom drops what was read of another room.
function forRoom(r) {
  const key = r && r.roomKey ? r.roomKey : ''
  if (key === view.roomKey) return
  view.roomKey = key
  resetLive(lp)
}

// No hooks of its own: the console owns the pane and the `/landfall` command.
export function install(on) {}

export async function band(io, e, k) {
  return null
}

export function onSnapshot(io, snap, prev) {}

export function start(io) {}

// tick: a shown list reads again every 30 s; closing the console stops the reads.
export async function tick(io, nowMs) {
  if (lp.open && !reading('more')) closed(lp)
  const r = currentRoom()
  const moved = (r ? r.roomKey : '') !== view.roomKey
  forRoom(r)
  if (!lp.open || !reading('more')) return
  if (r && (moved || due(lp, nowMs, LIST_MS))) void load(io, r.roomKey)
}

// warm starts the list's reads when the Comms chip is first shown (and reads again when asked).
export async function warm(io) {
  const r = currentRoom()
  forRoom(r)
  markOpen(lp)
  if (r) await load(io, r.roomKey)
}

export async function refresh(io) {
  await warm(io)
}

// Comms has no badge of its own: its count sits on More's chip (`count`).
export function badge() {
  return null
}

// count is the chip's number: updates not yet sent (draft or approved), known only once the list
// has been read; null before that and at zero.
export function count() {
  forRoom(currentRoom())
  const a = lp.answer
  if (!a || !a.ok) return null
  const n = (a.messages || []).filter((m) => m && m.state !== 'sent').length
  return n > 0 ? n : null
}

// The live read's state (live.js).
export function readState() {
  return lp
}

// Comms has no letters.
export function keys(k, io, nowMs, args) {
  return []
}

// tab is the Comms body (spec 4.7), under More's chip row: `Stakeholder updates` bold, one block
// per update (the state label, `simulated` in violet, dim meta, the text), at most 6, the dim
// note. The console draws the footer.
export function tab(k, io, nowMs, args) {
  const { Box, Text } = k.els
  drawn(lp)
  const r = currentRoom()
  forRoom(r)
  if (!r) return [k.dim('This folder is not in a war room.', 'cm-noroom')]
  const answer = lp.answer
  const messages = answer && answer.ok ? (answer.messages || []).slice(0, SHOWN) : []
  const rows = [Text({ key: 'cm-hdr', bold: true, children: ['Stakeholder updates'] })]
  if (!answer) rows.push(Text({ key: 'cm-wait', dimColor: true, children: ['Reading stakeholder updates…'] }))
  else if (!answer.ok) rows.push(Text({ key: 'cm-err', children: [String(answer.error || 'The stakeholder updates did not answer.')] }))
  else if (messages.length === 0) rows.push(Text({ key: 'cm-none', dimColor: true, children: ['No stakeholder update has been drafted yet.'] }))

  messages.forEach((m, i) => {
    const id = m.id || 'm' + i
    const meta = metaWords(m)
    const head = centerRow(
      k,
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
  return rows
}

// footer is the console's footer for this tab: `live · updated 4s ago`, or null before the first read.
export function footer(k, nowMs) {
  return footerOf(k, lp, nowMs, currentRoom(), 'cm-live')
}

// text is the Comms answer where no pane can be placed (`claude -p`): it reads the list and
// says it.
export async function text(io, args) {
  const r = currentRoom()
  if (!r) return 'This folder is not in a war room. Open a share link from the room, or run /landfall incidents to join one.'
  forRoom(r)
  await load(io, r.roomKey)
  return commsText(lp.last)
}

// load runs `landfall comms` for the room and redraws, one read at a time.
async function load(io, roomKey) {
  view.roomKey = roomKey
  const fetch = () => {
    io.invalidate()
    return io.run(['comms', '--room', view.roomKey, '--host', HOST], { timeoutMs: 20000 })
  }
  await readLive(lp, fetch, () => nowOf(io, lp.triedAt))
  io.invalidate()
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
