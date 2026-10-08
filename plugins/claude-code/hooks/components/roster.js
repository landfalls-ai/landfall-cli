// People (proposal item 03, FR-03; spec §4.3, §4.3.1): who is with me, where
// each person works, what their agents are doing, their latest contribution
// to the shared context with where it stands (staged, corroborated, contested,
// admitted, withdrawn) and its age, and the lines of investigation they hold.
// A press on a contribution quotes it into the prompt as a draft; nothing is
// sent. A press on a name opens that person's investigation in place: what
// they found and asked (the trail), the dashboard they built (their
// `edge.widget` snapshots, drawn as the Wall draws a person's dashboard) and
// the files they shared, from `landfall wall --person <humanActorId|me>`.
//
// The list reads `landfall watch` (rooms[0].status.people[]) and redraws with
// every snapshot; the person view reads the CLI every 15 s while it is shown,
// and at once when the room moves on. People are keyed by humanActorId (the
// watch row's, or the wall's `people[]` matched by displayName), never by
// position.
//
// THE TAB CONTRACT (core.js CONSOLE; console.js calls these by name):
//   tab(k, io, nowMs, args)  the body rows; args: a person's name (opens their investigation)
//   badge()                  people here, or null at 0
//   warm(io)                 reads the open person view once more (it is also `r`)
//   keys(k, io)              the tab's own letter Buttons: c, e; in a person view b, a, n
//   footer(k, nowMs)         the live line of the person view, or null on the list
//   text(io, args)           the tab as text, where no pane can be placed
//
// The pure helpers below (viaLine, latestWords, stateWord) are shared with the
// Home block and the band.

import { ago, clip, consoleState, currentRoom, quoteDraft, reading, roomName, whereIs } from '../core.js'
import { BUTTON_CHROME, clipToCells, textCells, TONE } from '../kit.js'
import { closed, drawn, due, liveFooter, livePane, readLive, WALL_MS, opened as markOpen } from '../live.js'
import { clipText, hhmm } from '../views.js'
import { claimLine, linesOf, lineLabel, lineWords, loneLines, releaseLine } from './lines.js'
import { addArtifact, artifactRow } from './context.js'
import { ageWords, dashboardTail, heading, idOfPerson, orderPeople, sharedAnswer, wallArgs, widgetCards } from './wall.js'

// The list's state: whether the claim field shows, a claim or release in
// flight, and the person view (`person` is the member key: their humanActorId,
// or `name:<name>` while nothing says who they are; '' is the list).
const ui = { claiming: false, busy: '', person: '', personName: '', selected: 0, pending: '', readFor: '' }
// The person view's live read: `landfall wall --person`.
const pv = livePane()

// reset starts the tab's state over (tests; a hot reload does the same by itself).
export function reset() {
  Object.assign(ui, { claiming: false, busy: '', person: '', personName: '', selected: 0, pending: '', readFor: '' })
  Object.assign(pv, livePane())
}

// The tab has no command or pane of its own: /landfall people opens the console.
export function install(on) {}

export async function band(io, e, k) {
  return null
}

export function onSnapshot(io, snap, prev) {
  if (!ui.person || !pv.open || consoleState.tab !== 'people' || pv.inFlight) return
  const r = currentRoom()
  if (r && typeof r.maxSeq === 'number' && r.maxSeq > pv.seq) void loadPerson(io)
}

export function start(io) {}

// tick: the person view shown reads again every 15 s; closing the console stops it.
export async function tick(io, nowMs) {
  if (pv.open && !reading('people')) closed(pv)
  if (!ui.person || !pv.open || !reading('people') || consoleState.tab !== 'people') return
  io.invalidate()
  if (due(pv, nowMs, WALL_MS)) void loadPerson(io)
}

// The order people are listed in: those here before those away, you last
// among them (you know what you said), then by name.
export function sortPeople(people) {
  return orderPeople(people)
}

// viaLine is where a person works and what their agents are doing:
// "Claude Code · comparing 5xx by region". A tool is named once however many
// sessions of it the person runs, and the same doing is said once.
export function viaLine(p) {
  const out = []
  const where = whereIs(p)
  if (where) out.push(where)
  else if (!p.here) out.push('away')
  for (const a of p.agents ?? []) if (a.here && a.doing && !out.includes(a.doing)) out.push(a.doing)
  return out.join(' · ')
}

