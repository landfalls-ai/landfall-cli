// context.js: the Context tab of the console (spec §4.10).
//
// What the room knows, and the one key that puts it in the person's agent's conversation without a
// turn. Two blocks: the shared context (`landfall brief`), then the artifacts (`landfall artifacts`
// and, once per file, `landfall artifact <id>`).
//
// "Add" is one `io.append` (core.js appendNote): a user-role row the model reads with the person's
// next message. It starts no turn, it fills no prompt, and a policy plugin above may refuse it; the
// refusal is the toast. Nothing else in this file reaches the model, and nothing here calls an MCP
// tool: every read and every add runs the landfall CLI.
//
// THE CONSOLE CONTRACT (core.js CONSOLE), called by name from console.js:
//   tab(k, io, nowMs, args)   the body rows. By default it ends with the footer and the keys row
//                             (`a`, `v`, `r: refresh`, close), the way every pane drew them; pass
//                             args.chrome === false to get the body alone and draw the footer and
//                             keys yourself with footer() and keys().
//   badge()                   artifacts shared since the tab was last shown, or null
//   warm(io)                  starts the reads (the first view)
// Also exported: install(on), onSnapshot(io, snap, prev), tick(io, nowMs), start(io), band(),
// focusKey(io, key), refresh(io), footer(k, io, nowMs), keys(k, io, nowMs), contextText(io),
// artifactRow(k, a, opts), artifactPreview(k, io, a, opts), addArtifact(io, a),
// addSharedContext(io, opts).

import { CONSOLE, HOST, ago, appendNote, clip, consoleState, currentOf, currentRoom, parseAnswer, reading, room as coreRoom, roomArgs, whereIs } from '../core.js'
import { TONE } from '../kit.js'
import { keep, liveWords, livePane, refresh as refreshLive } from '../live.js'
import { hhmm } from '../views.js'

// How often a shown tab reads again, and the slow read that keeps the badge honest while the
// console is open on another tab. Both land on the session's 5 s tick.
export const READ_MS = 30000
export const BADGE_MS = 60000
const SLACK_MS = 2000

export const ARTIFACTS_SHOWN = 12
export const PREVIEW_CHARS = 4000
export const ADD_CHARS = 20000
export const MAX_BYTES = 5 * 1024 * 1024
export const IMAGE_BYTES = 256 * 1024

export const HINT = 'Enter on an artifact adds it to your context'

// The tab's state. Module state: a hot reload starts it over and the next draw reads again.
const ctx = {
  roomKey: '',
  brief: livePane(),
  arts: livePane(),
  added: {}, // roomKey -> { atMs, asOfSeq }
  adding: false, // the shared context add is in flight
  addingArt: new Set(), // artifactIds with an add in flight
  previews: new Map(), // artifactId -> { state, text, totalChars, truncated, binary, base64 }
  selected: '', // artifactId of the focused row
  open: false, // the preview of the selected row is expanded
  known: {}, // roomKey -> Set of artifactIds already seen
  fresh: {}, // roomKey -> Set of artifactIds new since the tab was last shown
  rich: false, // the surface draws Svg (the last draw's)
  lastArtSeq: {}, // roomKey -> newest artifact.shared seq the watch digest showed
}

// ---------------------------------------------------------------- hooks

export function install(on) {
  // The focus ring moving onto an artifact row selects it (its preview reads once). Both the
  // terminal row Button and the desktop row's Add button name their artifact in their key.
  on('ui.focus', { requestId: 'landfall' }, async ($, e, next) => {
    focusKey(paneIo($), e.element)
    return next(e)
  })
}

// paneIo is what the focus hook reads with, from its own `$`, shaped as register.js shapes `io`.
function paneIo($) {
  return {
    run: async (args, opts) => {
      try {
        return parseAnswer(await $.process.run([binName(), ...args], { timeoutMs: 20000, ...(opts || {}) }))
      } catch (err) {
        return { ok: false, error: String(err).slice(0, 200) }
      }
    },
    invalidate: () => $.ui.invalidate('ui.render'),
    now: () => $.clock.now(),
  }
}

// The landfall binary, as core.js read it at session start.
function binName() {
  return coreRoom.bin || 'landfall'
}

export function band(io, e, k) {
  return null
}

export function start(io) {}

// focusKey: the ring moved onto `key`; an artifact row of ours selects its artifact.
export function focusKey(io, key) {
  const id = artifactIdOfKey(key)
  if (!id || id === ctx.selected) return
  ctx.selected = id
  ctx.open = false
  const a = findArtifact(id)
  if (a) void ensurePreview(io, a, { rich: ctx.rich })
  io.invalidate()
}

// artifactIdOfKey reads the artifact id out of a row element key ("art-<id>", "art-add-<id>",
// "art-prev-<id>"), or ''.
export function artifactIdOfKey(key) {
  const m = /^art-(?:add-|prev-)?(.+)$/.exec(String(key || ''))
  return m ? m[1] : ''
}

// ---------------------------------------------------------------- the console contract

export function badge() {
  const r = currentRoom()
  const n = r && ctx.fresh[r.roomKey] ? ctx.fresh[r.roomKey].size : 0
  return n > 0 ? n : null
}

// warm starts the tab's reads: the first view in a session, and any later one that finds them
// not started (a room change). Safe to call on every view.
export function warm(io) {
  const r = currentRoom()
  if (!r) return
  syncRoom(r.roomKey)
  if (!ctx.brief.inFlight && ctx.brief.triedAt === 0) void loadBrief(io, r)
  if (!ctx.arts.inFlight && ctx.arts.triedAt === 0) void loadArtifacts(io, r)
}

// onSnapshot: a new artifact in the room's untold digest reads the list at once; the first sight
// of a room reads it silently, so what is already there is never news.
export function onSnapshot(io, snap, prev) {
  const r = currentOf(snap)
  if (!r) return
  syncRoom(r.roomKey)
  const newest = newestArtifactSeq(r.digest)
  if (!ctx.known[r.roomKey]) {
    if (newest != null) ctx.lastArtSeq[r.roomKey] = newest
    if (!ctx.arts.inFlight && ctx.arts.triedAt === 0) void loadArtifacts(io, r)
    return
  }
  if (newest != null && newest > (ctx.lastArtSeq[r.roomKey] ?? -1)) {
    ctx.lastArtSeq[r.roomKey] = newest
    void loadArtifacts(io, r)
  }
}

