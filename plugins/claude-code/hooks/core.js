// The mod's shared core: the one `landfall watch` stream, what it last said,
// the band's sections, and the helpers every component draws with.
//
// Components live in ./components/*.js. Each exports `install(on)` and reads
// the room only through this file, so two components never keep two copies of
// the room or start two watch children. register.js installs them in order.
//
// THE COMPONENT CONTRACT. Every file in ./components exports exactly these,
// and register.js calls each one BY NAME (the engine's validator follows `$`
// only into named functions, so no callback registries):
//
//   install(on)                 its own hooks: commands it answers, panes, tool rows
//   band(io, e, k)              rows for the band above the prompt, or null
//   onSnapshot(io, snap, prev)  after each new `landfall watch` line (toasts, sounds)
//   start(io)                   once at session start, after commands register
//   tick(io, nowMs)             every 5 s, from the session clock (live panes refresh here)
//
// `$` NEVER CROSSES A FILE. The engine's validator follows `$` only into
// functions declared in the same file, so register.js hands components an
// `io` object instead: closures that each spell `$.noun.event` themselves
// (register.js makeIo). Inside a component's own hooks, `$` is the hook's
// parameter and is used directly; pass it only to functions in that file.
//
//   io.surface             where the band draws: terminal, desktop, vscode, mobile
//   io.fill(text)          drafts the prompt (never sends)
//   io.suggest(text)       a dim suggestion Tab takes
//   io.toast(text, ms?)    a toast
//   io.open(id, title, opts?)  opens a pane ($.ui.open)  -> { isPlaced }
//   io.close(id)           closes a pane
//   io.invalidate()        redraws every hooked site
//   io.run(args, opts?)    runs `landfall <args>`, answers the last stdout line as JSON
//                          ({ ok:false, error } when it cannot)
//   io.play(asset)         plays one of the mod's sound files
//   io.storeGet(key) / io.storeSet(key, value)   values kept across sessions
//   io.copy(text)          to the clipboard
//
// A command is declared with addCommand() inside install(); register.js
// registers them all at session start.

export const HOST = 'claude-code'
export const RESTART_MS = 3000
export const CATCH_UP = 'Catch me up on what changed in the war room.'
export const ADDRESSED = '  ← addressed to a person'

// The mutable room state, one object so every module sees the same values.
// Module variables start over on a hot reload; the next watch line refills them.
export const room = {
  // What `landfall watch` last said: { line, rooms: WatchRoom[] }.
  snapshot: { line: '', rooms: [] },
  // The newest seq per room the person set aside with "later".
  laterAt: {},
  // The landfall binary, read once at start.
  bin: 'landfall',
  // Set while a watch child is running, so a reload never starts two.
  watching: false,
}

const commands = []

// addCommand declares a slash command; register.js registers every one at
// session start (the engine takes one unmatched session.start hook per
// module). The component answers with its own on('command.run', { command }).
export function addCommand(spec) {
  if (!commands.some((x) => x.name === spec.name)) commands.push(spec)
}

export function declaredCommands() {
  return commands
}

// parseAnswer reads a `landfall` run's last stdout line as JSON. Every action a
// key press or a button takes runs the CLI, never $.mcp.call: Claude Code puts
// a plugin's MCP call made outside a typed command to the permission dialog,
// which a mod cannot answer.
export function parseAnswer(run) {
  const last = ((run && run.stdout) || '').trim().split('\n').pop() || ''
  try {
    const answer = JSON.parse(last)
    if (answer && typeof answer === 'object') return answer
  } catch {
    // fall through
  }
  return { ok: false, error: clip(((run && (run.stderr || run.stdout)) || 'no answer').trim(), 200) }
}

// currentRoom is the room this session is in NOW (orderRooms), not the
// stream's first: a daemon keeps an older, resolved room for a while after
// its incident ends, and every pane and command acts on this one.
export function currentRoom() {
  return currentOf(room.snapshot)
}

