// Landfall's Claude Code mod: the war room, drawn in the session it reaches.
//
// THE RULE IT KEEPS. Landfall never takes a turn the person did not ask for
// (internal/hooks/stopnotice.go). Settings hooks could only honor that by
// waiting for the person's next message. A mod runs inside Claude Code, so it
// can show room news the moment the room daemon has it, and leave every turn
// to the person:
//
//   band above the prompt  the untold count, the newest line, and three keys:
//                          1 catch up (drafts a prompt, never sends it),
//                          2 show the room, 3 later
//   toast                  a chat message that names someone, once
//   dim suggestion         after a turn, when the room has news (Tab takes it)
//   /room                  the room in a pane, with no model call
//   chart ready            after the agent reads a metric, key 4 (or /chart) puts
//                          that read on the room's canvas as a chart: no turn,
//                          no model call, no permission dialog
//
// DELIVERY TO THE AGENT IS UNCHANGED. On the person's next message the mod runs
// the same `landfall hooks user-prompt-submit` the settings hook ran, with
// --from-mod, and attaches what it returns as context only Claude reads. That
// keeps every invariant the Go hook already proves (never consume without
// delivering, the stage, per-harness readers).
//
// ONE VOICE PER SESSION. At start the mod sets LANDFALL_MOD. Claude Code passes
// it to every settings hook and status line it starts after that, and the
// Landfall ones then stay quiet (internal/cli/hookevents.go modOwnsEvent), so
// a session never hears the room twice. pre-tool-use is left to the settings
// hook: it is the person's own opt-in policy.
//
// THE STREAM. `landfall watch` is one child process for the session's life. It
// writes a JSON line whenever what the person would see changes, woken by the
// daemon's own push. If it ends (no daemon yet, a restart), the mod starts it
// again a few seconds later.

const PANE = 'landfall-room'
const HOST = 'claude-code'
const RESTART_MS = 3000
const CATCH_UP = 'Catch me up on what changed in the war room.'
const ADDRESSED = '  ← addressed to a person'

// What `landfall watch` last said.
let snapshot = { line: '', rooms: [] }
// The newest seq per room the person set aside with "later".
let laterAt = {}
// The newest addressed seq per room already shown as a toast.
let toastedAt = {}
// The landfall binary, read once at start.
let bin = 'landfall'
// Set while a watch child is running, so a reload never starts two.
let watching = false
// The agent's latest metric read that can become a chart: { query, label }.
let chartReady = null
// Set while a chart is being added, so a double press adds one.
let pinning = false