// tick: the shown tab reads again every 30 s; while the console is open on another tab and this one
// has been shown (it is warm), the artifact list is read every minute so the badge stays current.
// A console that is closed, or a tab never shown, reads nothing.
export async function tick(io, nowMs) {
  const r = currentRoom()
  if (!r || !reading('context')) return
  syncRoom(r.roomKey)
  const shown = consoleState.tab === 'context'
  if (shown) {
    if (due(ctx.brief, nowMs, READ_MS)) void loadBrief(io, r)
    if (due(ctx.arts, nowMs, READ_MS)) void loadArtifacts(io, r)
    io.invalidate()
  } else if (due(ctx.arts, nowMs, BADGE_MS)) {
    void loadArtifacts(io, r)
  }
}

function due(lp, nowMs, everyMs) {
  return !lp.inFlight && nowMs - lp.triedAt >= everyMs - SLACK_MS
}

// refresh: `r`. Reads both again, and lets a failed preview try once more.
export function refresh(io) {
  const r = currentRoom()
  if (!r) return
  for (const [id, p] of ctx.previews) if (p.state === 'fail') ctx.previews.delete(id)
  void loadBrief(io, r)
  void loadArtifacts(io, r)
}

// newestArtifactSeq is the newest `artifact.shared` seq in a room's untold digest lines
// ("#215 artifact.shared [bob@acme.com] <em dash> runbook.md"), or null.
export function newestArtifactSeq(digest) {
  let best = null
  for (const line of digest || []) {
    const m = /^#(\d+) artifact\.shared\b/.exec(String(line))
    if (m && (best == null || Number(m[1]) > best)) best = Number(m[1])
  }
  return best
}

// syncRoom starts the tab over when the session is in another room than the one it read.
function syncRoom(roomKey) {
  if (ctx.roomKey === roomKey) return
  ctx.roomKey = roomKey
  ctx.brief = livePane()
  ctx.arts = livePane()
  ctx.selected = ''
  ctx.open = false
  ctx.previews = new Map()
}

// ---------------------------------------------------------------- reads

async function loadBrief(io, r) {
  const roomKey = r.roomKey
  await refreshLive(ctx.brief, () => io.run(['brief', '--host', HOST, ...roomArgs(r)]), await nowOf(io))
  if (ctx.roomKey !== roomKey) return
  io.invalidate()
}

async function loadArtifacts(io, r) {
  const roomKey = r.roomKey
  const started = await refreshLive(ctx.arts, () => io.run(['artifacts', '--host', HOST, ...roomArgs(r)]), await nowOf(io))
  if (started === false) return
  if (ctx.roomKey !== roomKey) return
  const a = ctx.arts.last
  if (a && a.ok) noteArtifacts(io, roomKey, a.artifacts || [])
  const sel = selectedArtifact()
  if (sel) void ensurePreview(io, sel, { rich: ctx.rich })
  io.invalidate()
}

// noteArtifacts compares the list with what this session has seen of the room: new ones are
// told once (a toast) and counted on the Context segment until the tab is shown.
function noteArtifacts(io, roomKey, list) {
  const known = ctx.known[roomKey]
  const ids = list.map((a) => a.artifactId)
  if (!known) {
    ctx.known[roomKey] = new Set(ids)
    return
  }
  const added = list.filter((a) => !known.has(a.artifactId))
  if (added.length === 0) return
  for (const a of added) known.add(a.artifactId)
  const shown = consoleState.open && consoleState.tab === 'context'
  if (!shown) {
    if (!ctx.fresh[roomKey]) ctx.fresh[roomKey] = new Set()
    for (const a of added) ctx.fresh[roomKey].add(a.artifactId)
  }
  if (added.length > 3) io.toast('New in the room: ' + added.length + ' artifacts', 6000)
  else for (const a of added) io.toast(newArtifactWords(a), 6000)
}

// Every toast is at most TOAST_MAX characters (the engine's box holds 3 rows of 40 cells): the
// outcome and the subject come first, and a long filename is clipped in the middle, keeping its
// extension, so the verb and the outcome always survive.
export const TOAST_MAX = 80
export const NAME_MAX = 32

// clipMiddle: "postmortem-acme-incident-95.pdf" -> "postmortem-acm…ent-95.pdf", the extension kept.
export function clipMiddle(name, width) {
  name = String(name ?? '')
  if (name.length <= width) return name
  const dot = name.lastIndexOf('.')
  const ext = dot > 0 && name.length - dot <= 6 ? name.slice(dot) : ''
  const keep = Math.max(2, width - 1 - ext.length)
  const head = Math.ceil(keep / 2)
  const tail = keep - head
  const base = ext ? name.slice(0, dot) : name
  return base.slice(0, head) + '…' + (tail > 0 ? base.slice(base.length - tail) : '') + ext
}

// fit: prefix + filename + suffix within TOAST_MAX, the filename giving way first.
function fit(prefix, name, suffix) {
  const room = Math.max(12, Math.min(NAME_MAX, TOAST_MAX - prefix.length - suffix.length))
  return clip(prefix + clipMiddle(name, room) + suffix, TOAST_MAX)
}

// newArtifactWords is the toast: "New in the room: runbook.md · by bob's agent".
export function newArtifactWords(a) {
  const by = ' · by ' + clip(sharerName(a), 24)
  return fit('New in the room: ', a.filename, by)
}

export function addedFileWords(filename, size) {
  return fit('Added ', filename, ' (' + humanSize(size) + ') to your context.')
}

export function addedCutWords(filename, total) {
  return fit('Added ', filename, ' to your context: the first ' + fmtInt(ADD_CHARS) + ' of ' + fmtInt(total) + ' characters.')
}

export function addedNoteWords(filename, size) {
  return fit('Added a note about ', filename, ' (' + humanSize(size) + ') to your context.')
}

export function notAddedWords(reason) {
  return clip('Not added: ' + reason, TOAST_MAX)
}

export function unreadWords(reason) {
  return clip('The shared context could not be read: ' + reason, TOAST_MAX)
}

// joinWords: the post-join toast's lead, "Joined Landfall 171. " (the title is in the band).
function joinLead(joined) {
  return 'Joined ' + clip(joined, 24) + '. '
}

