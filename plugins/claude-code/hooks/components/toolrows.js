// Landfall tool calls drawn as Landfall rows (proposal item 05).
//
// A `mcp__landfall__*` call shows by default as a generic tool row with raw
// text. These render hooks draw it as a compact branded row instead:
//
//   ToolUse     "◆ Landfall  query  cloudwatch 5xxErrorRate" (the call)
//   ToolResult  what came back, per tool:
//                 get_brief        the incident header, then established/open/people/focus
//                 query_signals    a sparkline (Raster on the terminal, Svg elsewhere),
//                                  max and last, and "4 add" while the chart key offers it
//                 share_with_room  what became of the share (sent, held, needs N, admitted)
//                 anything else    one line of what it said
//   ToolGroup   a folded run made only of Landfall calls: one line naming them
//
// DRAWING ONLY. Nothing here changes what the model read: the hooks draw the
// row and leave the stored result alone. An expanded group (ctrl+o, --verbose)
// draws the engine's own rows, and an errored or interrupted call keeps the
// engine's row, which says why.

import { clip, currentRoom, room, severityTone, statusTone } from '../core.js'
import { TONE, kit } from '../kit.js'
import { chart, chartLabel, pinChart } from './chart.js'

export const PREFIX = 'mcp__landfall__'
const LANDFALL_TOOL = /^mcp__landfall__/

// Calls drawn inside an expanded group: their rows are the engine's.
const expanded = new Set()
// Each call's input by tool_use_id, from its ToolUse row: a ToolResult
// carries only the output, and a share's row needs what was shared.
const inputs = new Map()
const INPUTS_MAX = 200

export function install(on) {
  on('ui.render', { component: 'ToolGroup' }, async ($, e, next) => {
    const calls = e.props.calls ?? []
    if (e.props.isExpanded) {
      for (const c of calls) if (c.tool_use_id && LANDFALL_TOOL.test(c.tool)) expanded.add(c.tool_use_id)
      while (expanded.size > INPUTS_MAX) expanded.delete(expanded.values().next().value)
      return next(e)
    }
    if (calls.length === 0 || !calls.every((c) => LANDFALL_TOOL.test(c.tool))) return next(e)
    if (calls.some((c) => c.isErrored || c.isInterrupted)) return next(e)
    return groupRow(kit($.ui.resolve(e), e), calls, e.props.isActive)
  })

  on('ui.render', { component: 'ToolUse', props: { tool: LANDFALL_TOOL } }, async ($, e, next) => {
    const p = e.props
    if (expanded.has(p.tool_use_id) || p.isErrored || p.isInterrupted) return next(e)
    remember(p.tool_use_id, p.input)
    return callRow(kit($.ui.resolve(e), e), p.tool, p.input, p.isRunning)
  })

  on('ui.render', { component: 'ToolResult', props: { tool: LANDFALL_TOOL } }, async ($, e, next) => {
    const p = e.props
    if (expanded.has(p.tool_use_id) || p.isErrored) return next(e)
    const text = resultText(p.output)
    if (!text) return next(e)
    const k = kit($.ui.resolve(e), e)
    const pin = () =>
      pinChart({
        process: (args, opts) => $.process.run(args, opts),
        toast: (t, ms) => $.ui.toast(t, { timeoutMs: ms }),
        invalidate: () => $.ui.invalidate('ui.render'),
      })
    return resultRow(k, p.tool, text, inputs.get(p.tool_use_id), p.tool_use_id, pin) ?? next(e)
  })
}

export async function band(io, e, k) {
  return null
}

export function onSnapshot(io, snap, prev) {}

export function start(io) {}

function remember(id, input) {
  if (!id || inputs.has(id)) return
  inputs.set(id, input)
  if (inputs.size > INPUTS_MAX) inputs.delete(inputs.keys().next().value)
}

// --- the call -----------------------------------------------------------

// VERBS name each tool by what it does, in one short word or two.
const VERBS = {
  get_brief: 'brief',
  query_signals: 'query',
  get_signal_catalog: 'catalog',
  share_with_room: 'share',
  join_war_room: 'join',
  get_updates: 'updates',
  read_timeline: 'timeline',
  search_context: 'search',
  read_artifact: 'artifact',
  upload_artifact: 'upload',
  propose_action: 'suggest',
  describe_widget_types: 'widget types',
}

export function toolName(tool) {
  return String(tool || '').slice(PREFIX.length)
}