// The words and tone for where a contribution stands.
const STATES = {
  staged: { word: 'staged', tone: 'warning' },
  corroborated: { word: 'corroborated', tone: 'info' },
  contested: { word: 'contested', tone: 'critical' },
  admitted: { word: 'admitted', tone: 'good' },
  withdrawn: { word: 'withdrawn', tone: 'neutral' },
  note: { word: 'note', tone: 'neutral' },
}

export function stateWord(state) {
  return (STATES[state] && STATES[state].word) || String(state || 'shared')
}

export function latestTone(state) {
  return (STATES[state] && STATES[state].tone) || 'neutral'
}

// seqAge is "#205 · 3m".
export function seqAge(latest) {
  return [latest.seq != null ? '#' + latest.seq : '', ago(latest.ageMs)].filter(Boolean).join(' · ')
}

// latestWords is a contribution on one line: 'admitted · #205 · 3m: "Rollback ready"'.
export function latestWords(latest, width = 120) {
  if (!latest || !latest.text) return ''
  const head = [stateWord(latest.state), seqAge(latest)].filter(Boolean).join(' · ')
  return head + ': "' + clip(latest.text, Math.max(10, width - head.length - 4)) + '"'
}

// ---------------------------------------------------------------------------
// The console contract.

export function tab(k, io, nowMs, args) {
  const r = currentRoom()
  if (!r) return [k.text('This folder is not in a war room.', { key: 'who-none', dimColor: true })]
  const people = orderPeople((r.status && r.status.people) || [])
  takeArgs(args)
  resolvePending(io, people)
  if (ui.person) {
    const p = personOf(ui.person, people)
    drawn(pv)
    const target = readTarget(p)
    // The wall's people[] can say who they are after they were chosen.
    if (target && ui.readFor !== target) {
      ui.readFor = target
      Object.assign(pv, livePane())
      markOpen(pv)
      void loadPerson(io)
    }
    return personView(k, io, nowMs, r, p)
  }
  return listView(k, io, r, people)
}

export function badge() {
  const n = currentPeople().filter((p) => p.here).length
  return n > 0 ? n : null
}

// warm reads the open person view once more; the list needs no read of its own.
export function warm(io) {
  if (ui.person && readTarget(personOf(ui.person, currentPeople()))) {
    markOpen(pv)
    void loadPerson(io)
  }
}

// keys are the tab's letters. The list: c claim a line, e release it while you hold one.
// A person's view: b back to people, a ask about the selected widget, n next widget.
export function keys(k, io) {
  const r = currentRoom()
  if (!r) return []
  if (ui.person) {
    const list = widgetsOf()
    const sel = list[Math.min(ui.selected, list.length - 1)]
    return [
      k.button({ key: 'back', label: k.terminal ? 'back to people' : 'Back to people', hotkey: 'b', onPress: () => back(io) }),
      sel ? k.button({ key: 'pv-ask', label: 'ask about ' + clipText(sel.title || sel.type || 'widget', 28), hotkey: 'a', onPress: () => askAbout(io, sel) }) : null,
      list.length > 1 ? k.button({ key: 'pv-next', label: 'next widget', hotkey: 'n', onPress: () => nextWidget(io) }) : null,
    ].filter(Boolean)
  }
  const out = []
  if (hasFields(k) && !ui.claiming) out.push(k.button({ key: 'claim', label: ui.busy === 'claim' ? 'claiming…' : 'claim a line…', hotkey: 'c', primary: true, onPress: () => openClaim(io) }))
  const mine = heldLine(r)
  if (mine) out.push(k.button({ key: 'release', label: ui.busy === mine.claimId ? 'releasing…' : 'release', hotkey: 'e', onPress: () => release(io, mine) }))
  return out
}

export function footer(k, nowMs) {
  if (!ui.person) return null
  return liveFooter(k, pv, nowMs, currentRoom(), 'pv-live')
}

