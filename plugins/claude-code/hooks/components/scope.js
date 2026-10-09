// The room's scope in the conversation's context while attached (proposal
// item 17, built on `prompt.context` per the design review's finding 8: a
// plugin cannot add a `prompt.compose` section).
//
// One short block, `landfallRoomScope`, naming the room the session is in and
// what the room pinned it to: the focus (`steer.recorded`) and the scope
// (`context.attached`), as `landfall watch` carries them in
// rooms[].status.focus and .scope. Never room news, which keeps arriving
// through delivery.js. No focus and no scope: no block.
//
// The engine computes context blocks once per conversation and keeps them
// until `$.ui.invalidate('prompt.context')`, and a change re-sends the whole
// conversation uncached, so the block is refreshed only when its text changes:
// on the next message the person sends (delivery.js asks scopeChanged), or at
// once when register.js's io carries `invalidateContext` (see onSnapshot).

import { clip, roomName, room } from '../core.js'

export const BLOCK = 'landfallRoomScope'

// last: the block's text as the engine last took it ('' for none), undefined
// before the engine has asked.
export const scope = { last: undefined }

export function install(on) {
  on('prompt.context', async ($, e, next) => {
    const got = await next(e)
    const text = scopeText(room.snapshot)
    scope.last = text
    const blocks = (got.blocks ?? []).filter((b) => b.name !== BLOCK)
    if (!text) return { ...got, blocks }
    return { ...got, blocks: [...blocks, { name: BLOCK, text }] }
  })
}

// scopeChanged says whether the block the engine holds is out of date. The
// one unmatched prompt.submit hook (delivery.js) asks it before a message
// goes and refreshes the block, so that message's request carries the room
// as it is now. (The engine takes one unmatched hook per event per mod.)
export function scopeChanged() {
  return scope.last !== undefined && scopeText(room.snapshot) !== scope.last
}

export async function band(io, e, k) {
  return null
}

// onSnapshot refreshes the block at once when register.js offers the way to.
export function onSnapshot(io, snap, prev) {
  if (scope.last === undefined || typeof io.invalidateContext !== 'function') return
  const text = scopeText(snap)
  if (text === scope.last) return
  // Recorded now, so the message hook does not refresh the same change twice.
  scope.last = text
  io.invalidateContext()
}

export function start(io) {}

// scopeText is the block: one line per room that has a focus or a scope.
export function scopeText(snap) {
  const lines = []
  for (const r of snap.rooms ?? []) {
    const st = r.status
    if (!st) continue
    const focus = typeof st.focus === 'string' ? st.focus.trim() : ''
    const items = Array.isArray(st.scope) ? st.scope.map((s) => String(s).trim()).filter(Boolean) : []
    if (!focus && items.length === 0) continue
    const parts = []
    if (focus) parts.push('focus: ' + clip(focus, 120))
    if (items.length > 0) parts.push('scope: ' + clip(items.slice(0, 8).join(', '), 240))
    lines.push('- ' + roomName(r) + ': ' + parts.join('; '))
  }
  if (lines.length === 0) return ''
  return (
    'This session is attached to a Landfall war room. The people in it pinned the incident to the following; keep your work on it unless the person asks otherwise. ' +
    'Room news reaches you separately.\n' +
    lines.join('\n')
  )
}

// tick runs every TICK_MS while the session lives: a component with an open
// pane refreshes it here on its own cadence (nothing to do by default).
export function tick(io, nowMs) {}
