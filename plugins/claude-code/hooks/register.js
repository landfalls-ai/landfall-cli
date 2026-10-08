// Landfall's Claude Code mod: the war room, drawn in the session it reaches.
//
// THE RULE IT KEEPS. Landfall never takes a turn the person did not ask for
// (internal/hooks/stopnotice.go). A mod runs inside Claude Code, so it can show
// room news the moment the room daemon has it and leave every turn to the
// person: keys draft prompts (never send them), and every action a key takes
// runs the landfall CLI, never an MCP call (core.js runJSON).
//
// DELIVERY TO THE AGENT IS UNCHANGED (components/delivery.js). ONE VOICE PER
// SESSION: at start the mod sets LANDFALL_MOD, and the Landfall settings hooks
// then stay quiet (internal/cli/hookevents.go modOwnsEvent).
//
// THE LAYOUT. core.js holds the room state, the CLI runner and the helpers;
// kit.js draws Landfall's pieces for each surface (terminal, desktop, vscode,
// mobile); each file in components/ is one feature with the same four exports
// (see core.js, THE COMPONENT CONTRACT). This file is the only one that calls
// them, each by name, because the engine's validator follows `$` only into
// named functions. Adding a component is one import line and one line in each
// list below.

import { HOST, declaredCommands, parseAnswer, room } from './core.js'
import { kit } from './kit.js'
import { install as installRoom, band as bandRoom, onSnapshot as onSnapshotRoom, start as startRoom, tick as tickRoom } from './components/room.js'
import { install as installChart, band as bandChart, onSnapshot as onSnapshotChart, start as startChart, tick as tickChart } from './components/chart.js'
import { install as installDelivery, band as bandDelivery, onSnapshot as onSnapshotDelivery, start as startDelivery, tick as tickDelivery } from './components/delivery.js'
import { install as installToolRows, band as bandToolRows, onSnapshot as onSnapshotToolRows, start as startToolRows, tick as tickToolRows } from './components/toolrows.js'
import { install as installScope, band as bandScope, onSnapshot as onSnapshotScope, start as startScope, tick as tickScope } from './components/scope.js'
import { install as installSound, band as bandSound, onSnapshot as onSnapshotSound, start as startSound, tick as tickSound } from './components/sound.js'
import { install as installVote, band as bandVote, onSnapshot as onSnapshotVote, start as startVote, tick as tickVote } from './components/vote.js'
import { install as installBeacon, band as bandBeacon, onSnapshot as onSnapshotBeacon, start as startBeacon, tick as tickBeacon } from './components/beacon.js'
import { install as installIncidents, band as bandIncidents, onSnapshot as onSnapshotIncidents, start as startIncidents, tick as tickIncidents } from './components/incidents.js'
import { install as installRoster, band as bandRoster, onSnapshot as onSnapshotRoster, start as startRoster, tick as tickRoster } from './components/roster.js'
import { install as installLines, band as bandLines, onSnapshot as onSnapshotLines, start as startLines, tick as tickLines } from './components/lines.js'
import { install as installComms, band as bandComms, onSnapshot as onSnapshotComms, start as startComms, tick as tickComms } from './components/comms.js'
import { install as installBrain, band as bandBrain, onSnapshot as onSnapshotBrain, start as startBrain, tick as tickBrain } from './components/brain.js'
import { install as installWall, band as bandWall, onSnapshot as onSnapshotWall, start as startWall, tick as tickWall } from './components/wall.js'
import { install as installLb, band as bandLb, onSnapshot as onSnapshotLb, start as startLb, tick as tickLb } from './components/lb.js'
import { install as installTopology, band as bandTopology, onSnapshot as onSnapshotTopology, start as startTopology, tick as tickTopology } from './components/topology.js'
import { install as installTimeline, band as bandTimeline, onSnapshot as onSnapshotTimeline, start as startTimeline, tick as tickTimeline } from './components/timeline.js'

export function register(on, options) {
  // Components first: install() declares their commands, which the
  // session.start hook below registers.
  installRoom(on)
  installChart(on)
  installDelivery(on)
  installToolRows(on)
  installScope(on)
  installSound(on, options)
  installVote(on)
  installBeacon(on)
  installIncidents(on)
  installRoster(on)
  installLines(on)
  installComms(on)
  installBrain(on)
  installWall(on)
  installLb(on)
  installTopology(on)
  installTimeline(on)

  on('session.start', async ($, e, next) => {
    room.bin = (await $.env.get('LANDFALL_BIN')) || 'landfall'
    await $.env.set('LANDFALL_MOD', HOST)
    for (const c of declaredCommands()) await $.command.register(c)
    await startAll($)
    startWatch($)
    $.clock.every(TICK_MS, () => tickAll($))
    return next(e)
  })

  // The band above the prompt: every component's rows, in this order.
  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    const k = kit($.ui.resolve(e), e)
    const rows = await bandAll($, e, k)
    if (rows.length === 0) return next(e)
    const mine = await next(e)
    return k.els.Box({ flexDirection: 'column', children: [mine, ...rows].filter(Boolean) })
  })
}

