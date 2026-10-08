// Company second brain lookup (proposal item 15, FR-15): /brain [text] shows
// past incidents like this one and what fixed them, with why the brain
// believes it, beside the live room. A press quotes a match into the prompt as
// a draft. Reads run `landfall brain`; with no text the CLI asks for incidents
// like the room's own, with text it searches the organization's memory.

import { HOST, addCommand, clip, currentRoom, parseAnswer, quoteDraft, room } from '../core.js'
import { kit } from '../kit.js'

export const PANE = 'landfall-brain'

// The last `landfall brain` answer, the text it searched for, and whether one is running.
const view = { answer: null, q: '', loading: false }

export function install(on) {
  addCommand({ name: 'brain', description: 'Ask the company second brain for past incidents like this one', argumentHint: '[what to look for]' })

  on('command.run', { command: 'brain' }, async ($, e) => {
    const opened = await $.ui.open({ id: PANE, title: 'Company second brain', focus: true, closeOnEscape: true })
    await load($, (e.args || '').trim())
    if (!opened.isPlaced) return { text: brainText(view.answer, view.q) }
    return {}
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const k = kit($.ui.resolve(e), e)
    const { Box, Text, Button, Input } = k.els
    const answer = view.answer
    const matches = answer && answer.ok ? answer.matches || [] : []

    const quote = async (m) => {
      await $.ui.close({ id: PANE })
      await $.prompt.fill({ text: quoteDraft(matchQuote(m)) })
    }

    const r = currentRoom()
    const asked = view.q ? 'for "' + clip(view.q, 40) + '"' : r ? 'like ' + (r.displayId || 'this incident') : ''
    const rows = [k.header({ key: 'br-hdr', title: 'Company second brain', pills: [], dim: view.loading ? 'reading…' : asked })]
    if (!answer) rows.push(Text({ key: 'br-wait', dimColor: true, children: ['Asking the company second brain…'] }))
    else if (!answer.ok) rows.push(Text({ key: 'br-err', children: [String(answer.error || 'The company second brain did not answer.')] }))
    else if (matches.length === 0) rows.push(Text({ key: 'br-none', dimColor: true, children: ['No past incident matches yet.'] }))

    matches.forEach((m, i) => {
      const id = 'm' + i
      const head = k.row(
        [
          Text({ key: 'bt-' + id, bold: true, children: [String(m.title || 'past incident')] }),
          m.incident ? Text({ key: 'bi-' + id, dimColor: true, children: [String(m.incident)] }) : null,
          m.confidence ? k.pill(String(m.confidence), confidenceTone(m.confidence), 'bc-' + id) : null,
        ],
        'bh-' + id,
        1,
      )
      const facts = []
      if (m.fix) facts.push(Text({ key: 'bf-' + id, children: ['Fix: ' + m.fix] }))
      if (m.why) facts.push(Text({ key: 'bw-' + id, dimColor: true, children: ['Why: ' + m.why] }))
      const press = Button({ key: 'quote-' + i, label: k.terminal ? 'quote it' : 'Quote into my prompt', ...(k.terminal ? { plain: true } : {}), ...(i < 9 ? { hotkey: String(i + 1) } : {}), onPress: () => quote(m) })
      if (k.terminal) {
        rows.push(head)
        rows.push(Box({ key: 'bb-' + id, paddingLeft: 2, flexDirection: 'column', children: [...facts, press] }))
        return
      }
      rows.push(Box({ key: 'card-' + id, flexDirection: 'column', rowGap: 1, borderStyle: 'round', borderColor: '#d9d9d6', paddingX: 1, children: [head, ...facts, press] }))
    })

    if (hasFields(k)) {
      rows.push(Input({ key: 'brain-q', label: 'Search', placeholder: 'origin pool exhaustion', submitLabel: 'search', value: view.q, onSubmit: (value) => load($, String(value || '').trim()) }))
    }
    rows.push(
      k.row(
        [
          k.button({ key: 'refresh', label: 'refresh', hotkey: 'r', onPress: () => load($, view.q) }),
          k.button({ key: 'close', label: k.terminal ? 'close (esc)' : 'Close', dim: true, onPress: () => $.ui.close({ id: PANE }) }),
        ],
        'br-keys',
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

// load runs `landfall brain` for the room, with the text when there is some.
async function load($, q) {
  view.q = q
  view.loading = true
  $.ui.invalidate('ui.render')
  const r = currentRoom()
  const args = ['brain']
  if (r) args.push('--room', r.roomKey)
  if (q) args.push('--q', q)
  view.answer = await cli($, args)
  view.loading = false
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

export function confidenceTone(confidence) {
  const c = String(confidence || '').toLowerCase()
  if (/establish|confirm|high/.test(c)) return 'good'
  if (/likely|probable|medium/.test(c)) return 'info'
  if (/contest|disput/.test(c)) return 'critical'
  return 'neutral'
}

// matchQuote is a match as the person's draft quotes it.
export function matchQuote(m) {
  const out = ['Past incident: ' + (m.title || 'untitled') + (m.incident ? ' (' + m.incident + ')' : '')]
  if (m.fix) out.push('Fix: ' + m.fix)
  if (m.why) out.push('Why: ' + m.why)
  if (m.confidence) out.push('Confidence: ' + m.confidence)
  return out.join('\n')
}

// brainText is /brain where no pane can be drawn.
export function brainText(answer, q) {
  if (!answer) return 'The company second brain did not answer.'
  if (!answer.ok) return String(answer.error || 'The company second brain did not answer.')
  const matches = answer.matches || []
  if (matches.length === 0) return q ? 'The company second brain has nothing on "' + q + '" yet.' : 'No past incident matches yet.'
  const out = []
  for (const m of matches) {
    out.push([m.title || 'past incident', m.incident, m.confidence].filter(Boolean).join(' · '))
    if (m.fix) out.push('  Fix: ' + m.fix)
    if (m.why) out.push('  Why: ' + m.why)
  }
  return out.join('\n')
}

// hasFields: the surface draws an Input (mobile draws no field yet).
function hasFields(k) {
  return k.surface !== 'mobile' && typeof k.els.Input === 'function'
}