export function verb(tool) {
  const name = toolName(tool)
  return VERBS[name] || name.replace(/_/g, ' ')
}

// callDetail is the call's subject in a few words: the metric read, the
// words shared, the query searched.
export function callDetail(tool, input) {
  const i = input && typeof input === 'object' ? input : {}
  switch (toolName(tool)) {
    case 'query_signals':
      if (i.source || i.operation) {
        const label = chartLabel({ source: i.source, operation: i.operation, params: i.params })
        return label === i.source + ' ' + i.operation ? label : (i.source ? i.source + ' ' : '') + label
      }
      return ''
    case 'share_with_room':
      return i.text ? '"' + clip(oneLine(i.text), 70) + '"' : i.kind ? String(i.kind) : ''
    case 'search_context':
      return i.query ? '"' + clip(oneLine(i.query), 60) + '"' : ''
    case 'propose_action':
      return i.title ? clip(oneLine(i.title), 70) : ''
    case 'read_artifact':
    case 'upload_artifact':
      return i.name || i.title ? clip(oneLine(i.name || i.title), 60) : ''
    default:
      return ''
  }
}

// head is "◆ Landfall  <verb>  <detail>": the Beacon mark off the terminal.
function head(k, key, tool, detail, running) {
  const { Text } = k.els
  return k.row(
    [
      k.mark(key + '-m', 16),
      Text({ key: key + '-b', bold: true, children: ['Landfall'] }),
      Text({ key: key + '-v', dimColor: true, children: [verb(tool)] }),
      detail ? Text({ key: key + '-d', children: [clip(detail, Math.max(20, k.width - 24))] }) : null,
      running ? Text({ key: key + '-r', dimColor: true, children: ['…'] }) : null,
    ],
    key,
    1,
  )
}

function callRow(k, tool, input, running) {
  return head(k, 'call', tool, callDetail(tool, input), running)
}

function groupRow(k, calls, active) {
  const counts = new Map()
  for (const c of calls) counts.set(verb(c.tool), (counts.get(verb(c.tool)) ?? 0) + 1)
  const said = [...counts].map(([v, n]) => (n > 1 ? v + ' ×' + n : v)).join(', ')
  return head(k, 'group', PREFIX + 'calls', said, active)
}

// --- the result ---------------------------------------------------------

// resultText is a tool's answer as text, whatever shape the row carries it in:
// a string, MCP content blocks, or { content } / { text }.
export function resultText(output) {
  if (output == null) return ''
  if (typeof output === 'string') return output
  if (Array.isArray(output)) return output.map((b) => (b && typeof b === 'object' && typeof b.text === 'string' ? b.text : typeof b === 'string' ? b : '')).filter(Boolean).join('\n')
  if (typeof output === 'object') {
    if (Array.isArray(output.content)) return resultText(output.content)
    if (typeof output.text === 'string') return output.text
    if (typeof output.result !== 'undefined') return resultText(output.result)
  }
  return ''
}

function resultRow(k, tool, text, input, id, pin) {
  switch (toolName(tool)) {
    case 'get_brief':
      return briefRow(k, text)
    case 'query_signals':
      return signalsRow(k, text, id, pin)
    case 'share_with_room':
      return shareRow(k, text, input)
    default:
      return plainRow(k, text)
  }
}

// body indents a result under its call, as the engine indents its own.
function body(k, children, key) {
  const { Box } = k.els
  return Box({ key, flexDirection: 'column', marginLeft: 2, children: children.filter(Boolean) })
}

function plainRow(k, text) {
  const first = oneLine(text.split('\n').find((l) => l.trim()) || '')
  if (!first) return null
  return body(k, [k.dim(clip(first, Math.max(20, k.width - 4)), 'said')], 'result')
}

// get_brief ----------------------------------------------------------------

// parseBrief reads narrate.RenderFrame's text: the head line
// ("<id> · <title> · <severity> · <status>"), the Established and Open lists,
// Participants and Focus.
export function parseBrief(text) {
  const lines = String(text).split('\n')
  const out = { head: [], established: 0, open: 0, people: 0, focus: '' }
  out.head = (lines[0] || '').split(' · ').map((x) => x.trim()).filter(Boolean)
  let section = ''
  for (const line of lines.slice(1)) {
    if (/^Established:$/.test(line)) section = 'established'
    else if (/^Open:$/.test(line)) section = 'open'
    else if (/^ {2}#\d+ /.test(line)) {
      if (section === 'established') out.established++
      if (section === 'open') out.open++
    } else if (/^Participants: /.test(line)) {
      section = ''
      out.people = line.slice('Participants: '.length).split(', ').filter(Boolean).length
    } else if (/^Focus: /.test(line)) {
      section = ''
      out.focus = line.slice('Focus: '.length).replace(/ \(asked by [^)]*\)$/, '')
    } else if (line.trim() === '' || !/^ {5}/.test(line)) {
      if (line.trim() !== '') section = ''
    }
  }
  return out
}

