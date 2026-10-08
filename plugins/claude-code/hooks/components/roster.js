// Who is with me (proposal item 03, FR-03): /who lists each person in the
// room, where they work, what their agents are doing, and their latest
// contribution to the shared context with where it stands (staged,
// corroborated, contested, admitted, withdrawn) and its age. A press quotes it
// into the prompt as a draft; nothing is sent.
//
// It reads `landfall watch` (rooms[0].status.people[].latest), so it needs no
// command of its own and redraws with every snapshot.
//
// THE ROOM PANE (room.js, stream M1) can show the same thing in its people
// rows with the pure helpers below: under personLine(p), when p.latest is set,
// one more row `latestWords(p.latest)` drawn in TONE[latestTone(p.latest.state)]
// (or k.pill(stateWord(state), latestTone(state)) plus the quote on a remote
// surface), as a plain Button whose press is io.fill(quoteDraft(p.latest.text)).

import { addCommand, ago, clip, currentRoom, quoteDraft, roomName, whereIs } from '../core.js'
import { kit, TONE } from '../kit.js'

export const PANE = 'landfall-who'

export function install(on) {
  addCommand({ name: 'who', description: 'Who is in the war room, and what each person last added to the shared context' })

  on('command.run', { command: 'who' }, async ($) => {
    const r = currentRoom()
    if (!r) return { text: 'This folder is not in a war room. Open a share link from the room, or run /incidents to join one.' }
    const opened = await $.ui.open({ id: PANE, title: 'Who is here', focus: true, closeOnEscape: true })
    if (!opened.isPlaced) return { text: rosterText(r) }
    return {}
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const k = kit($.ui.resolve(e), e)
    const { Box, Text, Button } = k.els
    const r = currentRoom()
    const people = sortPeople((r && r.status && r.status.people) || [])
    const here = people.filter((p) => p.here).length

    const quote = async (text) => {
      await $.ui.close({ id: PANE })
      await $.prompt.fill({ text: quoteDraft(text) })
    }

    const st = (r && r.status) || {}
    const dim = r ? [r.displayId, st.severity].filter(Boolean).join(' · ') : ''
    const rows = [k.header({ key: 'who-hdr', title: 'In the room', pills: [{ text: here + ' here', tone: here > 0 ? 'good' : 'neutral' }], dim })]
    if (!r) rows.push(Text({ key: 'who-none', dimColor: true, children: ['This folder is not in a war room.'] }))
    else if (people.length === 0) rows.push(Text({ key: 'who-empty', dimColor: true, children: ['Nobody is in the room yet.'] }))

    let digit = 0
    people.forEach((p, i) => {
      const id = 'p' + i
      const name = p.name + (p.you ? ' (you)' : '')
      const via = viaLine(p)
      const l = p.latest
      const hotkey = l && l.text && digit < 9 ? String(++digit) : undefined
      const quoteButton = l && l.text
        ? Button({ key: 'quote-' + i, label: '"' + clip(l.text, Math.max(20, k.width - (k.terminal ? 8 : 2))) + '"', ...(k.terminal ? { plain: true } : {}), ...(hotkey ? { hotkey } : {}), onPress: () => quote(l.text) })
        : null
      const stateRow = l
        ? k.row([k.pill(stateWord(l.state), latestTone(l.state), 'ls-' + id), Text({ key: 'la-' + id, dimColor: true, children: [seqAge(l)] })], 'lr-' + id, 1)
        : Text({ key: 'ln-' + id, dimColor: true, children: ['Nothing shared to the context yet'] })

      if (k.terminal) {
        rows.push(
          k.row(
            [
              Text({ key: 'dot-' + id, color: p.here ? TONE.good : TONE.neutral, children: [p.here ? '●' : '○'] }),
              Text({ key: 'nm-' + id, bold: true, dimColor: !p.here, children: [name] }),
              via ? Text({ key: 'via-' + id, dimColor: true, children: [clip(via, Math.max(10, k.width - name.length - 4))] }) : null,
            ],
            'pr-' + id,
            1,
          ),
        )
        rows.push(Box({ key: 'pl-' + id, paddingLeft: 2, flexDirection: 'column', children: [stateRow, quoteButton].filter(Boolean) }))
        return
      }
      // Desktop, VS Code, mobile: an avatar card.
      rows.push(
        Box({
          key: 'card-' + id,
          flexDirection: 'column',
          rowGap: 1,
          borderStyle: 'round',
          borderColor: '#d9d9d6',
          paddingX: 1,
          children: [
            k.row(
              [
                k.svg(avatarSvg(p), { key: 'av-' + id, alt: p.name + (p.here ? ', here' : ', away'), width: 30, height: 30 }),
                k.col([Text({ key: 'nm-' + id, bold: true, dimColor: !p.here, children: [name] }), via ? Text({ key: 'via-' + id, dimColor: true, children: [via] }) : null], 'nv-' + id),
              ],
              'ph-' + id,
              1,
            ),
            stateRow,
            quoteButton,
          ].filter(Boolean),
        }),
      )
    })

    if (digit > 0) rows.push(Text({ key: 'who-hint', dimColor: true, children: ['Press a contribution to quote it into your prompt.'] }))
    rows.push(k.row([k.button({ key: 'close', label: k.terminal ? 'close (esc)' : 'Close', dim: true, onPress: () => $.ui.close({ id: PANE }) })], 'who-keys'))
    return Box({ flexDirection: 'column', rowGap: k.terminal ? 0 : 1, children: rows })
  })
}

