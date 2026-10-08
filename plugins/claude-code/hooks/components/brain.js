// Company second brain lookup (proposal item 15, FR-15): /brain [text] shows
// what the brain holds that bears on the live room: each entry's title, its
// summary, what kind of knowledge it is and how settled, how sure the brain is,
// and the past incidents it came from. A press quotes a match into the prompt
// as a draft. Reads run `landfall brain`; with no text the CLI asks for
// entries like the room's own incident, with text it searches the
// organization's memory. The CLI maps only fields an entry really carries
// (review #10): there is no "fix" or "why" on an entry, so none is drawn.

import { HOST, addCommand, clip, currentRoom, parseAnswer, quoteDraft, room } from '../core.js'
import { kit } from '../kit.js'

export const PANE = 'landfall-brain'

// The last `landfall brain` answer, the text it searched for, and whether one is running.
const view = { answer: null, q: '', loading: false }

export function install(on) {
  addCommand({ name: 'brain', description: 'Ask the company second brain what it knows that bears on this incident', argumentHint: '[what to look for]' })

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
    else if (matches.length === 0) rows.push(Text({ key: 'br-none', dimColor: true, children: [view.q ? 'The company second brain has nothing on "' + clip(view.q, 40) + '" yet.' : 'The company second brain holds nothing on this incident yet.'] }))

    matches.forEach((m, i) => {
      const id = 'm' + i
      const kind = kindWords(m)
      const head = k.row(
        [
          Text({ key: 'bt-' + id, bold: true, children: [String(m.title || 'past incident')] }),
          kind ? k.pill(kind, 'neutral', 'bk-' + id) : null,
          m.confidence ? k.pill(String(m.confidence), confidenceTone(m.confidence), 'bc-' + id) : null,
          m.status ? k.pill(String(m.status), statusTone(m.status), 'bs-' + id) : null,
        ],
        'bh-' + id,
        1,
      )
      const facts = []
      if (m.summary) facts.push(Text({ key: 'bf-' + id, children: [String(m.summary)] }))
      const from = fromWords(m)
      if (from) facts.push(Text({ key: 'bi-' + id, dimColor: true, children: [from] }))
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

// statusTone: an entry the brain no longer stands behind says so.
export function statusTone(status) {
  const s = String(status || '').toLowerCase()
  if (/conflict|contest/.test(s)) return 'critical'
  return 'warning'
}

// kindWords is the kind pill: "lesson · settled", "fact", or "".
export function kindWords(m) {
  return [m.kind, m.maturity].filter(Boolean).join(' · ')
}

// UUID-like ids are not names a person reads; display ids ("Acme 91") are.
const OPAQUE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

// fromWords names the incidents an entry came from: "From Acme 91, Acme 95",
// or "From 2 past incidents" when the CLI has only their ids.
export function fromWords(m) {
  const list = Array.isArray(m.incidents) && m.incidents.length > 0 ? m.incidents : m.incident ? [m.incident] : []
  if (list.length === 0) return ''
  const named = list.filter((x) => !OPAQUE.test(String(x)))
  if (named.length === list.length) return 'From ' + named.slice(0, 4).join(', ') + (named.length > 4 ? ' and ' + (named.length - 4) + ' more' : '')
  return 'From ' + list.length + (list.length === 1 ? ' past incident' : ' past incidents')
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
  const out = ['From the company second brain: ' + (m.title || 'untitled')]
  if (m.summary) out.push(m.summary)
  const from = fromWords(m)
  if (from) out.push(from)
  const sure = [kindWords(m), m.confidence ? 'confidence ' + m.confidence : '', m.status || ''].filter(Boolean).join(' · ')
  if (sure) out.push(sure)
  return out.join('\n')
}

// brainText is /brain where no pane can be drawn.
export function brainText(answer, q) {
  if (!answer) return 'The company second brain did not answer.'
  if (!answer.ok) return String(answer.error || 'The company second brain did not answer.')
  const matches = answer.matches || []
  if (matches.length === 0) return q ? 'The company second brain has nothing on "' + q + '" yet.' : 'The company second brain holds nothing on this incident yet.'
  const out = []
  for (const m of matches) {
    out.push([m.title || 'past incident', kindWords(m), m.confidence, m.status].filter(Boolean).join(' · '))
    if (m.summary) out.push('  ' + m.summary)
    const from = fromWords(m)
    if (from) out.push('  ' + from)
  }
  return out.join('\n')
}

// hasFields: the surface draws an Input (mobile draws no field yet).
function hasFields(k) {
  return k.surface !== 'mobile' && typeof k.els.Input === 'function'
}