function briefRow(k, text) {
  const b = parseBrief(text)
  if (b.head.length === 0) return null
  const { Text } = k.els
  // The head's last two words are severity and status when they read as such.
  const words = [...b.head]
  const pills = []
  let status = ''
  let severity = ''
  if (words.length >= 3 && /^(triggered|open|investigating|identified|mitigat|monitor|resolved|closed|postmortem)/i.test(words[words.length - 1])) status = words.pop()
  if (words.length >= 2 && /^(sev\d|p\d|critical|high|medium|low)$/i.test(words[words.length - 1])) severity = words.pop()
  if (severity) pills.push(k.pill(severity, severityTone(severity), 'brief-sev'))
  if (status) pills.push(k.pill(status, statusTone(status), 'brief-st'))
  const facts = []
  facts.push('established ' + b.established)
  facts.push('open ' + b.open)
  const here = (currentRoom()?.status?.people ?? []).filter((p) => p.here).length
  if (here > 0) facts.push(here + ' here')
  else if (b.people > 0) facts.push(b.people + (b.people === 1 ? ' person' : ' people'))
  if (b.focus) facts.push('focus: ' + b.focus)
  const title = Text({ key: 'brief-t', bold: true, children: [clip(words.join(' '), Math.max(20, k.width - 30))] })
  return body(k, [k.row([title, ...pills], 'brief-h', 1), k.dim(facts.join(' · '), 'brief-f')], 'result')
}

// query_signals --------------------------------------------------------------

// parseSeries reads narrate.renderSeries' text: the head line, then per series
// "<name> (<unit>): N points; min a, max b, last c at t" and its listed points
// ("  15:04:05Z 0.25"; the middle of a long series is not listed).
export function parseSeries(text) {
  const lines = String(text).split('\n')
  const series = []
  let cur = null
  for (const line of lines.slice(1)) {
    const s = /^(.+?): ([\d,]+) points?; min (\S+), max (\S+), last (\S+) at (.+)$/.exec(line)
    if (s) {
      const unitM = /^(.*) \(([^)]+)\)$/.exec(s[1])
      cur = { name: unitM ? unitM[1] : s[1], unit: unitM ? unitM[2] : '', count: Number(s[2].replace(/,/g, '')), min: Number(s[3]), max: Number(s[4]), last: Number(s[5]), values: [] }
      series.push(cur)
      continue
    }
    const pt = /^ {2}(?:\d\d-\d\d )?\d\d:\d\d:\d\dZ (-?[\d.]+(?:e[-+]?\d+)?)$/.exec(line)
    if (pt && cur) cur.values.push(Number(pt[1]))
  }
  return { head: lines[0] || '', series }
}

function withUnit(v, unit) {
  if (!isFinite(v)) return ''
  if (unit === 'Percent' || unit === '%') return v + '%'
  return unit && unit !== 'None' && unit !== 'Count' ? v + ' ' + unit : String(v)
}

function signalsRow(k, text, id, pin) {
  const { series } = parseSeries(text)
  const s = series.find((x) => x.values.length >= 2) || series[0]
  const offer = chart.ready && chart.ready.toolUseId && chart.ready.toolUseId === id
  if (!s) {
    // A log read, a list: the answer's first line says what came back.
    return plainRow(k, text)
  }
  const facts = []
  facts.push((k.terminal ? '' : 'peak ') + withUnit(s.max, s.unit))
  facts.push('last ' + withUnit(s.last, s.unit))
  facts.push(s.count === 1 ? '1 point' : s.count + ' points')
  if (series.length > 1) facts.push('+' + (series.length - 1) + ' more series')
  const spark = k.spark(s.values, { key: 'sig-spark', tone: 'info', width: 24, px: k.mobile ? 200 : 320, height: 46, label: s.name })
  if (k.terminal) {
    return body(
      k,
      [
        k.row(
          [spark, k.text(facts.join(' · '), { key: 'sig-f' }), offer ? k.text('4 add', { key: 'sig-add', bold: true }) : null],
          'sig-row',
          2,
        ),
      ],
      'result',
    )
  }
  return body(
    k,
    [
      spark,
      k.row(
        [k.dim(facts.join(' · '), 'sig-f'), offer ? k.button({ key: 'sig-add', label: 'Add to room', onPress: pin }) : null],
        'sig-row',
        2,
      ),
    ],
    'result',
  )
}

