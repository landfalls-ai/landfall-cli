// Small drawing parts the console's detail tabs share (vote, timeline, load balancers, more,
// comms, brain). Pure: nothing here holds `$`, and none of it is a component, so register.js
// never calls it. kit.js stays as it is; what a tab needs beyond it lives here.

import { clip } from '../core.js'
import { TONE, cap } from '../kit.js'
import { livePane, liveWords, notLive, since } from '../live.js'

// keyButton is a Button the way the console draws every key: a plain `k: label` on the terminal,
// a native Button elsewhere (`primary` fills it), and `dim` dims the label on BOTH surfaces
// (kit.button dims on the terminal only), so an inactive chip reads the same everywhere.
export function keyButton(k, { key, label, hotkey, onPress, primary, dim }) {
  const props = { key, label: k.terminal ? label : cap(label), onPress }
  if (hotkey) props.hotkey = hotkey
  if (k.terminal) props.plain = true
  else if (primary) props.variant = 'primary'
  if (dim) props.dimColor = true
  return k.els.Button(props)
}

// cap lives in kit.js; re-exported so the tabs keep importing it from here.
export { cap }

// chipRow is a row of identical Buttons with letters, the active one labeled `▸ <name>` at full
// strength and the others dim (the Timeline idiom; More uses it too). `chips` are
// { key, name, hotkey, active, onPress, count? }; the desktop capitalizes the name.
export function chipRow(k, chips, key) {
  return k.row(
    chips.map((c) => {
      const name = k.terminal ? c.name : cap(c.name)
      const label = (c.active ? '▸ ' : '') + name + (c.count ? ' ' + c.count : '')
      return keyButton(k, { key: c.key, label, hotkey: c.hotkey, onPress: c.onPress, dim: !c.active })
    }),
    key,
    // A native chip carries its own padding: a one-cell gap keeps the six Timeline chips on one row
    // of the docked pane (round 2 review, issue 13).
    k.terminal ? 2 : 1,
  )
}

// footerOf is the shared footer of §4.9 for one live read: `live · updated 4s ago`, with
// `· reading…` while a read runs over a good answer, `stale · …` in warning when the last read
// failed, and `updated 2m ago` (no `live`) while the room's connection is down. Null before the
// first good read.
export function footerOf(k, lp, nowMs, r, key = 'tab-live') {
  if (notLive(r)) {
    if (!lp.answer || !lp.answer.ok) return null
    return k.text('updated ' + since(Math.max(0, nowMs - lp.goodAt)) + ' ago', { key, dimColor: true })
  }
  const w = liveWords(lp, nowMs, r)
  if (!w) return null
  if (w.tone === 'neutral') return k.text(w.text + (lp.inFlight ? ' · reading…' : ''), { key, dimColor: true })
  return k.text(w.text, { key, color: TONE[w.tone] })
}

// nowOf is the session clock's time through io, or `fallback` when io has none.
export async function nowOf(io, fallback) {
  try {
    return Number(await io.now())
  } catch {
    return fallback
  }
}

// TOAST_MAX is the longest toast the console says: two rows of the engine's 40-cell toast box
// (round 4 review), the outcome and the thing named in the first 40.
export const TOAST_MAX = 80

// say is io.toast with the copy held to TOAST_MAX, so a long name or the CLI's own sentence never
// pushes the outcome out of the box.
export function say(io, text, ms) {
  return io.toast(clip(String(text ?? ''), TOAST_MAX), ms)
}

// centerRow is k.row with its children centered on the cross axis off the terminal, so a status
// label (a tinted box at the Button's height) sits on its neighbors' text baseline.
export function centerRow(k, children, key, gap = 2) {
  return k.els.Box({ key, flexDirection: 'row', columnGap: gap, flexWrap: 'wrap', ...(k.terminal ? {} : { alignItems: 'center' }), children: children.filter(Boolean) })
}

// resetLive forgets what a live read answered (another room, a new session) and keeps whether the
// pane is open, so the next read starts clean.
export function resetLive(lp) {
  const { open, closed } = lp
  Object.assign(lp, livePane(), { open, closed })
}

// roomKeyOf is the key of the room the console is about, or '' when there is none.
export function roomKeyOf(r) {
  return r && r.roomKey ? r.roomKey : ''
}