// text is the tab where no pane can be placed (the old /who, /lines).
export async function text(io, args) {
  const r = currentRoom()
  if (!r) return 'This folder is not in a war room. Open a share link from the room, or join one from Incidents.'
  const people = orderPeople((r.status && r.status.people) || [])
  const want = String(args || '').trim().toLowerCase()
  const p = want ? people.find((x) => String(x.name).trim().toLowerCase() === want) || people.find((x) => String(x.name).trim().toLowerCase().startsWith(want)) : null
  if (!p) return rosterText(r)
  const target = readTarget(p)
  if (!target) return personText(null, p)
  const got = await io.run(wallArgs(r, target), { timeoutMs: 30000 })
  return personText(got, p)
}

// ---------------------------------------------------------------------------
// The list.

function currentPeople() {
  const r = currentRoom()
  return orderPeople((r && r.status && r.status.people) || [])
}

function listView(k, io, r, people) {
  const { Box, Text, Button, Input } = k.els
  const here = people.filter((p) => p.here).length
  const rows = []
  if (people.length === 0) rows.push(Text({ key: 'who-empty', dimColor: true, children: ['Nobody is in the room yet.'] }))
  else rows.push(Text({ key: 'who-h', bold: true, children: ['Here · ' + here] }))

  let quotable = false
  people.forEach((p, i) => {
    const id = 'p' + i
    const key = personKey(p)
    const name = p.name + (p.you ? ' (you)' : '')
    const via = viaWords(r, p)
    const l = p.latest
    const quoteButton = l && l.text
      ? Button({ key: 'quote-' + i, label: '“' + clip(l.text, Math.max(20, k.width - (k.terminal ? 8 : 2))) + '”', ...(k.terminal ? { plain: true } : {}), onPress: () => io.fill(quoteDraft(l.text)) })
      : null
    if (quoteButton) quotable = true
    const stateRow = l
      ? k.row([k.pill(stateWord(l.state), latestTone(l.state), 'ls-' + id), Text({ key: 'la-' + id, dimColor: true, children: [seqAge(l)] })], 'lr-' + id, 1)
      : Text({ key: 'ln-' + id, dimColor: true, children: ['nothing shared yet'] })
    const open = () => openPerson(io, key, p)

    if (k.terminal) {
      rows.push(
        k.row(
          [
            Text({ key: 'dot-' + id, color: p.here ? TONE.good : TONE.neutral, dimColor: !p.here, children: [p.here ? '●' : '○'] }),
            Button({ key: 'open-' + i, label: name, plain: true, dimColor: !p.here, onPress: open }),
            via ? Text({ key: 'via-' + id, dimColor: true, children: [clip(via, Math.max(10, k.width - name.length - 4))] }) : null,
          ],
          'pr-' + id,
          1,
        ),
      )
      rows.push(Box({ key: 'pl-' + id, paddingLeft: 2, flexDirection: 'column', children: [stateRow, quoteButton].filter(Boolean) }))
      return
    }
    // Desktop, VS Code, mobile: an avatar card. The investigation button sits flush right,
    // one column for every card (round 4 review, issue 8).
    rows.push(
      Box({
        key: 'card-' + id,
        flexDirection: 'column',
        rowGap: 1,
        borderStyle: 'round',
        borderColor: '#d9d9d6',
        paddingX: 1,
        children: [
          Box({
            key: 'ph-' + id,
            flexDirection: 'row',
            columnGap: 1,
            alignItems: 'center',
            children: [
              k.avatar(p.name, !!p.here, { key: 'av-' + id, you: !!p.you, px: 30 }),
              Box({
                key: 'nv-' + id,
                flexDirection: 'column',
                flexGrow: 1,
                flexShrink: 1,
                children: [Text({ key: 'nm-' + id, bold: true, dimColor: !p.here, children: [name] }), via ? Text({ key: 'via-' + id, dimColor: true, wrap: 'truncate-end', children: [via] }) : null].filter(Boolean),
              }),
              Button({ key: 'open-' + i, label: p.you ? 'Your investigation' : p.name + "'s investigation", onPress: open }),
            ],
          }),
          stateRow,
          quoteButton,
        ].filter(Boolean),
      }),
    )
  })

  const lone = loneLines(r, people)
  if (lone.length > 0) {
    rows.push(Text({ key: 'ln-h', bold: true, children: ['Lines'] }))
    lone.forEach((l, i) => rows.push(Text({ key: 'ln-' + i, wrap: 'truncate-end', children: [lineWords(l)] })))
  }
  if (ui.claiming && hasFields(k)) {
    rows.push(
      Input({
        key: 'claim-label',
        label: 'Line',
        placeholder: 'eu-west-1 5xx',
        submitLabel: k.terminal ? 'Enter: claim' : 'Claim',
        autoFocus: true,
        onSubmit: (value) => claim(io, value),
      }),
    )
  } else if (!hasFields(k)) {
    rows.push(Text({ key: 'ln-mobile', dimColor: true, children: ['To claim a line here, type /landfall lines and what you are on.'] }))
  }
  if (people.length > 0) rows.push(Text({ key: 'who-hint', dimColor: true, children: [quotable ? 'Enter on a name opens their investigation; on a contribution, quotes it.' : 'Enter on a name opens their investigation.'] }))
  return rows
}

