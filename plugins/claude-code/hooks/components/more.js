// more.js: the More tab of the console (spec 4.11). It holds the chip state (comms or brain) and
// delegates the body to comms.js and brain.js. The chips are identical Buttons with letters in the
// Timeline idiom, `c: ▸ comms 2  b: brain`, the active one labeled `▸ <name>` at full strength and
// the other dim, never filled; More itself reads `▸ More` in the console's switcher whichever chip
// is shown. `/landfall comms`, `/landfall brain` and `/landfall brain <text>` open More on that
// chip.
//
// A letter typed into Brain's Search field does not fire a chip: an Input holding the focus takes
// every printable key alone (the engine's rule, claude-code.d.ts InputProps), so the chips keep
// their hotkeys.

import { consoleState } from '../core.js'
import * as brain from './brain.js'
import * as comms from './comms.js'
import { chipRow } from './tabparts.js'

const CHIPS = ['comms', 'brain']

// The chip last shown, remembered while the session lives.
const more = {
  chip: 'comms',
  argsApplied: '', // the words a `/landfall comms` or `/landfall brain <text>` already took
}

// No hooks of its own: the console owns the pane and the `/landfall` command.
export function install(on) {}

export async function band(io, e, k) {
  return null
}

export function onSnapshot(io, snap, prev) {}

export function start(io) {}

export function tick(io, nowMs) {}

// More has no badge of its own: the Comms count lives on its chip.
export function badge() {
  return null
}

// warm starts the shown chip's reads (Comms and Brain stay cold until first shown).
export async function warm(io) {
  if (more.chip === 'brain') await brain.warm(io)
  else await comms.warm(io)
}

// refresh reads the shown chip again (`r`).
export async function refresh(io) {
  await warm(io)
}

// The chips are the body's first row; Comms and Brain have no other letters.
export function keys(k, io, nowMs, args) {
  return []
}

// tab is More's body: the chip row, then the chosen tab's body as spec 4.7 or 4.8 draws it. `args`
// is what `/landfall` passed: `comms`, `brain`, or `brain <text>` (a string, or { chip, text }).
export function tab(k, io, nowMs, args) {
  applyArgs(io, args)
  const rows = [
    chipRow(
      k,
      [
        { key: 'more-comms', name: 'comms', hotkey: 'c', active: more.chip === 'comms', count: comms.count(), onPress: () => setChip(io, 'comms') },
        { key: 'more-brain', name: 'brain', hotkey: 'b', active: more.chip === 'brain', onPress: () => setChip(io, 'brain') },
      ],
      'more-chips',
    ),
  ]
  rows.push(...(more.chip === 'brain' ? brain.tab(k, io, nowMs, args) : comms.tab(k, io, nowMs, args)))
  return rows
}

// footer is the shown chip's footer; the console draws it once, under the body.
export function footer(k, nowMs) {
  return more.chip === 'brain' ? brain.footer(k, nowMs) : comms.footer(k, nowMs)
}

// text is More's answer where no pane can be placed (`claude -p`): the chip's own text.
export async function text(io, args) {
  const asked = parse(args)
  if (asked && asked.chip === 'brain') return brain.text(io, asked.text)
  if (asked && asked.chip === 'comms') return comms.text(io, '')
  return more.chip === 'brain' ? brain.text(io, '') : comms.text(io, '')
}

// parse reads `/landfall`'s words for More: { chip, text } or null.
function parse(args) {
  if (args == null || args === '') return null
  if (typeof args === 'object') {
    const chip = String(args.chip || '').toLowerCase()
    return CHIPS.includes(chip) ? { chip, text: String(args.text || '').trim() } : null
  }
  const words = String(args).trim()
  const first = words.split(/\s+/)[0].toLowerCase()
  if (!CHIPS.includes(first)) return null
  return { chip: first, text: words.slice(first.length).trim() }
}

// applyArgs takes `/landfall comms`, `brain` or `brain <text>` once: the chip is shown, and the
// text searched for, then not taken again on every draw (the person's own chip presses win).
function applyArgs(io, args) {
  if (args == null || args === '') {
    more.argsApplied = ''
    return
  }
  const asked = parse(args)
  if (consoleState.args === args) consoleState.args = null
  if (!asked) return
  const stamp = asked.chip + ' ' + asked.text
  if (stamp === more.argsApplied) return
  more.argsApplied = stamp
  more.chip = asked.chip
  if (asked.chip === 'brain') void (asked.text ? brain.search(io, asked.text) : brain.warm(io))
  else void comms.warm(io)
}

// setChip shows a chip, and starts its reads the first time it is shown.
async function setChip(io, chip) {
  if (more.chip === chip) return
  more.chip = chip
  io.invalidate()
  if (chip === 'brain') {
    if (!brain.readState().answer) await brain.warm(io)
  } else if (!comms.readState().answer) await comms.warm(io)
}