export function register(on) {
  on('session.start', async ($, e, next) => {
    bin = (await $.env.get('LANDFALL_BIN')) || 'landfall'
    await $.env.set('LANDFALL_MOD', HOST)
    await $.command.register({ name: 'room', description: 'Show your Landfall war room' })
    await $.command.register({ name: 'chart', description: "Add your agent's latest metric read to the war room as a chart" })
    startWatch($)
    return next(e)
  })

  on('command.run', { command: 'room' }, async ($) => {
    if (snapshot.rooms.length === 0) {
      return { text: 'This folder is not in a war room. Open a share link from the room to join it.' }
    }
    const opened = await $.ui.open({ id: PANE, title: 'Landfall', focus: true, closeOnEscape: true })
    if (!opened.isPlaced) return { text: roomText() }
    return {}
  })

  on('command.run', { command: 'chart' }, async ($) => {
    if (!chartReady) return { text: 'No metric read to chart yet. Ask your agent to read a metric from the room, then run /chart.' }
    await pinChart($)
    return {}
  })

  // Widgets without a turn: when the agent reads a metric through the room,
  // the band offers to put that read on the room's canvas as a chart. One key,
  // no model call, nothing more to approve: `landfall chart` has the person's
  // room daemon make the same read and queues the chart (pinChart), so no
  // points are copied by anyone.
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
        chartReady = { query, label: chartLabel(e) }
        $.ui.invalidate('ui.render')
      }
    } catch {
      // Reading the result for a chart offer must never change the tool's answer.
    }
    return result
  })

  // Delivery: the same Go hook the settings file used to run, on the person's
  // own message, so the agent hears the room exactly when it did before.
  on('prompt.submit', async ($, e, next) => {
    // The person has moved on: a room pane left open would keep the keyboard
    // and eat the first keys of their next message.
    $.ui.close({ id: PANE }).catch(() => {})
    let context = ''
    try {
      const payload = JSON.stringify({
        hook_event_name: 'UserPromptSubmit',
        session_id: await $.session.id(),
        cwd: await $.session.cwd(),
        prompt: e.text,
      })
      const run = await $.process.run([bin, 'hooks', 'user-prompt-submit', '--host', HOST, '--from-mod'], {
        stdin: payload,
        timeoutMs: 5000,
      })
      context = additionalContext(run.stdout)
    } catch {
      // No landfall on PATH, or it timed out: the prompt goes as typed.
    }
    if (!context) return next(e)
    laterAt = {}
    return next({ ...e, context: [...(e.context ?? []), context] })
  }).catch(async ($, e, next) => next(e)) // a failure never holds the person's message

  // After a turn, offer the catch-up as a dim suggestion the person can take
  // with Tab. A suggestion is never sent on its own.
  on('turn.complete', async ($, e, next) => {
    const result = await next(e)
    if (!e.agentId && !e.isAborted && pending().length > 0) {
      void $.prompt.suggest({ text: CATCH_UP })
    }
    return result
  })

  // The band above the prompt: shown while the room has news the person has not
  // set aside, or the agent has a metric read that could be a chart.
  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    const rooms = pending()
    const chart = chartReady
    if (rooms.length === 0 && !chart) return next(e)
    const { Box, Text, Button } = $.ui.resolve(e)
    const width = Math.max(20, (e.props.bodyColumns ?? 80) - 2)
    const mine = await next(e)
    const rows = [mine]
    if (rooms.length > 0) {
      const room = rooms[0]
      const total = rooms.reduce((n, r) => n + r.count, 0)
      const head = [roomName(room), total === 1 ? '1 new' : total + ' new']
      if (room.votesAwaited === 1) head.push('1 vote awaited')
      if (room.votesAwaited > 1) head.push(room.votesAwaited + ' votes awaited')
      if (room.connection && room.connection !== 'live') head.push(room.connection)
      const newest = clip(plainLine(newestLine(room.digest ?? [])), width)
      rows.push(Text({ bold: true, children: ['Landfall · ' + head.join(' · ')] }))
      if (newest) rows.push(Text({ dimColor: true, children: [newest] }))
      rows.push(
        Box({
          flexDirection: 'row',
          columnGap: 2,
          children: [
            Button({
              key: 'catch-up',
              label: 'catch up',
              hotkey: '1',
              plain: true,
              onPress: async () => {
                await $.prompt.fill({ text: CATCH_UP })
              },
            }),
            Button({
              key: 'show',
              label: 'show the room',
              hotkey: '2',
              plain: true,
              onPress: async () => {
                await $.ui.open({ id: PANE, title: 'Landfall', focus: true, closeOnEscape: true })
              },
            }),
            Button({
              key: 'later',
              label: 'later',
              hotkey: '3',
              plain: true,
              dimColor: true,
              onPress: () => {
                for (const r of rooms) laterAt[r.roomKey] = r.maxSeq
                $.ui.invalidate('ui.render')
              },
            }),
          ],
        }),
      )
    }
    if (chart) {
      rows.push(
        Box({
          flexDirection: 'row',
          columnGap: 2,
          children: [
            Text({ children: [clip('Chart ready: ' + chart.label, Math.max(20, width - 30))] }),
            Button({ key: 'pin', label: 'add it to the room', hotkey: '4', plain: true, onPress: () => pinChart($) }),
          ],
        }),
      )
    }
    return Box({ flexDirection: 'column', children: rows.filter(Boolean) })
  })

  // The room, in a pane: every untold line, addressed lines first.
  on('ui.render', { component: 'Pane' }, async ($, e, next) => {
    if (e.requestId !== PANE) return next(e)
    const { Box, Text, Button } = $.ui.resolve(e)
    const width = Math.max(20, (e.props.bodyColumns ?? 80) - 2)
    const rows = []
    for (const room of snapshot.rooms) {
      const head = [roomName(room), room.count === 1 ? '1 new' : room.count + ' new']
      if (room.connection && room.connection !== 'live') head.push(room.connection)
      rows.push(Text({ key: 'h-' + room.roomKey, bold: true, children: [head.join(' · ')] }))
      const lines = room.digest ?? []
      if (lines.length === 0) rows.push(Text({ key: 'n-' + room.roomKey, dimColor: true, children: ['Nothing new since you last spoke.'] }))
      lines.forEach((line, i) => {
        const addressed = line.endsWith(ADDRESSED)
        rows.push(Text({ key: room.roomKey + '-' + i, bold: addressed, children: [clip((addressed ? '@ ' : '  ') + plainLine(line), width)] }))
      })
      rows.push(Text({ key: 's-' + room.roomKey, children: [' '] }))
    }
    if (snapshot.line) rows.push(Text({ key: 'line', dimColor: true, children: [clip(snapshot.line, width)] }))
    rows.push(
      Box({
        key: 'actions',
        flexDirection: 'row',
        columnGap: 2,
        children: [
          Button({
            key: 'pane-catch-up',
            label: 'catch up',
            hotkey: 'c',
            plain: true,
            onPress: async () => {
              await $.ui.close({ id: PANE })
              await $.prompt.fill({ text: CATCH_UP })
            },
          }),
          Button({ key: 'pane-close', label: 'close (esc)', plain: true, dimColor: true, onPress: () => $.ui.close({ id: PANE }) }),
        ],
      }),
    )
    return Box({ flexDirection: 'column', children: rows })
  })
}