async function nowOf(io) {
  try {
    const n = Number(await io.now())
    return isFinite(n) ? n : 0
  } catch {
    return 0
  }
}

function artifactList() {
  const a = ctx.arts.answer
  return a && a.ok ? a.artifacts || [] : []
}

function findArtifact(id) {
  return artifactList().find((a) => a.artifactId === id) || null
}

// selectedArtifact is the focused row's artifact: the one the ring last landed on, else the first.
function selectedArtifact() {
  const list = artifactList()
  if (list.length === 0) return null
  return list.find((a) => a.artifactId === ctx.selected) || list[0]
}

// ---------------------------------------------------------------- previews

// ensurePreview reads one file's excerpt once and keeps it for the session. A PDF or a picture
// the surface cannot draw is never fetched; a picture the desktop can draw is fetched with its
// bytes when it is small enough.
export async function ensurePreview(io, a, { rich = false } = {}) {
  const id = a.artifactId
  const want = previewKind(a, rich)
  if (want === 'none') return
  const have = ctx.previews.get(id)
  if (have && (have.state === 'loading' || have.state === 'ok' || have.state === 'fail')) {
    if (!(want === 'image' && have.state === 'ok' && !have.base64 && rich && !have.triedBytes)) return
  }
  const prev = { state: 'loading', triedBytes: want === 'image' }
  ctx.previews.set(id, prev)
  const r = roomOf()
  const args = ['artifact', id, '--host', HOST, ...roomArgs(r), '--max-chars', String(PREVIEW_CHARS)]
  if (want === 'image') args.push('--bytes')
  const ans = await io.run(args)
  if (ans && ans.ok) {
    ctx.previews.set(id, {
      state: 'ok',
      triedBytes: prev.triedBytes,
      text: typeof ans.text === 'string' ? ans.text : '',
      totalChars: ans.totalChars ?? null,
      truncated: !!ans.truncated,
      binary: !!ans.binary,
      base64: typeof ans.base64 === 'string' ? ans.base64 : '',
    })
  } else {
    ctx.previews.set(id, { state: 'fail', error: (ans && ans.error) || '' })
  }
  io.invalidate()
}

// previewKind: 'text' (an excerpt is read), 'image' (the bytes are read, desktop, small), 'pdf'
// and 'picture' and 'binary' (said in a sentence, never fetched), or 'none'.
export function previewKind(a, rich) {
  const ct = baseType(a.contentType)
  if (ct === 'application/pdf') return 'none'
  if (isImage(ct)) return rich && (a.size ?? 0) <= IMAGE_BYTES ? 'image' : 'none'
  if (isKnownBinary(ct)) return 'none'
  return 'text'
}

// previewRows draws the preview under a row, indented 2: the excerpt's first two rows, or the
// whole excerpt while `open`, or the one dim sentence a picture or a PDF gets.
export function artifactPreview(k, io, a, { open = false, key = 'art' } = {}) {
  const ct = baseType(a.contentType)
  const pad = (kids, kkey) => k.els.Box({ key: kkey, flexDirection: 'column', paddingLeft: 2, children: kids.filter(Boolean) })
  if (ct === 'application/pdf') return pad([k.dim("a PDF; the room's web viewer shows it", key + '-pv')], key + '-pvb')
  if (isImage(ct) && !isTextLike(ct)) {
    const p = ctx.previews.get(a.artifactId)
    if (k.rich && (a.size ?? 0) <= IMAGE_BYTES) {
      if (p && p.state === 'ok' && p.base64) {
        return pad([k.svg(imageSvg(ct, p.base64), { key: key + '-img', alt: a.filename, width: 352, height: 200 })], key + '-pvb')
      }
      if (!p || p.state === 'loading') return pad([k.dim('Reading the picture…', key + '-pv')], key + '-pvb')
    }
    return pad([k.dim("a picture; the room's web viewer shows it", key + '-pv')], key + '-pvb')
  }
  if (isKnownBinary(ct)) {
    const zip = typeWord(a) === 'zip' || /zip/.test(ct)
    return pad([k.dim(zip ? "a zip archive; the room's web viewer has it" : "a file; the room's web viewer has it", key + '-pv')], key + '-pvb')
  }
  const p = ctx.previews.get(a.artifactId)
  if (!p || p.state === 'loading') return pad([k.dim('Reading the preview…', key + '-pv')], key + '-pvb')
  if (p.state === 'fail') return pad([k.dim('The file could not be read.', key + '-pv')], key + '-pvb')
  if (p.binary || !p.text) return null
  if (open) {
    const kids = [k.els.Text({ key: key + '-full', children: [p.text.replace(/\s+$/, '')] })]
    if (p.truncated && p.totalChars) kids.push(k.dim('The first ' + fmtInt(PREVIEW_CHARS) + ' of ' + fmtInt(p.totalChars) + ' characters.', key + '-cut'))
    return pad(kids, key + '-pvb')
  }
  const lines = excerptLines(p.text, Math.max(20, k.width - 4), 2, p.truncated)
  return pad(
    lines.map((l, i) => k.els.Text({ key: key + '-x' + i, wrap: 'truncate-end', children: [l] })),
    key + '-pvb',
  )
}

// excerptLines flows a text into at most `maxLines` rows of `width`, whitespace folded, with a
// final "…" when more was left.
export function excerptLines(text, width, maxLines, more = false) {
  const words = String(text ?? '')
    .split(/\s+/)
    .filter(Boolean)
  const lines = []
  let cur = ''
  let i = 0
  for (; i < words.length && lines.length < maxLines; i++) {
    let w = words[i]
    if (w.length > width) w = w.slice(0, width - 1) + '…'
    if (cur && cur.length + 1 + w.length > width) {
      lines.push(cur)
      cur = ''
      if (lines.length >= maxLines) break
    }
    cur = cur ? cur + ' ' + w : w
  }
  if (lines.length < maxLines && cur) {
    lines.push(cur)
    cur = ''
  }
  const left = i < words.length || cur !== '' || more
  if (left && lines.length > 0) {
    const last = lines[lines.length - 1]
    lines[lines.length - 1] = (last.length >= width ? last.slice(0, width - 1) : last) + '…'
  }
  return lines
}

