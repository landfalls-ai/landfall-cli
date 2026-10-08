// Topology pane for the room's graph widget: /topology (proposal item 12, FR-12).
//
// Reads the wall (`landfall wall`, through the person's own session) and
// draws its first `graph` widget: on the desktop an Svg with trust as line
// style (solid confirmed, dashed established, dotted inferred) and unhealthy
// nodes in their tone; on the terminal a tree list with the trust words,
// since box drawing for a real graph is unreadable.
//
// While open it follows the wall's events (live.md FR-L2): a widget event on
// the watch stream (`widgetSeq`) reads the wall again. From a CLI that does
// not send `widgetSeq`, any new room event does, at most every 15 s. A failed
// read keeps the last good graph and says it is stale.

import { HOST, addCommand, currentRoom, parseAnswer, room, roomName } from '../core.js'
import { kit } from '../kit.js'
import { closed, drawn, due, liveFooter, livePane, readLive, WALL_MS, widgetSeqOf, opened as markOpen } from '../live.js'
import { clipText, graphLines, graphView } from '../views.js'

export const PANE = 'landfall-topology'

// The pane's live read (live.js). `lp.seq` is the room's widgetSeq (or, from
// an older CLI, its maxSeq) when the wall was last read.
const lp = livePane()

export function install(on) {
  addCommand({ name: 'topology', description: "Show the war room's topology" })

  on('command.run', { command: 'topology' }, async ($) => {
    const r = currentRoom()
    const opened = await $.ui.open({ id: PANE, title: r && r.displayId ? 'Topology · ' + r.displayId : 'Topology', focus: true, closeOnEscape: true })
    if (opened && opened.isPlaced) markOpen(lp)
    await loadTopology(paneIo($))
    if (!opened || !opened.isPlaced) return { text: topologyText(lp.last) }
    return {}
  })

  // The person's close (esc, the close mark) reaches this hook; the mod's own
  // closes go through closePane, since a plugin's own $.ui.close is not
  // raised to its own hooks.
  on('ui.close', { id: PANE }, async ($, e, next) => {
    closed(lp)
    return next(e)
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const k = kit($.ui.resolve(e), e)
    drawn(lp)
    let nowMs = lp.goodAt
    try {
      nowMs = Number(await $.clock.now())
    } catch {
      // No clock: the age reads as of the last read.
    }
    const r = currentRoom()
    const a = lp.answer
    const g = graphOf(a)
    const rows = [k.header({ key: 'topo-h', title: g && g.title ? g.title : 'Topology', dim: [r ? roomName(r) : '', lp.inFlight ? 'reading…' : ''].filter(Boolean).join(' · ') })]
    if (!a) rows.push(k.text(lp.inFlight ? 'Reading the wall through your session…' : 'Press r to read the topology.', { key: 'topo-empty', dimColor: true }))
    else if (!a.ok) rows.push(k.text(clipText(a.error || 'The wall could not be read.', k.width), { key: 'topo-err' }))
    else if (!g) rows.push(k.text(NO_GRAPH, { key: 'topo-none', dimColor: true }))
    else {
      rows.push(...graphView(k, g, 'topo'))
      if (!k.rich) rows.push(k.text('confirmed, established and inferred say how sure the room is of each link', { key: 'topo-legend', dimColor: true }))
    }
    const foot = liveFooter(k, lp, nowMs, r, 'topo-live')
    if (foot) rows.push(foot)
    rows.push(
      k.row(
        [
          k.button({ key: 'topo-refresh', label: 'refresh', hotkey: 'r', onPress: () => loadTopology(paneIo($)) }),
          k.button({ key: 'topo-close', label: 'close (esc)', dim: true, onPress: () => closePane($) }),
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

// onSnapshot: an open topology reads the wall again when a widget event
// lands, or, from a CLI without widgetSeq, when the room moved on and the
// last read is 15 s old.
export function onSnapshot(io, snap, prev) {
  if (!lp.open) return
  const r = (snap.rooms || [])[0]
  if (!r) return
  const ws = widgetSeqOf(r)
  if (ws != null) {
    if (ws > lp.seq) void loadTopology(io)
    return
  }
  if (typeof r.maxSeq === 'number' && r.maxSeq > lp.seq) void loadAfterGap(io)
}

async function loadAfterGap(io) {
  if (due(lp, await ioNow(io), WALL_MS)) await loadTopology(io)
}

export function start(io) {}

// tick moves the age under an open topology; a widget event the snapshot
// could not read (a read was running) is read here.
export async function tick(io, nowMs) {
  if (!lp.open) return
  io.invalidate()
  const r = currentRoom()
  const ws = widgetSeqOf(r)
  if (ws != null && ws > lp.seq && !lp.inFlight) void loadTopology(io)
}

const NO_GRAPH = 'The wall has no topology yet. Ask your agent or Beacon to map the services this incident touches.'

// loadTopology reads the wall (`landfall wall`), one read at a time.
async function loadTopology(io) {
  const fetch = () => {
    const r = currentRoom()
    const ws = widgetSeqOf(r)
    lp.seq = ws != null ? ws : r && typeof r.maxSeq === 'number' ? r.maxSeq : -1
    io.invalidate()
    const args = ['wall', '--host', HOST]
    if (r && r.roomKey) args.push('--room', r.roomKey)
    return io.run(args, { timeoutMs: 30000 })
  }
  await readLive(lp, fetch, () => ioNow(io))
  io.invalidate()
}

// closePane closes the pane and stops its reads.
async function closePane($) {
  closed(lp)
  await $.ui.close({ id: PANE })
}

// paneIo is what this file's reads take, from the hook's own `$`, shaped as
// register.js shapes `io`.
function paneIo($) {
  return {
    run: async (args, opts) => {
      try {
        return parseAnswer(await $.process.run([room.bin, ...args], { timeoutMs: 30000, ...(opts || {}) }))
      } catch (err) {
        return { ok: false, error: String(err).slice(0, 200) }
      }
    },
    invalidate: () => $.ui.invalidate('ui.render'),
    now: () => $.clock.now(),
  }
}

async function ioNow(io) {
  try {
    return Number(await io.now())
  } catch {
    return lp.triedAt
  }
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