// currentOf is a snapshot's current room, or null.
export function currentOf(snap) {
  return orderRooms((snap && snap.rooms) || [])[0] || null
}

// pending is the rooms with untold news the person has not set aside, the
// current room first. A room whose incident is over has no news worth the
// band unless it is the only one.
export function pending() {
  const rooms = orderRooms(room.snapshot.rooms)
  return rooms.filter((r, i) => r.count > 0 && r.maxSeq > (room.laterAt[r.roomKey] ?? -1) && (i === 0 || !roomIsOver(r)))
}

export function roomName(r) {
  if (r.displayId && r.title) return r.displayId + ' · ' + r.title
  return r.displayId || r.title || r.slug || 'war room'
}

export function clip(text, width) {
  text = String(text ?? '')
  return text.length > width ? text.slice(0, Math.max(1, width - 1)) + '…' : text
}

// plainLine is a digest line as a person reads it: "bob: @alice can you…"
// rather than "#12 chat.message [bob@acme.com] — @alice can you…".
export function plainLine(line) {
  const text = line.endsWith(ADDRESSED) ? line.slice(0, -ADDRESSED.length) : line
  const m = /^#\d+ (\S+)(?: \[([^\]]*)\])? — (.*)$/.exec(text)
  if (!m) {
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
export const KINDS = {
  'claim.staged': 'finding, awaiting a second person',
  'claim.admitted': 'finding admitted',
  'claim.corroborated': 'corroborated',
  'claim.contested': 'contested',
  'edge.finding': 'finding',
  'finding.published': 'finding',
  'edge.action.proposed': 'suggested action',
  'remediation.proposed': 'suggested fix',
}

// newestLine is the most recent untold line.
export function newestLine(digest) {
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

// dot is the status line's lead: red while live, yellow once mitigated, green once over.
export function dot(status) {
  const s = (status || '').toLowerCase()
  if (/resolved|closed|postmortem|done/.test(s)) return '🟢'
  if (/mitigat|monitor|stable/.test(s)) return '🟡'
  return '🔴'
}

// tone maps an incident's status or severity to a kit tone.
export function statusTone(status) {
  const s = (status || '').toLowerCase()
  if (/resolved|closed|done|monitor/.test(s)) return 'good'
  if (/identified/.test(s)) return 'info'
  if (/mitigat/.test(s)) return 'warning'
  return 'critical'
}

export function severityTone(sev) {
  const s = (sev || '').toLowerCase()
  if (/sev1|critical/.test(s)) return 'critical'
  if (/sev2|high/.test(s)) return 'serious'
  if (/sev3/.test(s)) return 'warning'
  return 'neutral'
}

// statusWords is a room's state in words: status, severity, Beacon's run.
export function statusWords(st) {
  const out = []
  if (st.status) out.push(st.status)
  if (st.severity) out.push(st.severity)
  if (st.beacon) out.push('Beacon ' + st.beacon)
  return out
}

// personLine is one person in the pane ("● bob · Claude Code: querying ALB").
export function personLine(p) {
  const name = p.name + (p.you ? ' (you)' : '')
  const tools = toolsOf(p)
  return (p.here ? '● ' : '○ ') + name + (tools.length > 0 ? ' · ' + tools.join(' · ') : '') + (p.here ? '' : ' · away')
}

// whereIs is where a person works, in words: "war room, Claude Code".
export function whereIs(p) {
  const where = (p.agents ?? []).filter((a) => a.here).map((a) => a.tool)
  if (p.browser) where.unshift('war room')
  return [...new Set(where)].join(', ')
}

// whoIsHere is the line under the prompt: the other people who are here.
export function whoIsHere() {
  const r = currentRoom()
  const st = r && r.status
  if (!st) return ''
  const others = (st.people ?? []).filter((p) => p.here && !p.you)
  if (others.length === 0) return ''
  const shown = others.slice(0, 4).map((p) => {
    const where = whereIs(p)
    return where ? p.name + ' (' + where + ')' : p.name
  })
  const more = others.length > 4 ? ' · ' + (others.length - 4) + ' more' : ''
  return 'Here: ' + shown.join(' · ') + more
}

// ago is a short age: "now", "42s", "3m", "2h".
export function ago(ms) {
  if (ms == null || !isFinite(ms) || ms < 0) return ''
  const s = Math.round(ms / 1000)
  if (s < 5) return 'now'
  if (s < 60) return s + 's'
  const m = Math.round(s / 60)
  if (m < 60) return m + 'm'
  return Math.round(m / 60) + 'h'
}

// quoteDraft is text quoted into the prompt as a draft: every line behind
// "> ", then a blank line, so the person writes their own words under it.
export function quoteDraft(text) {
  return (
    String(text ?? '')
      .trim()
      .split('\n')
      .map((l) => '> ' + l)
      .join('\n') + '\n\n'
  )
}

// toolsOf is where a person works, one entry per tool however many sessions
// of it they run (two Claude sessions of one person are one "Claude"), each
// tool's distinct doings after it once: ["war room", "Claude: investigating"].
// A tool none of whose sessions is here reads "Codex (away)". With hereOnly,
// sessions that are away are left out.
export function toolsOf(p, { hereOnly = false } = {}) {
  const out = []
  if (p.browser) out.push('war room')
  const byTool = new Map()
  for (const a of p.agents ?? []) {
    if (hereOnly && !a.here) continue
    const tool = a.tool || 'agent'
    let t = byTool.get(tool)
    if (!t) byTool.set(tool, (t = { here: false, doing: [] }))
    if (a.here) t.here = true
    if (a.doing && !t.doing.includes(a.doing)) t.doing.push(a.doing)
  }
  for (const [tool, t] of byTool) out.push(tool + (t.here ? '' : ' (away)') + (t.doing.length > 0 ? ': ' + t.doing.join(', ') : ''))
  return out
}

// agentIn reports that this session's own agent is in the room (the watch
// stream's agent.inRoom).
export function agentIn(r) {
  return !!(r && r.agent && r.agent.inRoom === true)
}

// roomIsOver reports a room whose incident is resolved, closed or in
// postmortem (the CLI's closedStatus).
export function roomIsOver(r) {
  const s = ((r && r.status && r.status.status) || '').toLowerCase().trim()
  return s === 'resolved' || s === 'closed' || s === 'postmortem'
}

// orderRooms is a copy of the rooms with the one this session is in now
// first: the room its own agent is in, then an open incident before one that
// is over, then the newest activity (maxSeq). Rooms equal on all three keep
// the stream's order. The CLI orders the stream the same way (currentRooms);
// this keeps a mod in step with an older CLI that does not.
export function orderRooms(rooms) {
  return (rooms || [])
    .map((r, i) => ({ r, i }))
    .sort((a, b) => {
      const ai = agentIn(a.r) ? 0 : 1
      const bi = agentIn(b.r) ? 0 : 1
      if (ai !== bi) return ai - bi
      const ao = roomIsOver(a.r) ? 1 : 0
      const bo = roomIsOver(b.r) ? 1 : 0
      if (ao !== bo) return ao - bo
      const d = (b.r.maxSeq ?? 0) - (a.r.maxSeq ?? 0)
      return d !== 0 ? d : a.i - b.i
    })
    .map((x) => x.r)
}

// roomArgs is `--room <key>` for a CLI run about one room, so the CLI never
// falls back to a room of its own choosing; nothing when there is no room.
export function roomArgs(r) {
  return r && r.roomKey ? ['--room', r.roomKey] : []
}

// Every row the console adds to the person's own conversation goes through here, so the wording
// of what was refused lives in one place. Answers the deny reason, or null once stored.
export async function appendNote(io, text) {
  if (!io || typeof io.append !== 'function') return 'This Claude Code cannot add notes to the chat.'
  if (!text || !String(text).trim()) return 'Nothing to add.'
  return io.append(String(text))
}