// startWatch runs `landfall watch` for the session's life and starts it again
// when it ends. A top-level function, so `claude plugin validate` can follow $.
function startWatch($) {
  if (watching) return
  watching = true
  void (async () => {
    let buffered = ''
    try {
      const child = $.process.spawn({ argv: [bin, 'watch', '--host', HOST] })
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
    watching = false
    $.clock.after(RESTART_MS, () => startWatch($))
  })()
}

// applySnapshot takes one line of the stream and redraws what changed.
function applySnapshot($, line) {
  let next
  try {
    next = JSON.parse(line)
  } catch {
    return
  }
  if (!next || next.type !== 'rooms' || !Array.isArray(next.rooms)) return
  snapshot = { line: next.line || '', rooms: next.rooms }
  for (const room of snapshot.rooms) {
    if (room.addressed > 0 && room.maxSeq > (toastedAt[room.roomKey] ?? -1)) {
      toastedAt[room.roomKey] = room.maxSeq
      const said = (room.digest ?? []).find((l) => l.endsWith(ADDRESSED))
      if (said) $.ui.toast(roomName(room) + ': ' + plainLine(said), { timeoutMs: 8000 })
    }
  }
  // Claude Code already puts the mod's name in front of its status line.
  $.ui.status(snapshot.line ? snapshot.line.replace(/^🔴 landfall: /, '🔴 ') : undefined)
  $.ui.invalidate('ui.render')
}

// newestLine is the most recent untold line. The digest lists @-mentions
// first, and the daemon cannot tell whose name a mention carries, so the first
// line can be an older message to somebody else; the band shows what just
// happened, and the toast already covered the mention.
function newestLine(digest) {
  let best = ''
  let bestSeq = -1
  for (const line of digest) {
    const m = /^#(\d+) /.exec(line)
    const seq = m ? Number(m[1]) : -1
    if (seq >= bestSeq) {
      best = line
      bestSeq = seq
    }
  }
  return best
}

// pinChart puts the agent's latest metric read on the room's canvas as a chart.
// It runs `landfall chart`, which asks the person's room daemon to make the same
// read and queues the chart for their session to publish. A local command, not
// an MCP call: Claude Code puts a plugin's MCP call made from a key press to the
// permission dialog, and the key press is all the asking this needs.
async function pinChart($) {
  const chart = chartReady
  if (!chart || pinning) return
  pinning = true
  chartReady = null
  $.ui.invalidate('ui.render')
  try {
    const run = await $.process.run([bin, 'chart', '--host', HOST, '--query', JSON.stringify({ ...chart.query, title: chart.label })], {
      timeoutMs: 40000,
    })
    let answer = {}
    try {
      answer = JSON.parse((run.stdout || '').trim().split('\n').pop() || '{}')
    } catch {
      answer = { ok: false, error: (run.stderr || run.stdout || 'no answer').trim().slice(0, 200) }
    }
    if (answer.ok) $.ui.toast('Chart added to your dashboard in the room: ' + (answer.title || chart.label), { timeoutMs: 6000 })
    else $.ui.toast('Chart not added: ' + clip(String(answer.error || 'no answer'), 200), { timeoutMs: 8000 })
  } catch (err) {
    $.ui.toast('Chart not added: ' + String(err).slice(0, 200), { timeoutMs: 8000 })
  }
  pinning = false
}

// chartLabel names a read by the metric it asked for, wherever the source keeps it.
function chartLabel(e) {
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

// pending is the rooms with untold news the person has not set aside.
function pending() {
  return snapshot.rooms.filter((r) => r.count > 0 && r.maxSeq > (laterAt[r.roomKey] ?? -1))
}

function roomName(room) {
  if (room.displayId && room.title) return room.displayId + ' · ' + room.title
  return room.displayId || room.title || room.slug || 'war room'
}

// plainLine is a digest line as a person reads it: "bob: @alice can you…"
// rather than "#12 chat.message [bob@acme.com] — @alice can you…". The
// agent still gets the full line; this is only what the band and pane draw.
function plainLine(line) {
  const text = line.endsWith(ADDRESSED) ? line.slice(0, -ADDRESSED.length) : line
  const m = /^#\d+ (\S+)(?: \[([^\]]*)\])? — (.*)$/.exec(text)
  if (!m) {
    // A line with only an event type ("#30 memory.proposed"): say the type in words.
    const bare = /^#\d+ ([a-z_.]+)$/.exec(text)
    return bare ? KINDS[bare[1]] || bare[1].replace(/[._]/g, ' ') : text
  }
  const [, type, who, said] = m
  const kind = KINDS[type]
  if (!who) return kind ? kind + ': ' + said : said
  const name = who.split('@')[0] || who
  return kind ? name + ' (' + kind + '): ' + said : name + ': ' + said
}

// KINDS names the event types a person would want called out; chat needs no label.
const KINDS = {
  'claim.staged': 'finding, awaiting a second person',
  'claim.admitted': 'finding admitted',
  'claim.corroborated': 'corroborated',
  'claim.contested': 'contested',
  'edge.finding': 'finding',
  'finding.published': 'finding',
  'edge.action.proposed': 'suggested action',
  'remediation.proposed': 'suggested fix',
}

function clip(text, width) {
  return text.length > width ? text.slice(0, width - 1) + '…' : text
}

// roomText is /room's answer where no pane can be drawn (`claude -p`).
function roomText() {
  const out = []
  for (const room of snapshot.rooms) {
    out.push(roomName(room) + ' · ' + room.count + ' new')
    for (const line of room.digest ?? []) out.push('  ' + plainLine(line))
  }
  return out.join('\n')
}

// additionalContext reads the Go hook's stdout: one JSON object, or nothing.
function additionalContext(stdout) {
  const body = (stdout || '').trim()
  if (!body) return ''
  try {
    return JSON.parse(body)?.hookSpecificOutput?.additionalContext || ''
  } catch {
    return ''
  }
}