async function startAll($) {
  const io = makeIo($, null)
  try {
    await startRoom(io)
  } catch {
    // One component's start never stops the session's.
  }
  try {
    await startChart(io)
  } catch {
    // One component's start never stops the session's.
  }
  try {
    await startDelivery(io)
  } catch {
    // One component's start never stops the session's.
  }
  try {
    await startToolRows(io)
  } catch {
    // One component's start never stops the session's.
  }
  try {
    await startScope(io)
  } catch {
    // One component's start never stops the session's.
  }
  try {
    await startSound(io)
  } catch {
    // One component's start never stops the session's.
  }
  try {
    await startVote(io)
  } catch {
    // One component's start never stops the session's.
  }
  try {
    await startBeacon(io)
  } catch {
    // One component's start never stops the session's.
  }
  try {
    await startIncidents(io)
  } catch {
    // One component's start never stops the session's.
  }
  try {
    await startRoster(io)
  } catch {
    // One component's start never stops the session's.
  }
  try {
    await startLines(io)
  } catch {
    // One component's start never stops the session's.
  }
  try {
    await startComms(io)
  } catch {
    // One component's start never stops the session's.
  }
  try {
    await startBrain(io)
  } catch {
    // One component's start never stops the session's.
  }
  try {
    await startWall(io)
  } catch {
    // One component's start never stops the session's.
  }
  try {
    await startLb(io)
  } catch {
    // One component's start never stops the session's.
  }
  try {
    await startTopology(io)
  } catch {
    // One component's start never stops the session's.
  }
  try {
    await startTimeline(io)
  } catch {
    // One component's start never stops the session's.
  }
}

// The band's order: what asks the person for something first.
async function bandAll($, e, k) {
  const io = makeIo($, e.surface)
  const rows = []
  try {
    const got = await bandVote(io, e, k)
    if (got && got.length > 0) rows.push(...got)
  } catch {
    // One section failing never takes the band down.
  }
  try {
    const got = await bandBeacon(io, e, k)
    if (got && got.length > 0) rows.push(...got)
  } catch {
    // One section failing never takes the band down.
  }
  try {
    const got = await bandRoom(io, e, k)
    if (got && got.length > 0) rows.push(...got)
  } catch {
    // One section failing never takes the band down.
  }
  try {
    const got = await bandLines(io, e, k)
    if (got && got.length > 0) rows.push(...got)
  } catch {
    // One section failing never takes the band down.
  }
  try {
    const got = await bandChart(io, e, k)
    if (got && got.length > 0) rows.push(...got)
  } catch {
    // One section failing never takes the band down.
  }
  return rows
}


// startWatch runs `landfall watch` for the session's life and starts it again
// when it ends.
function startWatch($) {
  if (room.watching) return
  room.watching = true
  void (async () => {
    let buffered = ''
    try {
      const child = $.process.spawn({ argv: [room.bin, 'watch', '--host', HOST] })
      for await (const piece of child) {
        if (piece.stream !== 'stdout') continue
        buffered += piece.text
        let nl = buffered.indexOf('\n')
        while (nl >= 0) {
          applySnapshot($, buffered.slice(0, nl))
          buffered = buffered.slice(nl + 1)
          nl = buffered.indexOf('\n')
        }
      }
    } catch {
      // landfall is missing or could not start; try again below.
    }
    room.watching = false
    $.clock.after(RESTART_MS, () => startWatch($))
  })()
}

const RESTART_MS = 3000

// TICK_MS is the session clock components refresh open panes on (tick).
const TICK_MS = 5000

// tickAll gives every component the clock: live panes refresh on their own
// cadence (the wall's data every 15 s, as the war room's own canvas does).
async function tickAll($) {
  const io = makeIo($, null)
  const nowMs = Number(await $.clock.now())
  try {
    await tickRoom(io, nowMs)
  } catch {
    // One component's tick never stops the others.
  }
  try {
    await tickChart(io, nowMs)
  } catch {
    // One component's tick never stops the others.
  }
  try {
    await tickDelivery(io, nowMs)
  } catch {
    // One component's tick never stops the others.
  }
  try {
    await tickToolRows(io, nowMs)
  } catch {
    // One component's tick never stops the others.
  }
  try {
    await tickScope(io, nowMs)
  } catch {
    // One component's tick never stops the others.
  }
  try {
    await tickSound(io, nowMs)
  } catch {
    // One component's tick never stops the others.
  }
  try {
    await tickVote(io, nowMs)
  } catch {
    // One component's tick never stops the others.
  }
  try {
    await tickBeacon(io, nowMs)
  } catch {
    // One component's tick never stops the others.
  }
  try {
    await tickIncidents(io, nowMs)
  } catch {
    // One component's tick never stops the others.
  }
  try {
    await tickRoster(io, nowMs)
  } catch {
    // One component's tick never stops the others.
  }
  try {
    await tickLines(io, nowMs)
  } catch {
    // One component's tick never stops the others.
  }
  try {
    await tickComms(io, nowMs)
  } catch {
    // One component's tick never stops the others.
  }
  try {
    await tickBrain(io, nowMs)
  } catch {
    // One component's tick never stops the others.
  }
  try {
    await tickWall(io, nowMs)
  } catch {
    // One component's tick never stops the others.
  }
  try {
    await tickLb(io, nowMs)
  } catch {
    // One component's tick never stops the others.
  }
  try {
    await tickTopology(io, nowMs)
  } catch {
    // One component's tick never stops the others.
  }
  try {
    await tickTimeline(io, nowMs)
  } catch {
    // One component's tick never stops the others.
  }
}