export async function band(io, e, k) {
  return null
}

export function onSnapshot(io, snap, prev) {}

export function start(io) {}

// The order people are listed in: those here before those away, you last
// among them (you know what you said), then by name.
export function sortPeople(people) {
  const rank = (p) => (p.here ? 0 : 2) + (p.you ? 1 : 0)
  return [...people].sort((a, b) => rank(a) - rank(b) || String(a.name).localeCompare(String(b.name)))
}

// viaLine is where a person works and what their agents are doing:
// "Claude Code · comparing 5xx by region". A tool is named once however many
// sessions of it the person runs, and the same doing is said once.
export function viaLine(p) {
  const out = []
  const where = whereIs(p)
  if (where) out.push(where)
  else if (!p.here) out.push('away')
  for (const a of p.agents ?? []) if (a.here && a.doing && !out.includes(a.doing)) out.push(a.doing)
  return out.join(' · ')
}

// The words and tone for where a contribution stands.
const STATES = {
  staged: { word: 'staged', tone: 'warning' },
  corroborated: { word: 'corroborated', tone: 'info' },
  contested: { word: 'contested', tone: 'critical' },
  admitted: { word: 'admitted', tone: 'good' },
  withdrawn: { word: 'withdrawn', tone: 'neutral' },
  note: { word: 'note', tone: 'neutral' },
}

export function stateWord(state) {
  return (STATES[state] && STATES[state].word) || String(state || 'shared')
}

export function latestTone(state) {
  return (STATES[state] && STATES[state].tone) || 'neutral'
}

// seqAge is "#205 · 3m".
export function seqAge(latest) {
  return [latest.seq != null ? '#' + latest.seq : '', ago(latest.ageMs)].filter(Boolean).join(' · ')
}

// latestWords is a contribution on one line: 'admitted · #205 · 3m: "Rollback ready"'.
export function latestWords(latest, width = 120) {
  if (!latest || !latest.text) return ''
  const head = [stateWord(latest.state), seqAge(latest)].filter(Boolean).join(' · ')
  return head + ': "' + clip(latest.text, Math.max(10, width - head.length - 4)) + '"'
}

// rosterText is /who where no pane can be drawn.
export function rosterText(r) {
  const people = sortPeople((r.status && r.status.people) || [])
  const out = [roomName(r) + ' · ' + people.filter((p) => p.here).length + ' here']
  for (const p of people) {
    const via = viaLine(p)
    out.push('  ' + (p.here ? '● ' : '○ ') + p.name + (p.you ? ' (you)' : '') + (via ? ' · ' + via : ''))
    out.push('    ' + (p.latest && p.latest.text ? latestWords(p.latest) : 'Nothing shared to the context yet'))
  }
  return out.join('\n')
}

const AVATAR = ['#8a5cd6', '#2a78d6', '#c2551f', '#2e7d46', '#9a6b00', '#5a5a5a', '#b03a6f', '#1f7a8c']

// avatarSvg is a round avatar with the person's initial and a presence dot.
export function avatarSvg(p) {
  let h = 0
  for (const c of String(p.name || '?')) h = (h * 31 + c.charCodeAt(0)) >>> 0
  const fill = p.you ? '#141414' : AVATAR[h % AVATAR.length]
  const initial = esc((String(p.name || '?').trim()[0] || '?').toUpperCase())
  return (
    '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 30 30" width="30" height="30">' +
    '<circle cx="15" cy="15" r="14" fill="' + fill + '"' + (p.here ? '' : ' fill-opacity=".45"') + '/>' +
    '<text x="15" y="19.5" text-anchor="middle" font-family="-apple-system,Segoe UI,sans-serif" font-size="13" font-weight="700" fill="#fff">' + initial + '</text>' +
    '<circle cx="25" cy="25" r="4.2" fill="' + (p.here ? TONE.good : TONE.neutral) + '" stroke="#fff" stroke-width="1.6"/>' +
    '</svg>'
  )
}

function esc(s) {
  return String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c])
}

// tick runs every TICK_MS while the session lives: a component with an open
// pane refreshes it here on its own cadence (nothing to do by default).
export function tick(io, nowMs) {}

// THE CONSOLE CONTRACT (core.js CONSOLE): console.js calls these by name. Stubs until this tab is built.
export function tab(k, io, nowMs, args) {
  return []
}

export function badge() {
  return null
}

export function warm(io) {}