function imageSvg(ct, b64) {
  return (
    '<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 352 200" width="352" height="200">' +
    '<image width="352" height="200" preserveAspectRatio="xMinYMin meet" href="data:' +
    ct +
    ';base64,' +
    b64 +
    '"/></svg>'
  )
}

// ---------------------------------------------------------------- artifact words

export function baseType(ct) {
  return String(ct || '')
    .split(';')[0]
    .trim()
    .toLowerCase()
}

// isTextLike mirrors the CLI's tools.TextLike: the files `landfall artifact` answers as text.
export function isTextLike(ct) {
  ct = baseType(ct)
  if (ct.startsWith('text/')) return true
  if (['application/json', 'application/xml', 'application/javascript', 'application/x-yaml', 'application/yaml', 'application/csv', 'image/svg+xml'].includes(ct)) return true
  return ct.endsWith('+json') || ct.endsWith('+xml')
}

function isImage(ct) {
  return baseType(ct).startsWith('image/') && baseType(ct) !== 'image/svg+xml'
}

// isKnownBinary: files that are never text, so no read is spent finding that out.
function isKnownBinary(ct) {
  ct = baseType(ct)
  if (isTextLike(ct)) return false
  if (/^(image|audio|video|font)\//.test(ct)) return true
  return /(zip|gzip|tar|x-7z|octet-stream|pdf|msword|officedocument|x-bzip|x-xz)/.test(ct)
}

// typeWord is the type as one word: markdown, text, csv, json, html, png, jpeg, gif, webp, pdf.
export function typeWord(a) {
  const ct = baseType(a.contentType)
  const fixed = {
    'text/markdown': 'markdown',
    'text/x-markdown': 'markdown',
    'text/plain': 'text',
    'text/csv': 'csv',
    'application/csv': 'csv',
    'application/json': 'json',
    'text/html': 'html',
    'image/png': 'png',
    'image/jpeg': 'jpeg',
    'image/jpg': 'jpeg',
    'image/gif': 'gif',
    'image/webp': 'webp',
    'image/svg+xml': 'svg',
    'application/pdf': 'pdf',
    'application/zip': 'zip',
    'application/gzip': 'gzip',
    'application/x-gzip': 'gzip',
    'application/xml': 'xml',
    'text/xml': 'xml',
    'application/yaml': 'yaml',
    'application/x-yaml': 'yaml',
  }
  if (fixed[ct]) return fixed[ct]
  if (ct.startsWith('text/')) return 'text'
  const ext = /\.([A-Za-z0-9]{1,5})$/.exec(String(a.filename || ''))
  if (ext) return ext[1].toLowerCase()
  const sub = ct.split('/')[1]
  if (sub && sub !== 'octet-stream') return sub.replace(/^x-/, '').replace(/\+.*$/, '')
  return 'file'
}

// humanSize is the web app's own (warroom-ui humanArtifactSize), with a whole number's ".0" left off.
export function humanSize(n) {
  n = Number(n) || 0
  if (n < 1024) return n + ' B'
  const trim = (v) => v.toFixed(1).replace(/\.0$/, '')
  if (n < 1024 * 1024) return trim(n / 1024) + ' KB'
  return trim(n / (1024 * 1024)) + ' MB'
}

function fmtInt(n) {
  return String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ',')
}

// sharerName is who shared it: the name, with "'s agent" when an agent did.
export function sharerName(a) {
  const s = (a && a.sharer) || {}
  const name = s.displayName || 'Someone'
  if (s.kind !== 'agent') return name
  if (/agent$/i.test(name) || /^beacon$/i.test(name)) return name
  return name + "'s agent"
}

// harnessOf names the tool behind an agent's label ("bob-claude-code" is Claude Code).
export function harnessOf(label) {
  const l = String(label || '')
  if (!l) return ''
  if (/claude/i.test(l)) return 'Claude Code'
  if (/codex/i.test(l)) return 'Codex'
  if (/cursor/i.test(l)) return 'Cursor'
  if (/gemini/i.test(l)) return 'Gemini'
  if (/copilot/i.test(l)) return 'Copilot'
  return l
}

function metaWords(a, nowMs) {
  const t = Date.parse(a.sharedAt || '')
  const age = isFinite(t) && nowMs ? ago(Math.max(0, nowMs - t)) : ''
  return [typeWord(a), humanSize(a.size), sharerName(a), age].filter(Boolean).join(' · ')
}

// ---------------------------------------------------------------- drawing

function roomOf() {
  return currentRoom()
}

// label is a status word in its tone: "● staged" on the terminal, and off it a tinted box with no
// border (a chip with a border reads as pressable).
function label(k, text, tone, key) {
  const color = TONE[tone] || TONE.neutral
  if (k.terminal) return k.els.Text({ key, color, children: ['● ' + text] })
  return k.els.Box({
    key,
    paddingX: 1,
    backgroundColor: color + '22',
    flexShrink: 0,
    children: [k.els.Text({ key: key + '-t', color, bold: true, children: ['● ' + text] })],
  })
}

const STATE_TONE = { staged: 'neutral', contested: 'serious', corroborated: 'info', admitted: 'good', withdrawn: 'neutral' }

// itemRow is one brief item: "#205 statement · carol", a state label at the right edge for a
// claim, and under an established one how it got in, dim, indented 5.
function itemRow(k, it, key) {
  const { Text, Box } = k.els
  const left = Box({
    key: key + '-l',
    flexDirection: 'row',
    columnGap: 1,
    flexShrink: 1,
    flexGrow: 1,
    children: [
      Text({ key: key + '-n', dimColor: true, children: ['#' + it.seq] }),
      Text({ key: key + '-s', flexShrink: 1, children: [String(it.statement || '')] }),
      it.by ? Text({ key: key + '-b', dimColor: true, children: ['· ' + it.by] }) : null,
    ].filter(Boolean),
  })
  const kids = [left]
  if (it.state) kids.push(label(k, it.state, STATE_TONE[it.state] || 'neutral', key + '-st'))
  const row = Box({ key, flexDirection: 'row', columnGap: 1, justifyContent: 'space-between', alignItems: 'center', children: kids })
  if (!it.admission) return row
  return k.col(
    [
      row,
      Box({ key: key + '-ab', paddingLeft: 5, children: [Text({ key: key + '-a', dimColor: true, wrap: 'truncate-end', children: [String(it.admission)] })] }),
    ],
    key + '-c',
  )
}

