// Lines of investigation (proposal item 13, FR-13): who is on which lead, so
// two people do not chase the same one. The band shows one dim row while the
// room has claimed lines; /lines lists them, claims one ("I'm on eu-west-1")
// and releases your own. Claims and releases run the landfall CLI as the
// person (`landfall lines claim|release`); the list rides on `landfall watch`
// (status.lines).

import { HOST, addCommand, ago, clip, currentRoom, parseAnswer, room } from '../core.js'
import { kit } from '../kit.js'

export const PANE = 'landfall-lines'

// Whether the pane shows its claim field, and a claim or release in flight.
const ui = { claiming: false, busy: '' }

export function install(on) {
  addCommand({ name: 'lines', description: 'See who is on which line of investigation, and claim one', argumentHint: '[the line you are on]' })

  on('command.run', { command: 'lines' }, async ($, e) => {
    const r = currentRoom()
    if (!r) return { text: 'This folder is not in a war room. Open a share link from the room, or run /incidents to join one.' }
    const label = (e.args || '').trim()
    if (label) {
      // `/lines eu-west-1 5xx` claims at once: the one way to claim where no field draws.
      const got = await cli($, ['lines', 'claim', '--room', r.roomKey, '--label', label])
      return { text: claimWords(got, label) }
    }
    ui.claiming = false
    const opened = await $.ui.open({ id: PANE, title: 'Lines of investigation', focus: true, closeOnEscape: true })
    if (!opened.isPlaced) return { text: linesText(r) }
    return {}
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const k = kit($.ui.resolve(e), e)
    const { Box, Text, Input } = k.els
    const r = currentRoom()
    const lines = roomLines(r)

    const claim = async (label) => {
      label = String(label || '').trim()
      if (!label || !r) return
      ui.busy = 'claim'
      $.ui.invalidate('ui.render')
      const got = await cli($, ['lines', 'claim', '--room', r.roomKey, '--label', label])
      ui.busy = ''
      if (got.ok) ui.claiming = false
      await $.ui.toast(clip(claimWords(got, label), 220), { timeoutMs: got.ok ? 6000 : 8000 })
      $.ui.invalidate('ui.render')
    }
    const release = async (line) => {
      if (!r || !line.claimId) return
      ui.busy = line.claimId
      $.ui.invalidate('ui.render')
      const got = await cli($, ['lines', 'release', '--room', r.roomKey, '--claim', line.claimId])
      ui.busy = ''
      if (got.ok) await $.ui.toast(got.note ? String(got.note) : 'You released the line ' + lineLabel(line) + '.', { timeoutMs: 6000 })
      else await $.ui.toast('Line not released: ' + clip(got.error || 'no answer', 200), { timeoutMs: 8000 })
      $.ui.invalidate('ui.render')
    }
    const openClaim = async () => {
      ui.claiming = true
      $.ui.invalidate('ui.render')
    }

    const rows = [k.header({ key: 'ln-hdr', title: 'Lines of investigation', pills: [{ text: String(lines.length), tone: 'neutral' }], dim: r ? r.displayId || '' : '' })]
    if (!r) rows.push(Text({ key: 'ln-none', dimColor: true, children: ['This folder is not in a war room.'] }))
    else if (lines.length === 0) rows.push(Text({ key: 'ln-empty', dimColor: true, children: ['Nobody has claimed a line yet.'] }))

    lines.forEach((line, i) => {
      const id = line.claimId || 'l' + i
      const owner = line.you ? 'you' : line.owner || 'someone'
      const words = [owner, ago(line.ageMs)].filter(Boolean).join(' · ')
      const kids = [
        Text({ key: 'lt-' + id, bold: !!line.you, children: [clip(lineLabel(line), Math.max(10, k.width - words.length - 16))] }),
        Text({ key: 'lo-' + id, dimColor: true, children: [words] }),
      ]
      if (line.you && line.claimId) {
        kids.push(k.button({ key: 'release-' + id, label: ui.busy === line.claimId ? 'releasing…' : 'release', onPress: () => release(line) }))
      }
      rows.push(k.row(kids, 'lr-' + id, 2))
    })

    const keys = []
    if (r && hasFields(k)) {
      if (ui.claiming) {
        rows.push(
          Input({
            key: 'claim-label',
            label: 'Line',
            placeholder: 'eu-west-1 5xx',
            submitLabel: 'claim',
            autoFocus: true,
            onSubmit: (value) => claim(value),
          }),
        )
      } else {
        keys.push(k.button({ key: 'claim', label: ui.busy === 'claim' ? 'claiming…' : 'claim a line…', hotkey: 'n', primary: true, onPress: openClaim }))
      }
    } else if (r) {
      rows.push(Text({ key: 'ln-mobile', dimColor: true, children: ['To claim a line here, type /lines and what you are on.'] }))
    }
    keys.push(k.button({ key: 'close', label: k.terminal ? 'close (esc)' : 'Close', dim: true, onPress: () => $.ui.close({ id: PANE }) }))
    rows.push(k.row(keys, 'ln-keys'))
    return Box({ flexDirection: 'column', rowGap: k.terminal ? 0 : 1, children: rows })
  })
}

// band: one dim row while the room has claimed lines.
export async function band(io, e, k) {
  const words = linesWords(currentRoom())
  if (!words) return null
  return [k.text(clip('Lines: ' + words, k.width), { key: 'lines', dimColor: true })]
}

export function onSnapshot(io, snap, prev) {}

export function start(io) {}

// cli runs `landfall <args> --host claude-code` and reads its one JSON line.
async function cli($, args) {
  try {
    return parseAnswer(await $.process.run([room.bin, ...args, '--host', HOST], { timeoutMs: 20000 }))
  } catch (err) {
    return { ok: false, error: clip(String(err), 200) }
  }
}

// claimWords is what a claim's answer says to the person. When someone else
// already holds the line, the CLI's sentence names them and is said as it is
// ("dave already holds this line. Help them, or claim another.").
export function claimWords(got, label) {
  if (got.ok) return 'You claimed the line ' + (got.label || label) + '. The room sees it.'
  if (got.heldBy) return String(got.error || got.heldBy + ' already holds this line. Help them, or claim another.')
  return 'Line not claimed: ' + (got.error || 'no answer')
}

// roomLines is the room's claimed lines from `landfall watch`.
export function roomLines(r) {
  const lines = r && r.status && Array.isArray(r.status.lines) ? r.status.lines : []
  return lines.filter((l) => l && lineLabel(l))
}

// lineLabel reads the label (review #5) or the first contract's `line`.
export function lineLabel(l) {
  return String(l.label || l.line || l.lineKey || '')
}

// linesWords is the band's row: "eu-west-1 5xx (dave) · origin pool (you)".
export function linesWords(r) {
  return roomLines(r)
    .map((l) => lineLabel(l) + ' (' + (l.you ? 'you' : l.owner || 'someone') + ')')
    .join(' · ')
}

// linesText is /lines where no pane can be drawn.
export function linesText(r) {
  const lines = roomLines(r)
  if (lines.length === 0) return 'Nobody has claimed a line yet. Type /lines and what you are on to claim one.'
  const out = ['Lines of investigation']
  for (const l of lines) out.push('  ' + lineLabel(l) + ' · ' + [l.you ? 'you' : l.owner || 'someone', ago(l.ageMs)].filter(Boolean).join(' · '))
  return out.join('\n')
}

// hasFields: the surface draws an Input (mobile draws no field yet).
function hasFields(k) {
  return k.surface !== 'mobile' && typeof k.els.Input === 'function'
}
