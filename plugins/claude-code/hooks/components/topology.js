// Topology pane for the room's graph widget: /topology (proposal item 12, FR-12).
//
// Reads the wall (`landfall wall`, through the person's own session) and
// draws its first `graph` widget: on the desktop an Svg with trust as line
// style (solid confirmed, dashed established, dotted inferred) and unhealthy
// nodes in their tone; on the terminal a tree list with the trust words,
// since box drawing for a real graph is unreadable.

import { HOST, addCommand, currentRoom, parseAnswer, room, roomName } from '../core.js'
import { kit } from '../kit.js'
import { clipText, graphLines, graphView } from '../views.js'

export const PANE = 'landfall-topology'

const topo = { answer: null, loading: false }

export function install(on) {
  addCommand({ name: 'topology', description: "Show the war room's topology" })

  on('command.run', { command: 'topology' }, async ($) => {
    const r = currentRoom()
    const opened = await $.ui.open({ id: PANE, title: r && r.displayId ? 'Topology · ' + r.displayId : 'Topology', focus: true, closeOnEscape: true })
    await loadTopology($)
    if (!opened || !opened.isPlaced) return { text: topologyText(topo.answer) }
    return {}
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const k = kit($.ui.resolve(e), e)
    const r = currentRoom()
    const a = topo.answer
    const g = graphOf(a)
    const rows = [k.header({ key: 'topo-h', title: g && g.title ? g.title : 'Topology', dim: [r ? roomName(r) : '', topo.loading ? 'reading…' : ''].filter(Boolean).join(' · ') })]
    if (!a) rows.push(k.text(topo.loading ? 'Reading the wall through your session…' : 'Press r to read the topology.', { key: 'topo-empty', dimColor: true }))
    else if (!a.ok) rows.push(k.text(clipText(a.error || 'The wall could not be read.', k.width), { key: 'topo-err' }))
    else if (!g) rows.push(k.text(NO_GRAPH, { key: 'topo-none', dimColor: true }))
    else {
      rows.push(...graphView(k, g, 'topo'))
      if (!k.rich) rows.push(k.text('confirmed, established and inferred say how sure the room is of each link', { key: 'topo-legend', dimColor: true }))
    }
    rows.push(
      k.row(
        [
          k.button({ key: 'topo-refresh', label: 'refresh', hotkey: 'r', onPress: () => loadTopology($) }),
          k.button({ key: 'topo-close', label: 'close (esc)', dim: true, onPress: () => $.ui.close({ id: PANE }) }),
        ],
        'topo-keys',
      ),
    )
    return k.col(rows, 'topo')
  })
}

export async function band(io, e, k) {
  return null
}

export function onSnapshot(io, snap, prev) {}

export function start(io) {}

const NO_GRAPH = 'The wall has no topology yet. Ask your agent or Beacon to map the services this incident touches.'

async function loadTopology($) {
  if (topo.loading) return
  topo.loading = true
  $.ui.invalidate('ui.render')
  const r = currentRoom()
  const args = ['wall', '--host', HOST]
  if (r && r.roomKey) args.push('--room', r.roomKey)
  try {
    topo.answer = parseAnswer(await $.process.run([room.bin, ...args], { timeoutMs: 30000 }))
  } catch (err) {
    topo.answer = { ok: false, error: String(err).slice(0, 200) }
  }
  topo.loading = false
  $.ui.invalidate('ui.render')
}

// graphOf is the wall's first graph widget.
export function graphOf(a) {
  if (!a || !a.ok) return null
  const graphs = (a.widgets || []).filter((w) => w.type === 'graph')
  return graphs.find((w) => !w.empty && (w.nodes || []).length > 0) || graphs[0] || null
}

// topologyText is /topology's answer where no pane can be drawn.
export function topologyText(a) {
  if (!a) return 'The wall could not be read.'
  if (!a.ok) return a.error || 'The wall could not be read.'
  const g = graphOf(a)
  if (!g) return NO_GRAPH
  return [g.title || 'Topology', ...graphLines(g.nodes, g.edges).map((l) => '  ' + l)].join('\n')
}

// tick runs every TICK_MS while the session lives: a component with an open
// pane refreshes it here on its own cadence (nothing to do by default).
export function tick(io, nowMs) {}