// share_with_room -----------------------------------------------------------

// shareOutcome reads what became of a share: first from the room (the
// person's latest contribution and the votes on it, when the share is the one
// the watch stream names), else from the tool's own answer.
export function shareOutcome(text, input) {
  const t = String(text || '')
  if (/^held\b/i.test(t)) return { label: 'held on this machine', tone: 'warning', note: 'it names your working directory; landfall allow-cwd lets it through' }
  if (/^queued\b/i.test(t)) return { label: 'waiting on this machine', tone: 'warning', note: 'the room session expired; it goes out when you rejoin' }
  const fromRoom = roomState(input && input.text)
  if (fromRoom) return fromRoom
  const needs = /needs (\d+) more/i.exec(t)
  const seq = /#(\d+)/.exec(t)
  const note = [needs ? 'needs ' + needs[1] + ' more ' + (needs[1] === '1' ? 'position' : 'positions') : '', seq ? '#' + seq[1] : ''].filter(Boolean).join(' · ')
  if (/held for review|staged|awaiting/i.test(t)) return { label: 'held for review', tone: 'warning', note }
  if (/\badmitted\b/i.test(t)) return { label: 'admitted', tone: 'good', note }
  if (/redacted/i.test(t)) return { label: 'sent', tone: 'warning', note: 'a credential in it was replaced with [redacted]' }
  if (/^shared\b/i.test(t)) return { label: 'sent to the room', tone: 'info', note }
  return null
}

const STATE_WORDS = {
  staged: ['held for review', 'warning'],
  corroborated: ['corroborated', 'good'],
  contested: ['contested', 'critical'],
  admitted: ['admitted', 'good'],
  withdrawn: ['withdrawn', 'neutral'],
  note: ['shared as a note', 'info'],
}

// roomState is the room's word on a share this session made: the person's
// latest contribution when it is the same words, and the vote it waits on.
function roomState(said) {
  if (!said) return null
  const words = norm(said)
  for (const r of room.snapshot.rooms) {
    const me = (r.status?.people ?? []).find((p) => p.you)
    const latest = me && me.latest
    if (!latest || !latest.text || !sameWords(norm(latest.text), words)) continue
    const known = STATE_WORDS[latest.state]
    if (!known) continue
    const vote = (r.votes ?? []).find((v) => v.mine && v.statement && sameWords(norm(v.statement), words))
    const note = []
    if (vote && vote.shortfall > 0) note.push('needs ' + vote.shortfall + ' more ' + (vote.shortfall === 1 ? 'position' : 'positions'))
    if (latest.seq) note.push('#' + latest.seq)
    return { label: known[0], tone: known[1], note: note.join(' · ') }
  }
  return null
}

function norm(s) {
  return oneLine(s).toLowerCase()
}

// sameWords allows for the room clipping a long statement.
function sameWords(a, b) {
  if (!a || !b) return false
  if (a === b) return true
  const n = Math.min(a.length, b.length, 80)
  return n >= 20 && a.replace(/…$/, '').slice(0, n) === b.replace(/…$/, '').slice(0, n)
}

function shareRow(k, text, input) {
  const o = shareOutcome(text, input)
  if (!o) return plainRow(k, text)
  const { Text } = k.els
  const state = k.terminal ? Text({ key: 'share-s', color: toneColor(o.tone), children: [o.label] }) : k.pill(o.label, o.tone, 'share-s')
  const kids = []
  if (!k.terminal && input && input.text) kids.push(k.quote(clip(oneLine(input.text), Math.max(30, k.width - 6)), 'share-q'))
  kids.push(k.row([state, o.note ? k.dim(o.note, 'share-n') : null], 'share-row', 1))
  return body(k, kids, 'result')
}

function toneColor(tone) {
  return TONE[tone] || TONE.neutral
}

function oneLine(s) {
  return String(s ?? '').replace(/\s+/g, ' ').trim()
}

// tick runs every TICK_MS while the session lives: a component with an open
// pane refreshes it here on its own cadence (nothing to do by default).
export function tick(io, nowMs) {}