// participantsWords: "carol (war room), bob (Claude Code), alice (Codex), dave (you)". Where each
// works comes from the watch (whereIs), which the brief's labels back up.
export function participantsWords(b, r) {
  const people = (r && r.status && r.status.people) || []
  const byName = new Map()
  for (const p of b.participants || []) {
    const name = p.displayName || ''
    if (!name) continue
    const e = byName.get(name) || { where: [] }
    const w = p.edgeAgentLabel ? harnessOf(p.edgeAgentLabel) : 'war room'
    if (w && !e.where.includes(w)) e.where.push(w)
    byName.set(name, e)
  }
  const out = []
  for (const [name, e] of byName) {
    const p = people.find((x) => x.name === name)
    if (p && p.you) {
      out.push(name + ' (you)')
      continue
    }
    const where = p ? whereIs(p) : e.where.join(', ')
    out.push(where ? name + ' (' + where + ')' : name)
  }
  return out.join(', ')
}

function scopeWords(s) {
  return 'Scope: ' + [s.kind, s.label].filter(Boolean).join(' ') + (s.by ? ' · pinned by ' + s.by : '')
}

function focusWords(f) {
  return 'Focus: ' + f.text + (f.by ? ' · asked by ' + f.by : '')
}

function changedSince(roomKey) {
  const added = ctx.added[roomKey]
  const b = ctx.brief.answer
  if (!added || !b || !b.ok) return false
  return b.asOfSeq != null && added.asOfSeq != null && b.asOfSeq > added.asOfSeq
}

// addKeyDrawn: the add key is shown before the first add and again once the context moved; not
// while the conversation holds the current context, and not before there is something to add.
function addKeyDrawn(roomKey) {
  const b = ctx.brief.answer
  if (!b || !b.ok) return false
  return !ctx.added[roomKey] || changedSince(roomKey)
}

function sharedBlock(k, io, r, nowMs) {
  const { Text } = k.els
  const rows = []
  const b = ctx.brief.answer
  if (!b) {
    rows.push(k.dim('Reading the shared context…', 'cx-reading'))
    return rows
  }
  if (!b.ok) {
    rows.push(k.text(b.error || 'The shared context could not be read.', { key: 'cx-err' }))
    return rows
  }
  const tail = [b.asOfSeq != null ? 'as of #' + b.asOfSeq : '', ctx.brief.inFlight ? 'reading…' : ''].filter(Boolean)
  rows.push(
    k.row(
      [Text({ key: 'cx-h', bold: true, children: ['Shared context'] }), tail.length ? Text({ key: 'cx-hd', dimColor: true, children: ['· ' + tail.join(' · ')] }) : null],
      'cx-head',
      1,
    ),
  )
  const added = ctx.added[r.roomKey]
  if (added) rows.push(k.dim((changedSince(r.roomKey) ? 'changed since ' : 'in your conversation since ') + hhmm(added.atMs), 'cx-added'))
  const est = b.established || []
  const open = b.open || []
  if (est.length === 0 && open.length === 0) rows.push(k.dim('No established or open items yet.', 'cx-fresh'))
  if (est.length > 0) {
    rows.push(Text({ key: 'cx-eh', bold: true, children: ['Established · ' + est.length] }))
    est.forEach((it, i) => rows.push(itemRow(k, it, 'cx-e' + i)))
  }
  if (open.length > 0) {
    rows.push(Text({ key: 'cx-oh', bold: true, children: ['Open · ' + open.length] }))
    open.forEach((it, i) => rows.push(itemRow(k, it, 'cx-o' + i)))
  }
  const who = participantsWords(b, r)
  if (who) rows.push(k.text('Participants: ' + who, { key: 'cx-people' }))
  ;(b.scope || []).forEach((s, i) => rows.push(k.text(scopeWords(s), { key: 'cx-scope' + i })))
  if (b.focus && b.focus.text) rows.push(k.text(focusWords(b.focus), { key: 'cx-focus' }))
  if (b.listening === false) rows.push(k.dim('Beacon is not listening to chat in this room.', 'cx-listen'))
  if (b.instructionsVersion != null) rows.push(k.dim('Organization instructions · version ' + b.instructionsVersion + ' · included when added', 'cx-instr'))
  if (!k.terminal && addKeyDrawn(r.roomKey)) rows.push(addButton(k, io, true))
  return rows
}

function addButton(k, io, primary) {
  const again = changedSince(ctx.roomKey)
  const text = ctx.adding ? 'adding…' : k.terminal ? (again ? 'add the shared context again' : 'add the shared context to my conversation') : again ? 'Add the shared context again' : 'Add the shared context to my conversation'
  return k.button({ key: 'cx-add', label: text, hotkey: 'a', primary, onPress: () => void addSharedContext(io) })
}

// artifactRow is one artifact as §4.10 draws it: the filename, then dim "markdown · 4 KB · bob's
// agent · 12m"; the focused row carries ▸. On the terminal the row is one plain Button (Enter adds
// it); on a surface with Svg the row's one button reads "Add to my context".
//   opts: { io, nowMs, selected, key, nameWidth }  (the row's button is always the default one)
export function artifactRow(k, a, opts = {}) {
  const { Text, Box } = k.els
  const io = opts.io
  const key = (opts.key || 'art') + '-' + a.artifactId
  const nowMs = opts.nowMs || 0
  const busy = ctx.addingArt.has(a.artifactId)
  const meta = metaWords(a, nowMs)
  const mark = opts.selected ? '▸ ' : '  '
  const press = () => {
    if (io) void addArtifact(io, a)
  }
  if (k.terminal) {
    const nameW = Math.max(8, Math.min((opts.nameWidth || 22) + 2, 26, Math.floor(k.width / 3)))
    const name = clipMiddle(a.filename, nameW - 2).padEnd(nameW)
    const text = busy ? mark + name + ' adding…' : mark + name
    return Box({
      key,
      flexDirection: 'row',
      columnGap: 1,
      children: [k.els.Button({ key, label: text, plain: true, onPress: press }), busy ? null : Text({ key: key + '-m', dimColor: true, wrap: 'truncate-end', children: [meta] })].filter(Boolean),
    })
  }
  const head = Box({
    key: key + '-h',
    flexDirection: 'row',
    columnGap: 0,
    flexShrink: 1,
    flexWrap: 'wrap',
    children: [
      Box({ key: key + '-slot', width: 2, flexShrink: 0, children: [Text({ key: key + '-mk', children: [mark.trim() || ' '] })] }),
      Text({ key: key + '-n', bold: true, children: [a.filename] }),
      Text({ key: key + '-m', dimColor: true, children: ['  ' + meta] }),
    ],
  })
  const button = k.button({ key: 'art-add-' + a.artifactId, label: busy ? 'adding…' : 'Add to my context', onPress: press })
  return Box({ key, flexDirection: 'row', columnGap: 1, justifyContent: 'space-between', alignItems: 'center', children: [head, button] })
}