// viaWords is where a person works, what their agents are doing, and the line they hold.
function viaWords(r, p) {
  return [viaLine(p), ...linesOf(r, p).map((l) => 'on ' + lineLabel(l))].filter(Boolean).join(' · ')
}

// ---------------------------------------------------------------------------
// A person's investigation.

// personKey is how a person is selected: their humanActorId, or their name until the CLI says.
function personKey(p) {
  const id = idOfPerson(p, sharedAnswer())
  return id || 'name:' + String(p.name || '').trim().toLowerCase()
}

// personOf is the watch row for a key, or a row of the key's own (a person who left the room).
function personOf(key, people) {
  const hit = people.find((p) => personKey(p) === key || 'name:' + String(p.name || '').trim().toLowerCase() === key)
  if (hit) return hit
  const wall = sharedAnswer()
  const row = ((wall && wall.people) || []).find((w) => w.humanActorId === key)
  return { name: (row && row.displayName) || ui.personName || 'someone', here: false, left: true, humanActorId: row ? row.humanActorId : '' }
}

// readTarget is what `--person` takes: `me` for you, a humanActorId, or '' when nothing says who they are.
function readTarget(p) {
  if (!p) return ''
  if (p.you) return 'me'
  return idOfPerson(p, sharedAnswer())
}

function takeArgs(args) {
  if (args == null || args === '') return
  if (consoleState.args === args) consoleState.args = null
  ui.pending = String(args).trim()
}

// resolvePending opens the investigation /landfall people <name> named.
function resolvePending(io, people) {
  const want = ui.pending.toLowerCase()
  if (!want) return
  ui.pending = ''
  const p = people.find((x) => String(x.name).trim().toLowerCase() === want) || people.find((x) => String(x.name).trim().toLowerCase().startsWith(want))
  if (p) openPerson(io, personKey(p), p)
}

// openPerson shows a person's investigation, and reads it at once when it is known who they are.
function openPerson(io, key, p) {
  ui.person = key
  ui.personName = (p && p.name) || ''
  ui.selected = 0
  ui.readFor = ''
  Object.assign(pv, livePane())
  const target = readTarget(p)
  if (target) {
    ui.readFor = target
    markOpen(pv)
    void loadPerson(io)
  }
  io.invalidate()
}

function back(io) {
  ui.person = ''
  ui.readFor = ''
  closed(pv)
  io.invalidate()
}

async function loadPerson(io) {
  const p = personOf(ui.person, currentPeople())
  const target = readTarget(p)
  if (!target) return
  const fetch = () => {
    const r = currentRoom()
    pv.seq = r && typeof r.maxSeq === 'number' ? r.maxSeq : -1
    io.invalidate()
    return io.run(wallArgs(r, target), { timeoutMs: 30000 })
  }
  await readLive(pv, fetch, async () => {
    try {
      return Number(await io.now())
    } catch {
      return pv.triedAt
    }
  })
  const n = widgetsOf().length
  if (ui.selected >= n) ui.selected = 0
  io.invalidate()
}

function widgetsOf() {
  return pv.answer && pv.answer.ok ? pv.answer.widgets || [] : []
}

function nextWidget(io) {
  const n = widgetsOf().length
  if (n > 0) ui.selected = (Math.min(ui.selected, n - 1) + 1) % n
  io.invalidate()
}

async function askAbout(io, w) {
  const r = currentRoom()
  const p = personOf(ui.person, currentPeople())
  const where = r ? r.displayId || roomName(r) : 'the war room'
  await io.fill('Tell me about the ' + (w.title || w.type) + ' widget' + (p.you ? ' on your dashboard' : ' on ' + p.name + "'s dashboard") + ' in ' + where + '.')
}

