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
// mobile); console.js is the one command, `/landfall`, and the one pane, the
// console, whose tabs are the components' `tab()` (see console.js); each file in
// components/ is one feature with the same five exports (see core.js, THE
// COMPONENT CONTRACT). This file is the only one that calls them, each by name,
// because the engine's validator follows `$` only into named functions. Adding a
// component is one import line and one line in each list below.

import { HOST, declaredCommands, parseAnswer, room, severityTone } from './core.js'
import { kit } from './kit.js'
import { notLive } from './live.js'
import { bandHead, bandHasNews, bandKeys, bandNews, bandRoom as roomOfBand } from './components/room.js'
import { chart } from './components/chart.js'
import { REMOVED } from './console.js'
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
import { install as installTimeline, band as bandTimeline, onSnapshot as onSnapshotTimeline, start as startTimeline, tick as tickTimeline } from './components/timeline.js'
import { install as installContext, band as bandContext, onSnapshot as onSnapshotContext, start as startContext, tick as tickContext } from './components/context.js'
import { install as installMore, band as bandMore, onSnapshot as onSnapshotMore, start as startMore, tick as tickMore } from './components/more.js'
import { install as installConsole, band as bandConsole, onSnapshot as onSnapshotConsole, start as startConsole, tick as tickConsole } from './console.js'

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
  installTimeline(on)
  installContext(on)
  installMore(on)
  installConsole(on)

  on('session.start', async ($, e, next) => {
    room.bin = (await $.env.get('LANDFALL_BIN')) || 'landfall'
    await $.env.set('LANDFALL_MOD', HOST)
    // One command, /landfall (spec §1.3): the commands the console replaced are not registered.
    for (const c of declaredCommands()) if (!REMOVED.includes(c.name)) await $.command.register(c)
    await startAll($)
    startWatch($)
    $.clock.every(TICK_MS, () => tickAll($))
    return next(e)
  })

  // The band above the prompt (spec §5.1): what needs the person, and one key to the console.
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
    await startTimeline(io)
  } catch {
    // One component's start never stops the session's.
  }
  try {
    await startContext(io)
  } catch {
    // One component's start never stops the session's.
  }
  try {
    await startMore(io)
  } catch {
    // One component's start never stops the session's.
  }
  try {
    await startConsole(io)
  } catch {
    // One component's start never stops the session's.
  }
}

// The band's order (§5.1): the head (the room, its news count, the vote waiting, the agent),
// the vote's statement and keys, Beacon, the news or new-widget row, then the keys row. A row
// with nothing to say is left out; with nothing at all the band draws nothing. Off the
// terminal the rows sit in one card, its border critical while SEV1.
async function bandAll($, e, k) {
  const io = makeIo($, e.surface)
  const body = []
  try {
    const got = await bandVote(io, e, k)
    // The vote's head row is the head's label now (§5.1: the head says `● vote waiting 4m 10s`;
    // the band is the statement and the keys), so it is left out here.
    if (got && got.length > 0) body.push(...got.filter((row) => !['vote-h', 'vote-p'].includes(keyOf(row))))
  } catch {
    // One section failing never takes the band down.
  }
  try {
    const got = await bandBeacon(io, e, k)
    if (got && got.length > 0) body.push(...got)
  } catch {
    // One section failing never takes the band down.
  }
  try {
    const got = await bandNews(io, e, k)
    if (got && got.length > 0) body.push(...got)
  } catch {
    // One section failing never takes the band down.
  }
  let any = body.length > 0 || !!chart.ready
  try {
    if (!any) any = await bandHasNews(io)
  } catch {
    // No room news is no band.
  }
  const r = roomOfBand()
  if (!any) return []
  let head = []
  let keys = []
  try {
    head = bandHead(io, e, k) || []
  } catch {
    // A head that cannot draw leaves the rows below it.
  }
  try {
    keys = bandKeys(io, e, k) || []
  } catch {
    // The keys row is the console's door; the rows above still draw.
  }
  const rows = [...head, ...body, ...keys]
  if (k.terminal || k.mobile) return rows
  const sev1 = !!r && severityTone(r.status && r.status.severity) === 'critical'
  return [k.card(rows, { key: 'band-card', tone: sev1 ? 'critical' : r && notLive(r) ? 'warning' : undefined })]
}

// keyOf is an element's key wherever its constructor keeps it.
function keyOf(el) {
  if (!el || typeof el !== 'object') return ''
  return String(el.key ?? (el.props && el.props.key) ?? '')
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

// tickAll gives every component the clock: warm tabs refresh on their own
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
    await tickTimeline(io, nowMs)
  } catch {
    // One component's tick never stops the others.
  }
  try {
    await tickContext(io, nowMs)
  } catch {
    // One component's tick never stops the others.
  }
  try {
    await tickMore(io, nowMs)
  } catch {
    // One component's tick never stops the others.
  }
  try {
    await tickConsole(io, nowMs)
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
    onSnapshotTimeline(io, room.snapshot, prev)
  } catch {
    // One component's listener never stops the others.
  }
  try {
    onSnapshotContext(io, room.snapshot, prev)
  } catch {
    // One component's listener never stops the others.
  }
  try {
    onSnapshotMore(io, room.snapshot, prev)
  } catch {
    // One component's listener never stops the others.
  }
  try {
    onSnapshotConsole(io, room.snapshot, prev)
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
    fill: (text, mode) => $.prompt.fill({ text, ...(mode ? { mode } : {}) }),
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
    // spawn starts `landfall <args>`, hands each piece to onPiece, and answers { stop, done }:
    // stop() ends the loop, which kills the child; done settles with how it ended.
    spawn: (args, onPiece) => {
      const stream = $.process.spawn({ argv: [room.bin, ...args] })
      const done = (async () => {
        try {
          for (;;) {
            const step = await stream.next()
            if (step.done) {
              const v = step.value
              return (v && v.value) || v || { code: null, signal: null }
            }
            try {
              onPiece(step.value)
            } catch {
              // A piece the reader could not take never ends the child.
            }
          }
        } catch (err) {
          return { code: null, signal: null, error: String(err).slice(0, 200) }
        }
      })()
      return {
        stop: () => {
          try {
            void Promise.resolve(stream.return(undefined)).catch(() => {})
          } catch {
            // Already ended.
          }
        },
        done,
      }
    },
    play: (asset) => $.audio.play({ asset }),
    storeGet: (key) => $.store.get(key),
    storeSet: (key, value) => $.store.set(key, value),
    copy: (text, s) => $.ui.copy({ text, surface: s || surface || undefined }),
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