function artifactsBlock(k, io, r, nowMs, sharedKeyDrawn) {
  const { Text } = k.els
  const rows = []
  const a = ctx.arts.answer
  if (!a) {
    rows.push(Text({ key: 'ax-h', bold: true, children: ['Artifacts'] }))
    rows.push(k.dim('Reading the artifacts…', 'ax-reading'))
    return rows
  }
  if (!a.ok) {
    rows.push(Text({ key: 'ax-h', bold: true, children: ['Artifacts'] }))
    rows.push(k.text(a.error || 'The artifacts could not be read.', { key: 'ax-err' }))
    return rows
  }
  const list = a.artifacts || []
  const total = a.total || list.length
  if (list.length === 0) {
    rows.push(Text({ key: 'ax-h', bold: true, children: ['Artifacts'] }))
    rows.push(k.dim('Nothing has been shared yet.', 'ax-none'))
    return rows
  }
  rows.push(Text({ key: 'ax-h', bold: true, children: ['Artifacts · ' + total] }))
  const shown = list.slice(0, ARTIFACTS_SHOWN)
  const sel = selectedArtifact()
  const nameWidth = Math.min(26, Math.max(...shown.map((x) => x.filename.length)))
  for (const art of shown) {
    const isSel = !!sel && sel.artifactId === art.artifactId
    rows.push(artifactRow(k, art, { io, nowMs, selected: isSel, nameWidth }))
    if (isSel) {
      const pv = artifactPreview(k, io, art, { open: ctx.open })
      if (pv) rows.push(pv)
      if (!k.terminal && canExpand(art)) rows.push(previewButton(k, art))
    }
  }
  if (total > shown.length) rows.push(k.dim('+' + (total - shown.length) + ' more in the war room', 'ax-more'))
  if (k.terminal) rows.push(k.dim(HINT, 'ax-hint'))
  return rows
}

// canExpand: the selected row has an excerpt to open whole.
function canExpand(a) {
  const p = ctx.previews.get(a.artifactId)
  return previewKind(a, false) === 'text' && !!p && p.state === 'ok' && !!p.text && !p.binary
}

function previewButton(k, a) {
  const b = k.button({ key: 'art-prev-' + a.artifactId, label: ctx.open ? 'Close the preview' : 'Open the preview', hotkey: 'v', onPress: () => togglePreview(k.io || null) })
  return k.els.Box({ key: 'art-prevb-' + a.artifactId, flexDirection: 'row', paddingLeft: 2, children: [b] })
}

function togglePreview(io) {
  ctx.open = !ctx.open
  if (io) io.invalidate()
}

// ---------------------------------------------------------------- the tab

export function tab(k, io, nowMs, args) {
  const chrome = !(args && args.chrome === false)
  const r = currentRoom()
  if (!r) {
    const rows = [k.text('This folder is not in a war room.', { key: 'cx-noroom' })]
    if (chrome) rows.push(keysRow(k, io, []))
    return rows
  }
  syncRoom(r.roomKey)
  warm(io)
  // Shown: what was new is seen.
  if (ctx.fresh[r.roomKey]) ctx.fresh[r.roomKey].clear()
  k = withIo(k, io)
  const addDrawn = addKeyDrawn(r.roomKey)
  ctx.rich = !!k.rich
  const sel = selectedArtifact()
  if (sel) void ensurePreview(io, sel, { rich: k.rich })
  const shared = sharedBlock(k, io, r, nowMs)
  const arts = artifactsBlock(k, io, r, nowMs, addDrawn && !k.terminal)
  const blocks = [k.card(shared, { key: 'cx-shared' }), k.card(arts, { key: 'cx-arts' })]
  // The expanded desktop pane draws two columns, as Home does, so a label and a button stay beside
  // the row they belong to.
  const two = k.rich && !k.mobile && k.width >= 90
  const rows = [two ? k.els.Box({ key: 'cx-body', flexDirection: 'row', columnGap: 2, alignItems: 'flex-start', children: blocks.map((b, i) => k.els.Box({ key: 'cx-col' + i, width: '48%', flexDirection: 'column', children: [b] })) }) : k.col(blocks, 'cx-body', 1)]
  if (chrome) {
    const foot = footer(k, io, nowMs)
    rows.push(k.col([foot, keysRow(k, io, keyList(k, io))].filter(Boolean), 'cx-chrome'))
  }
  return rows
}

// withIo lets this file's rows reach `io` from a Button press (previewButton), without widening the
// kit: the same kit, with the io the tab was drawn with.
function withIo(k, io) {
  return Object.create(k, { io: { value: io } })
}

// keyList is the tab's own letters: the add, then the preview toggle (terminal; the desktop draws
// both as buttons inside the blocks).
function keyList(k, io) {
  const out = []
  const r = currentRoom()
  if (!r || !k.terminal) return out
  if (addKeyDrawn(r.roomKey)) out.push(addButton(k, io, false))
  const sel = selectedArtifact()
  if (sel && canExpand(sel)) out.push(k.button({ key: 'cx-prev', label: ctx.open ? 'close the preview' : 'open the preview', hotkey: 'v', onPress: () => togglePreview(io) }))
  return out
}

export function keys(k, io, nowMs) {
  return keyList(k, io)
}