const GLYPH = { finding: '◇', note: '·', hypothesis: '?', query: '✓', suggestion: '◇' }
const TRAIL_MAX = 8

// trailWords is what one trail row says.
export function trailWords(t) {
  if (t.kind === 'query') return 'queried ' + t.text
  if (t.kind === 'suggestion') return 'suggested: ' + t.text
  return String(t.text || '')
}

function personView(k, io, nowMs, r, p) {
  const { Box, Text, Button } = k.els
  const mine = !!p.you
  const name = p.name
  const rows = []
  rows.push(Text({ key: 'pv-h', bold: true, children: [mine ? 'Your investigation' : name + "'s investigation"] }))
  rows.push(personRow(k, r, p))
  const empty = mine ? 'You have not shared anything yet.' : name + ' has not shared anything yet.'
  const target = readTarget(p)
  if (!target) {
    rows.push(Text({ key: 'pv-none', dimColor: true, children: [empty] }))
    return rows
  }
  const a = pv.answer
  if (!a) {
    rows.push(Text({ key: 'pv-read', dimColor: true, children: [mine ? 'Reading your investigation…' : 'Reading ' + name + "'s investigation…"] }))
    return rows
  }
  if (!a.ok) {
    rows.push(Text({ key: 'pv-err', children: [clipText(a.error || 'The investigation could not be read.', k.width)] }))
    return rows
  }
  const trail = a.trail || []
  const widgets = a.widgets || []
  const arts = a.artifacts || []
  if (trail.length === 0 && widgets.length === 0 && arts.length === 0) {
    rows.push(Text({ key: 'pv-none', dimColor: true, children: [empty] }))
    return rows
  }

  if (trail.length > 0) {
    const total = Math.max(a.totalTrail || 0, trail.length)
    rows.push(Text({ key: 'pv-f-h', bold: true, children: ['Findings · ' + total] }))
    trail.slice(0, TRAIL_MAX).forEach((t, i) => {
      const state = t.state ? k.pill(stateWord(t.state), latestTone(t.state), 'pv-s' + i) : null
      const words = trailWords(t)
      // Every finding is a quote button, on both surfaces: curly quotes where a desktop draws it as one.
      // On the terminal the budget is characters; off it the text is proportional, so the row's
      // real width is measured (time, glyph, gaps and the state label taken off the pane) and the
      // finding takes what is left, cut with an ellipsis only there (round 1 cut it at ~25 chars).
      let label
      if (k.terminal) {
        label = clipText(words, Math.max(10, k.width - 5 - 2 - 2 - (t.state ? stateWord(t.state).length + 4 : 0)))
      } else {
        // The state label wraps under the finding when both do not fit one row (the row is
        // `flexWrap: wrap`), so the finding is budgeted without it: it takes the full width.
        const taken = textCells(hhmm(t.at)) + textCells(GLYPH[t.kind] || '·') + 3
        const room = Math.max(8, k.width + 2 - 1 - taken - BUTTON_CHROME - textCells('“”'))
        label = '“' + clipToCells(words, room) + '”'
      }
      rows.push(
        Box({
          key: 'pv-t' + i,
          flexDirection: 'row',
          columnGap: 1,
          alignItems: 'center',
          ...(k.terminal ? {} : { flexWrap: 'wrap' }),
          children: [
            Text({ key: 'pv-ti' + i, dimColor: true, children: [hhmm(t.at)] }),
            Text({ key: 'pv-g' + i, children: [GLYPH[t.kind] || '·'] }),
            Button({ key: 'trail-' + i, label, ...(k.terminal ? { plain: true } : {}), onPress: () => io.fill(quoteDraft(words)) }),
            state ? Box({ key: 'pv-sp' + i, flexGrow: 1 }) : null,
            state,
          ].filter(Boolean),
        }),
      )
    })
    if (total > TRAIL_MAX) rows.push(Text({ key: 'pv-more', dimColor: true, children: ['+' + (total - TRAIL_MAX) + ' earlier in Timeline'] }))
  }

  rows.push(heading(k, 'pv-d', 'Dashboard', dashboardTail(widgets, a.totalWidgets, true, nowMs, pv.inFlight)))
  if (widgets.length === 0) rows.push(Text({ key: 'pv-dnone', dimColor: true, children: [mine ? 'You have not shared a dashboard yet.' : name + ' has not shared a dashboard yet.'] }))
  else {
    const onSelect = (i) => {
      ui.selected = i
      io.invalidate()
    }
    rows.push(...widgetCards(k, io, widgets, { prefix: 'pv', selected: Math.min(ui.selected, widgets.length - 1), onSelect, nowMs, snapshot: true, two: false }))
  }

  if (arts.length > 0) {
    rows.push(Text({ key: 'pv-a-h', bold: true, children: ['Artifacts · ' + arts.length] }))
    arts.slice(0, 8).forEach((x, i) => {
      const nameWidth = Math.max(...arts.slice(0, 8).map((y) => String(y.filename || '').length))
      const opts = { io, nowMs, key: 'art', nameWidth, onPress: () => addArtifact(io, x) }
      rows.push(artifactRow(k, x, opts) || fallbackArtifactRow(k, x, { ...opts, key: 'art-' + (x.artifactId || i) }, nowMs))
    })
    if (arts.length > 8) rows.push(Text({ key: 'pv-a-more', dimColor: true, children: ['+' + (arts.length - 8) + ' more in the war room'] }))
  }
  rows.push(Text({ key: 'pv-hint', dimColor: true, children: ['Enter on a finding quotes it; on an artifact, adds it to your context'] }))
  return rows
}

