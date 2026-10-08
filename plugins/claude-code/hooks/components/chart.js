// Widgets without a turn: when the agent reads a metric through the room, the
// band offers to put that read on the room's canvas as a chart. One key (4) or
// /chart, no model call, nothing more to approve: `landfall chart` has the
// person's room daemon make the same read and queues the chart, so no points
// are copied by anyone.

import { HOST, addCommand, clip, currentRoom, room, roomArgs } from '../core.js'

// The agent's latest metric read that can become a chart: { query, label,
// toolUseId } (the call's id, so its tool row can offer the same key).
export const chart = { ready: null, pinning: false }

export function install(on) {
  addCommand({ name: 'chart', description: "Add your agent's latest metric read to the war room as a chart" })

  on('command.run', { command: 'chart' }, async ($) => {
    if (!chart.ready) return { text: 'No metric read to chart yet. Ask your agent to read a metric from the room, then run /chart.' }
    await pinChart({
      process: (args, opts) => $.process.run(args, opts),
      toast: (text, ms) => $.ui.toast(text, { timeoutMs: ms }),
      invalidate: () => $.ui.invalidate('ui.render'),
    })
    return {}
  })

  on('tool.call', { tool: 'mcp__landfall__query_signals' }, async ($, e, next) => {
    const result = await next(e)
    try {
      if (!result || result.deny || result.isError) return result
      const m = /: (\d+) series, (\d+) points?/.exec(JSON.stringify(result.result ?? ''))
      if (m && Number(m[2]) > 0 && e.source && e.operation) {
        const query = { source: e.source, operation: e.operation }
        if (e.params) query.params = e.params
        if (e.connection) query.connection = e.connection
        if (e.account) query.account = e.account
        chart.ready = { query, label: chartLabel(e), toolUseId: e.tool_use_id }
        $.ui.invalidate('ui.render')
      }
    } catch {
      // Reading the result for a chart offer must never change the tool's answer.
    }
    return result
  })

}

// band: 'Chart ready: <metric>' and key 4.
export async function band(io, e, k) {
  const c = chart.ready
  if (!c) return null
  if (!k.terminal) {
    // Off the terminal: one real button that names the metric.
    return [
      k.row(
        [
          k.mark('chart-m', 14),
          k.dim('Chart ready', 'chart-l'),
          k.button({ key: 'pin', label: clip('Add chart: ' + c.label, k.mobile ? 36 : 60), hotkey: '4', onPress: () => pinChart(io) }),
        ],
        'chart-row',
        1,
      ),
    ]
  }
  return [
    k.row(
      [
        k.text(clip('Chart ready: ' + c.label, Math.max(20, k.width - 30)), { key: 'chart-l' }),
        k.button({ key: 'pin', label: 'add it to the room', hotkey: '4', onPress: () => pinChart(io) }),
      ],
      'chart-row',
    ),
  ]
}

export function onSnapshot(io, snap, prev) {}

export function start(io) {}

// pinChart runs `landfall chart`: a local command, not an MCP call, since
// Claude Code puts a plugin's MCP call made from a key press to the permission
// dialog, and the key press is all the asking this needs. `io` is register.js's
// io, or the command's own { run, toast, invalidate }.
export async function pinChart(io) {
  const c = chart.ready
  if (!c || chart.pinning) return
  chart.pinning = true
  chart.ready = null
  io.invalidate()
  try {
    const run = await io.process([room.bin, 'chart', '--host', HOST, ...roomArgs(currentRoom()), '--query', JSON.stringify({ ...c.query, title: c.label })], {
      timeoutMs: 40000,
    })
    let answer = {}
    try {
      answer = JSON.parse((run.stdout || '').trim().split('\n').pop() || '{}')
    } catch {
      answer = { ok: false, error: (run.stderr || run.stdout || 'no answer').trim().slice(0, 200) }
    }
    if (answer.ok) io.toast('Chart added to your dashboard in the room: ' + (answer.title || c.label), 6000)
    else io.toast('Chart not added: ' + clip(String(answer.error || 'no answer'), 200), 8000)
  } catch (err) {
    io.toast('Chart not added: ' + String(err).slice(0, 200), 8000)
  }
  chart.pinning = false
}

// chartLabel names a read by the metric it asked for, wherever the source keeps it.
export function chartLabel(e) {
  const name = findValue(e.params, ['MetricName', 'metricName', 'metric', 'name'])
  return name ? String(name) : e.source + ' ' + e.operation
}

function findValue(obj, keys, depth = 0) {
  if (!obj || typeof obj !== 'object' || depth > 5) return undefined
  for (const k of keys) if (typeof obj[k] === 'string' && obj[k]) return obj[k]
  for (const v of Object.values(obj)) {
    const found = findValue(v, keys, depth + 1)
    if (found) return found
  }
  return undefined
}

// tick runs every TICK_MS while the session lives: a component with an open
// pane refreshes it here on its own cadence (nothing to do by default).
export function tick(io, nowMs) {}