// keysRow: the tab's letters, then r: refresh, then close (esc); it breaks once before refresh
// when the row would not fit.
function keysRow(k, io, letters) {
  const refreshBtn = k.button({ key: 'cx-refresh', label: k.terminal ? 'refresh' : 'Refresh', hotkey: 'r', onPress: () => refresh(io) })
  const closeBtn = k.button({ key: 'cx-close', label: k.terminal ? 'close (esc)' : 'Close', dim: true, dismiss: true, onPress: () => closeConsole(io) })
  const own = letters.filter(Boolean)
  const need = own.reduce((n, b) => n + (b.props && b.props.label ? String(b.props.label).length + 5 : 24), 0) + 'r: refresh'.length + 2 + 'close (esc)'.length
  if (k.terminal && own.length > 0 && need > k.width) return k.col([k.row(own, 'cx-keys-a'), k.row([refreshBtn, closeBtn], 'cx-keys-b')], 'cx-keys', 0)
  return k.row([...own, refreshBtn, closeBtn], 'cx-keys')
}

function closeConsole(io) {
  consoleState.open = false
  io.close(CONSOLE)
}

// footer is "live · updated 4s ago" for the older of the two reads, "stale · …" when either failed,
// and " · reading…" while one runs.
export function footer(k, io, nowMs) {
  const r = currentRoom()
  const first = ctx.brief.answer && ctx.brief.answer.ok
  const second = ctx.arts.answer && ctx.arts.answer.ok
  if (!first && !second) return null
  const pick = ctx.brief.stale ? ctx.brief : ctx.arts.stale ? ctx.arts : first && second ? (ctx.brief.goodAt <= ctx.arts.goodAt ? ctx.brief : ctx.arts) : first ? ctx.brief : ctx.arts
  const w = liveWords(pick, nowMs, r)
  if (!w) return null
  const busy = ctx.brief.inFlight || ctx.arts.inFlight
  if (w.tone === 'neutral') return k.text(w.text + (busy ? ' · reading…' : ''), { key: 'cx-live', dimColor: true })
  return k.text(w.text, { key: 'cx-live', color: TONE[w.tone] })
}

// ---------------------------------------------------------------- the text fallback

// contextText is /landfall context where no pane can be drawn.
export async function contextText(io) {
  const r = currentRoom()
  if (!r) return 'This folder is not in a war room.'
  syncRoom(r.roomKey)
  if (!ctx.brief.answer) await loadBrief(io, r)
  if (!ctx.arts.answer) await loadArtifacts(io, r)
  const nowMs = await nowOf(io)
  const out = []
  const b = ctx.brief.answer
  if (!b || !b.ok) out.push((b && b.error) || 'The shared context could not be read.')
  else {
    out.push('Shared context' + (b.asOfSeq != null ? ' · as of #' + b.asOfSeq : ''))
    const est = b.established || []
    const open = b.open || []
    if (est.length === 0 && open.length === 0) out.push('No established or open items yet.')
    if (est.length > 0) {
      out.push('Established · ' + est.length)
      for (const it of est) {
        out.push('#' + it.seq + ' ' + it.statement + (it.by ? ' · ' + it.by : ''))
        if (it.admission) out.push('     ' + it.admission)
      }
    }
    if (open.length > 0) {
      out.push('Open · ' + open.length)
      for (const it of open) out.push('#' + it.seq + ' ' + it.statement + (it.by ? ' · ' + it.by : '') + (it.state ? '  ● ' + it.state : ''))
    }
    const who = participantsWords(b, r)
    if (who) out.push('Participants: ' + who)
    for (const s of b.scope || []) out.push(scopeWords(s))
    if (b.focus && b.focus.text) out.push(focusWords(b.focus))
    if (b.listening === false) out.push('Beacon is not listening to chat in this room.')
    if (b.instructionsVersion != null) out.push('Organization instructions · version ' + b.instructionsVersion + ' · included when added')
  }
  out.push('')
  const a = ctx.arts.answer
  if (!a || !a.ok) out.push((a && a.error) || 'The artifacts could not be read.')
  else {
    const list = a.artifacts || []
    if (list.length === 0) out.push('Artifacts', 'Nothing has been shared yet.')
    else {
      out.push('Artifacts · ' + (a.total || list.length))
      for (const x of list.slice(0, ARTIFACTS_SHOWN)) out.push(x.filename + '  ' + metaWords(x, nowMs))
      if ((a.total || list.length) > ARTIFACTS_SHOWN) out.push('+' + ((a.total || list.length) - ARTIFACTS_SHOWN) + ' more in the war room')
    }
  }
  return out.join('\n')
}

// ---------------------------------------------------------------- the adds

function incidentWords(inc) {
  if (!inc) return ''
  if (inc.displayId && inc.title) return inc.displayId + ' · ' + inc.title
  return inc.displayId || inc.title || ''
}

// addSharedContext appends the room's shared context to the person's conversation: one
// `landfall brief` read, one append, one toast. It reads fresh, never from what the tab shows.
//   opts.roomKey  the room to read (the join answers it before the watch stream has the room)
//   opts.joined   the incident's name when this is the post-join add (§2.6): the toast is then the
//                 join toast, "Joined <name>. Shared context added to your conversation: …"
// Answers null once added, or the reason it was not (a toast has already said it).
export async function addSharedContext(io, opts = {}) {
  const joined = opts.joined ? String(opts.joined) : ''
  const lead = joined ? joinLead(joined) : ''
  const roomKey = opts.roomKey || (currentRoom() || {}).roomKey
  if (!roomKey) {
    const why = 'This folder is not in a war room.'
    io.toast(joined ? lead + 'The shared context could not be read; add it from Context.' : unreadWords(why), 8000)
    return why
  }
  if (ctx.adding) return 'Already adding the shared context.'
  ctx.adding = true
  io.invalidate()
  try {
    const ans = await io.run(['brief', '--host', HOST, '--room', roomKey])
    if (!ans || !ans.ok || !ans.text) {
      const why = (ans && ans.error) || 'Landfall did not answer.'
      io.toast(joined ? lead + 'The shared context could not be read; add it from Context.' : unreadWords(why), 8000)
      return why
    }
    const now = await nowOf(io)
    const name = incidentWords(ans.incident) || joined || 'this war room'
    const note =
      '[Landfall war room] Shared context of ' +
      name +
      ', added by the person\nfrom the Landfall console at ' +
      hhmm(now) +
      'Z. Nobody asked a question; read it and use it when it is\nrelevant. Your landfall MCP session is in this room (it adopts it on your next tool call);\nget_updates tells you what changes after this.\n\n' +
      ans.text
    const deny = await appendNote(io, note)
    if (deny) {
      io.toast(clip(lead + 'Not added: ' + deny, TOAST_MAX), 8000)
      return deny
    }
    ctx.added[roomKey] = { atMs: now, asOfSeq: ans.asOfSeq ?? null }
    if (ctx.roomKey === roomKey) keep(ctx.brief, ans, now)
    io.toast(lead + addedWords(ans, !!joined), 8000)
    return null
  } finally {
    ctx.adding = false
    io.invalidate()
  }
}