// personRow is the person as the list draws them, without the press: presence dot,
// name, where they work and their line; away is dim, and one who left says so.
function personRow(k, r, p) {
  const { Text } = k.els
  const name = p.name + (p.you ? ' (you)' : '')
  const via = p.left ? 'left the room' : viaWords(r, p)
  if (!k.terminal) {
    return k.row(
      [
        k.avatar(p.name, !!p.here, { key: 'pv-av', you: !!p.you, px: 30 }),
        k.col([Text({ key: 'pv-nm', bold: true, dimColor: !p.here, children: [name] }), via ? Text({ key: 'pv-via', dimColor: true, children: [via] }) : null], 'pv-nv'),
      ],
      'pv-row',
      1,
    )
  }
  return k.row(
    [
      Text({ key: 'pv-dot', color: p.here ? TONE.good : TONE.neutral, dimColor: !p.here, children: [p.here ? '●' : '○'] }),
      Text({ key: 'pv-nm', bold: true, dimColor: !p.here, children: [name] }),
      via ? Text({ key: 'pv-via', dimColor: true, children: [clip(via, Math.max(10, k.width - name.length - 4))] }) : null,
    ],
    'pv-row',
    1,
  )
}

// fallbackArtifactRow draws an artifact until context.js draws it: a plain Button
// that adds the file to the person's context.
function fallbackArtifactRow(k, x, opts, nowMs) {
  const { Button } = k.els
  const sharer = x.sharer || {}
  const who = sharer.displayName ? sharer.displayName + (sharer.kind === 'agent' ? "'s agent" : '') : ''
  const when = x.sharedAt ? ageWords(nowMs - Date.parse(x.sharedAt)).replace(/ ago$/, '') : ''
  const words = [typeWord(x.contentType, x.filename), sizeWords(x.size), who, when].filter(Boolean).join(' · ')
  return Button({ key: opts.key, label: clipText(String(x.filename || 'file') + '   ' + words, k.width), ...(k.terminal ? { plain: true } : {}), onPress: opts.onPress })
}

function typeWord(contentType, filename) {
  const t = String(contentType || '').toLowerCase()
  if (t.includes('markdown')) return 'markdown'
  if (t.includes('pdf')) return 'pdf'
  const m = /^(?:text|image|application)\/([a-z0-9.+-]+)/.exec(t)
  if (m) return m[1] === 'plain' ? 'text' : m[1]
  const ext = /\.([a-z0-9]+)$/i.exec(String(filename || ''))
  return ext ? ext[1].toLowerCase() : ''
}

function sizeWords(n) {
  if (typeof n !== 'number' || !isFinite(n)) return ''
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return Math.round(n / 1024) + ' KB'
  return (n / 1024 / 1024).toFixed(1) + ' MB'
}

// ---------------------------------------------------------------------------
// Claiming and releasing a line.

function hasFields(k) {
  return k.surface !== 'mobile' && typeof k.els.Input === 'function'
}

