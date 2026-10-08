// Beacon's run, live, one line above the prompt (proposal item 09).
//
// While Beacon investigates, the band carries its latest `agent.step`
// narration and step number (watch `status.beaconStep`). When the run
// concludes, the line turns into its conclusion (`status.beaconConclusion`)
// with key 9, which drafts a prompt asking for it (never sent), until the
// person presses it or ten minutes pass. On a surface that draws vectors the
// row carries a step track: one dot per step seen, the current one in violet.

import { clip, currentRoom, roomName } from '../core.js'
import { TONE } from '../kit.js'

// A conclusion is offered this long after the mod first draws it.
export const CONCLUSION_MS = 10 * 60 * 1000

// Per room: the newest conclusion seq the person has already been shown or
// that predates the session (the first sight is a baseline, not news).
let seenSeq = {}
// Per room: the conclusion on offer, { seq, text, at } (`at` set when first drawn).
let offer = {}
// Per room: the step texts seen in the live run, { runSeq, texts: { [step]: text } }.
let runs = {}

export function install(on) {}

// band: "◆ Beacon step 6 · <text>" while a run is live; after a conclusion,
// "◆ Beacon concluded <text>" with key 9.
export async function band(io, e, k) {
  const r = currentRoom()
  if (!r || !r.status) return null
  const st = r.status
  const { Text } = k.els
  const violet = TONE.violet
  if (st.beaconStep && st.beaconStep.text) {
    const s = st.beaconStep
    const words = 'Beacon step ' + (s.step ?? '?') + ' · ' + s.text
    if (!k.rich) return [Text({ key: 'beacon-step', color: violet, children: [clip('◆ ' + words, k.width)] })]
    return [k.row([k.mark('beacon-m'), Text({ key: 'beacon-step', color: violet, children: [clip(words, Math.max(20, k.width - 16))] }), stepTrack(k, r.roomKey, s)], 'beacon-row', 1)]
  }
  const o = offer[r.roomKey]
  if (!o) return null
  const now = await nowOf(io)
  if (o.at == null) o.at = now
  if (now - o.at >= CONCLUSION_MS) {
    delete offer[r.roomKey]
    return null
  }
  const read = k.button({
    key: 'beacon-read',
    label: 'read the conclusion',
    hotkey: '9',
    onPress: async () => {
      delete offer[r.roomKey]
      await io.fill("Read me Beacon's conclusion in " + roomName(r) + ': ' + o.text)
      io.invalidate()
    },
  })
  const label = k.rich ? 'Beacon concluded' : '◆ Beacon concluded'
  return [
    k.row(
      [
        k.rich ? k.mark('beacon-m') : null,
        Text({ key: 'beacon-done', color: violet, bold: true, children: [label] }),
        Text({ key: 'beacon-text', children: [clip(o.text, Math.max(20, k.width - 42))] }),
        read,
      ],
      'beacon-row',
      1,
    ),
  ]
}

// onSnapshot keeps the run's step texts for the track and offers each new
// conclusion once.
export function onSnapshot(io, snap) {
  for (const r of snap.rooms) {
    const st = r.status
    if (!st) continue
    const s = st.beaconStep
    if (s && s.text) {
      const run = runs[r.roomKey]
      if (!run || run.runSeq !== s.runSeq) runs[r.roomKey] = { runSeq: s.runSeq, texts: {} }
      if (s.step != null) runs[r.roomKey].texts[s.step] = s.text
    }
    const c = st.beaconConclusion
    const first = !(r.roomKey in seenSeq)
    if (first) {
      seenSeq[r.roomKey] = c && c.seq != null ? c.seq : -1
      continue
    }
    if (c && c.seq != null && c.seq > seenSeq[r.roomKey] && c.text) {
      seenSeq[r.roomKey] = c.seq
      offer[r.roomKey] = { seq: c.seq, text: c.text, at: null }
      delete runs[r.roomKey]
    }
  }
}

export function start(io) {}

// nowOf reads the session clock where register.js hands one over (io.now),
// the module's own otherwise.
async function nowOf(io) {
  try {
    if (typeof io.now === 'function') return Number(await io.now())
  } catch {
    // fall through to the module clock
  }
  return Date.now()
}

// stepTrack is one dot per step of the live run: done steps green, the
// current one violet, each titled with its narration where it was seen.
function stepTrack(k, roomKey, s) {
  const n = Math.max(1, Math.min(Number(s.step) || 1, 24))
  const texts = (runs[roomKey] && runs[roomKey].texts) || {}
  const gap = 14
  let svg = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ' + n * gap + ' 12" width="' + n * gap + '" height="12">'
  for (let i = 1; i <= n; i++) {
    const cx = (i - 1) * gap + 6
    if (i > 1) svg += '<line x1="' + (cx - gap + 5) + '" x2="' + (cx - 5) + '" y1="6" y2="6" stroke="#8a8a8a" stroke-opacity=".4"/>'
    const color = i === n ? TONE.violet : TONE.good
    const title = texts[i] ? '<title>' + esc('Step ' + i + ': ' + texts[i]) + '</title>' : ''
    svg += '<circle cx="' + cx + '" cy="6" r="' + (i === n ? 5 : 4) + '" fill="' + color + '">' + title + '</circle>'
  }
  svg += '</svg>'
  return k.svg(svg, { key: 'beacon-track', alt: 'Beacon at step ' + n, width: n * gap, height: 12, hover: true })
}

function esc(s) {
  return String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c])
}

// tick runs every TICK_MS while the session lives: a component with an open
// pane refreshes it here on its own cadence (nothing to do by default).
export function tick(io, nowMs) {}