// addedWords is the toast: counts, or "a fresh incident" when there is nothing yet.
export function addedWords(ans, joined = false) {
  const c = ans.counts || {}
  const est = c.established ?? (ans.established || []).length
  const open = c.open ?? (ans.open || []).length
  const head = joined ? 'Shared context added: ' : 'Shared context added to your conversation: '
  if (est === 0 && open === 0) {
    const n = c.participants ?? (ans.participants || []).length
    return head + 'a fresh incident, ' + n + (n === 1 ? ' participant.' : ' participants.')
  }
  return head + est + ' established, ' + open + ' open.'
}

// addArtifact reads one shared file and appends it to the person's conversation (§4.10's two
// shapes), with the toast. A second press while it is in flight does nothing. Answers null once
// added, or the reason it was not (a toast has already said it).
export async function addArtifact(io, a, opts = {}) {
  if (!a || !a.artifactId) return 'Nothing to add.'
  if (ctx.addingArt.has(a.artifactId)) return 'Already adding it.'
  const r = opts.roomKey ? { roomKey: opts.roomKey, displayId: opts.displayId } : currentRoom()
  if (!r) {
    io.toast(notAddedWords('this folder is not in a war room.'), 8000)
    return 'This folder is not in a war room.'
  }
  ctx.addingArt.add(a.artifactId)
  io.invalidate()
  try {
    const where = r.displayId || (r.title ? r.title : 'the war room')
    const ct = baseType(a.contentType)
    const binary = isKnownBinary(ct)
    let size = a.size
    let body = ''
    let cut = ''
    let toast
    const sharer = sharerLine(a)
    const when = a.sharedAt ? hhmm(a.sharedAt) + 'Z' : ''
    if (!binary) {
      if ((a.size ?? 0) > MAX_BYTES) {
        const why = 'the file is over 5 MiB.'
        io.toast(notAddedWords(why), 8000)
        return why
      }
      const ans = await io.run(['artifact', a.artifactId, '--host', HOST, ...roomArgs(r), '--max-chars', String(ADD_CHARS)])
      if (!ans || !ans.ok) {
        const why = (ans && ans.error) || 'Landfall did not answer.'
        io.toast(notAddedWords(why), 8000)
        return why
      }
      size = ans.size ?? size
      if (ans.binary) {
        body = binaryNote(where, a, ct, size, sharer, when, await webUrl(io, r))
        toast = binaryToast(a, size)
      } else {
        const text = typeof ans.text === 'string' ? ans.text : ''
        body = textNote(where, a, ct, size, sharer, when) + '\n\n' + text
        if (ans.truncated) {
          body += '\n\n[truncated at ' + fmtInt(ADD_CHARS) + ' of ' + fmtInt(ans.totalChars) + ' characters; read_artifact with artifactId ' + a.artifactId + ' and a larger maxChars reads the rest]'
          toast = [addedCutWords(a.filename, ans.totalChars), 8000]
        } else {
          toast = [addedFileWords(a.filename, size), 6000]
        }
        cut = ''
      }
    } else {
      body = binaryNote(where, a, ct, size, sharer, when, await webUrl(io, r))
      toast = binaryToast(a, size)
    }
    const deny = await appendNote(io, body + cut)
    if (deny) {
      io.toast(notAddedWords(deny), 8000)
      return deny
    }
    io.toast(toast[0], toast[1])
    return null
  } finally {
    ctx.addingArt.delete(a.artifactId)
    io.invalidate()
  }
}

function binaryToast(a, size) {
  return [addedNoteWords(a.filename, size), 6000]
}

// sharerLine: "bob's agent (Claude Code)", or the name alone.
function sharerLine(a) {
  const s = a.sharer || {}
  const who = sharerName(a)
  const tool = s.kind === 'agent' ? harnessOf(s.edgeAgentLabel) : ''
  return tool ? who + ' (' + tool + ')' : who
}

function textNote(where, a, ct, size, sharer, when) {
  return (
    '[Landfall war room] Artifact from ' +
    where +
    ': ' +
    a.filename +
    ' (' +
    (ct || 'text/plain') +
    ', ' +
    humanSize(size) +
    '), shared by ' +
    sharer +
    (when ? ' at ' + when : '') +
    ', added by the person from the Landfall console. Nobody asked a question; use it when it is relevant.'
  )
}

function binaryNote(where, a, ct, size, sharer, when, url) {
  return (
    '[Landfall war room] Artifact from ' +
    where +
    ': ' +
    a.filename +
    ' (' +
    (ct || 'application/octet-stream') +
    ', ' +
    humanSize(size) +
    '), shared by ' +
    sharer +
    (when ? ' at ' + when : '') +
    '. A binary file; it is not inlined.' +
    (url ? " The room's web viewer shows it: " + url + '.' : '') +
    ' read_artifact describes it.'
  )
}

// webUrl is the room's page in the web app, from the sign-in's web host (`landfall whoami`) and the
// room's slug and incident, or '' when any of them is not known.
async function webUrl(io, r) {
  const full = currentOf({ rooms: [] }) || r
  const live = currentRoom()
  const slug = (live && live.roomKey === r.roomKey ? live.slug : r.slug) || full.slug
  const id = (live && live.roomKey === r.roomKey ? live.incidentId : r.incidentId) || full.incidentId
  if (!slug || !id) return ''
  const who = await io.run(['whoami', '--json'])
  const web = who && who.ok && who.instance && who.instance.web
  return web ? String(web).replace(/\/+$/, '') + '/o/' + slug + '/incidents/' + id : ''
}