function heldLine(r) {
  const held = ((r.status && r.status.lines) || []).filter((l) => l && l.you && l.claimId)
  return held[0] || null
}

function openClaim(io) {
  ui.claiming = true
  io.invalidate()
}

// A toast is at most 80 characters: two rows of the engine's box (round 4 review, issue 2).
const TOAST_MAX = 80

async function claim(io, label) {
  label = String(label || '').trim()
  if (!label || !currentRoom()) return
  ui.busy = 'claim'
  io.invalidate()
  const got = await claimLine(io, label)
  ui.busy = ''
  if (got.ok) ui.claiming = false
  io.toast(clip(got.text, TOAST_MAX), got.ok ? 6000 : 8000)
  io.invalidate()
}

async function release(io, line) {
  ui.busy = line.claimId
  io.invalidate()
  const got = await releaseLine(io, line)
  ui.busy = ''
  io.toast(clip(got.text, TOAST_MAX), got.ok ? 6000 : 8000)
  io.invalidate()
}

// ---------------------------------------------------------------------------
// Text, where no pane can be drawn.

// rosterText is People as text.
export function rosterText(r) {
  const people = orderPeople((r.status && r.status.people) || [])
  const out = [roomName(r) + ' · ' + people.filter((p) => p.here).length + ' here']
  for (const p of people) {
    const via = viaLine(p)
    out.push('  ' + (p.here ? '● ' : '○ ') + p.name + (p.you ? ' (you)' : '') + (via ? ' · ' + via : ''))
    out.push('    ' + (p.latest && p.latest.text ? latestWords(p.latest) : 'Nothing shared to the context yet'))
  }
  return out.join('\n')
}

// personText is one person's investigation as text.
export function personText(a, p) {
  const head = p.you ? 'Your investigation' : p.name + "'s investigation"
  const empty = p.you ? 'You have not shared anything yet.' : p.name + ' has not shared anything yet.'
  if (!a) return head + '\n  ' + empty
  if (!a.ok) return a.error || 'The investigation could not be read.'
  const out = [head]
  const trail = a.trail || []
  if (trail.length > 0) {
    out.push('Findings · ' + Math.max(a.totalTrail || 0, trail.length))
    for (const t of trail.slice(0, TRAIL_MAX)) out.push('  ' + hhmm(t.at) + ' ' + (GLYPH[t.kind] || '·') + ' ' + trailWords(t) + (t.state ? '  ' + stateWord(t.state) : ''))
  }
  const widgets = a.widgets || []
  if (widgets.length > 0) {
    out.push('Dashboard · ' + widgets.length + (widgets.length === 1 ? ' widget' : ' widgets') + ' · snapshots')
    for (const w of widgets) out.push('  ' + (w.title || w.type))
  }
  const arts = a.artifacts || []
  if (arts.length > 0) {
    out.push('Artifacts · ' + arts.length)
    for (const x of arts) out.push('  ' + x.filename)
  }
  if (out.length === 1) out.push('  ' + empty)
  return out.join('\n')
}

const AVATAR = ['#8a5cd6', '#2a78d6', '#c2551f', '#2e7d46', '#9a6b00', '#5a5a5a', '#b03a6f', '#1f7a8c']

// avatarSvg is a round avatar with the person's initial and a presence dot.
export function avatarSvg(p) {
  let h = 0
  for (const c of String(p.name || '?')) h = (h * 31 + c.charCodeAt(0)) >>> 0
  const fill = p.you ? '#141414' : AVATAR[h % AVATAR.length]
  const initial = esc((String(p.name || '?').trim()[0] || '?').toUpperCase())
  return (
    '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 30 30" width="30" height="30">' +
    '<circle cx="15" cy="15" r="14" fill="' + fill + '"' + (p.here ? '' : ' fill-opacity=".45"') + '/>' +
    '<text x="15" y="19.5" text-anchor="middle" font-family="-apple-system,Segoe UI,sans-serif" font-size="13" font-weight="700" fill="#fff">' + initial + '</text>' +
    '<circle cx="25" cy="25" r="4.2" fill="' + (p.here ? TONE.good : TONE.neutral) + '" stroke="#fff" stroke-width="1.6"/>' +
    '</svg>'
  )
}

function esc(s) {
  return String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c])
}