// applySnapshot takes one line of the stream, tells every component, redraws.
function applySnapshot($, line) {
  let next
  try {
    next = JSON.parse(line)
  } catch {
    return
  }
  if (!next || next.type !== 'rooms' || !Array.isArray(next.rooms)) return
  const prev = room.snapshot
  room.snapshot = { line: next.line || '', rooms: next.rooms }
  const io = makeIo($, null)
  try {
    onSnapshotRoom(io, room.snapshot, prev)
  } catch {
    // One component's listener never stops the others.
  }
  try {
    onSnapshotChart(io, room.snapshot, prev)
  } catch {
    // One component's listener never stops the others.
  }
  try {
    onSnapshotDelivery(io, room.snapshot, prev)
  } catch {
    // One component's listener never stops the others.
  }
  try {
    onSnapshotToolRows(io, room.snapshot, prev)
  } catch {
    // One component's listener never stops the others.
  }
  try {
    onSnapshotScope(io, room.snapshot, prev)
  } catch {
    // One component's listener never stops the others.
  }
  try {
    onSnapshotSound(io, room.snapshot, prev)
  } catch {
    // One component's listener never stops the others.
  }
  try {
    onSnapshotVote(io, room.snapshot, prev)
  } catch {
    // One component's listener never stops the others.
  }
  try {
    onSnapshotBeacon(io, room.snapshot, prev)
  } catch {
    // One component's listener never stops the others.
  }
  try {
    onSnapshotIncidents(io, room.snapshot, prev)
  } catch {
    // One component's listener never stops the others.
  }
  try {
    onSnapshotRoster(io, room.snapshot, prev)
  } catch {
    // One component's listener never stops the others.
  }
  try {
    onSnapshotLines(io, room.snapshot, prev)
  } catch {
    // One component's listener never stops the others.
  }
  try {
    onSnapshotComms(io, room.snapshot, prev)
  } catch {
    // One component's listener never stops the others.
  }
  try {
    onSnapshotBrain(io, room.snapshot, prev)
  } catch {
    // One component's listener never stops the others.
  }
  try {
    onSnapshotWall(io, room.snapshot, prev)
  } catch {
    // One component's listener never stops the others.
  }
  try {
    onSnapshotLb(io, room.snapshot, prev)
  } catch {
    // One component's listener never stops the others.
  }
  try {
    onSnapshotTopology(io, room.snapshot, prev)
  } catch {
    // One component's listener never stops the others.
  }
  try {
    onSnapshotTimeline(io, room.snapshot, prev)
  } catch {
    // One component's listener never stops the others.
  }
  $.ui.invalidate('ui.render')
}

// makeIo is what components get in place of `$`: each closure spells `$`
// itself, so the validator sees every call where it is made.
function makeIo($, surface) {
  return {
    surface: surface || 'terminal',
    fill: (text) => $.prompt.fill({ text }),
    suggest: (text) => $.prompt.suggest({ text }),
    toast: (text, ms) => $.ui.toast(text, ms ? { timeoutMs: ms } : undefined),
    status: (text) => $.ui.status(text),
    open: (id, title, opts) => $.ui.open({ id, title, ...(opts || {}) }),
    close: (id) => $.ui.close({ id }),
    invalidate: () => $.ui.invalidate('ui.render'),
    process: (args, opts) => $.process.run(args, opts),
    run: async (args, opts) => {
      try {
        return parseAnswer(await $.process.run([room.bin, ...args], { timeoutMs: 20000, ...(opts || {}) }))
      } catch (err) {
        return { ok: false, error: String(err).slice(0, 200) }
      }
    },
    play: (asset) => $.audio.play({ asset }),
    storeGet: (key) => $.store.get(key),
    storeSet: (key, value) => $.store.set(key, value),
    copy: (text) => $.ui.copy({ text, surface: surface || undefined }),
    now: () => $.clock.now(),
    invalidateContext: () => $.ui.invalidate('prompt.context'),
    // A user-role row the model reads and the person does not see as typed; no turn starts.
    // Answers the deny reason, or null once stored.
    append: async (text) => {
      try {
        const got = await $.session.append({ message: { type: 'user', content: [{ type: 'text', text }] } })
        return got && got.deny ? String(got.deny) : null
      } catch (err) {
        return String(err).slice(0, 200)
      }
    },
  }
}
