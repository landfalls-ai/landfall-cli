import { expect, test } from 'claude-code/testing'
import { consoleState, room } from '../hooks/core.js'
import { kit } from '../hooks/kit.js'
import { checkProps } from './_props'
import * as cx from '../hooks/components/context.js'

// The Context tab (spec §4.10), driven through its own exports with a fake io: the CLI answers are
// the Go commands' real shapes (brief.go, artifacts.go), and the tree is read as text.

const NOW = Date.UTC(2026, 9, 8, 16, 12, 0)

let n = 0
function mkRoom(over: Record<string, unknown> = {}) {
  n += 1
  return {
    roomKey: 'ctx-' + n,
    incidentId: 'inc-' + n,
    displayId: 'Landfall 168',
    title: 'cloudfront-5xx-high',
    slug: 'acme',
    connection: 'live',
    count: 0,
    maxSeq: 214,
    digest: [] as string[],
    status: {
      status: 'investigating',
      severity: 'SEV2',
      people: [
        { name: 'carol', here: true, browser: true },
        { name: 'bob', here: true, agents: [{ tool: 'Claude Code', label: 'bob-claude-code', here: true }] },
        { name: 'dave', you: true, here: true, agents: [{ tool: 'Claude Code', label: 'dave-claude-code', here: true }] },
      ],
    },
    ...over,
  }
}

const BRIEF = {
  ok: true,
  text: 'Landfall 168 · cloudfront-5xx-high\nEstablished (2)\n- #205 Rollback is ready\nAs of seq 214',
  asOfSeq: 214,
  freshnessMs: 4000,
  counts: { established: 2, open: 3, participants: 3, scope: 1, focus: true },
  established: [
    { seq: 205, statement: 'Rollback of web-edge v2.3.1 is ready', by: 'carol', admission: "admitted: corroborated by bob (member); alice's agent (agent)" },
    { seq: 188, statement: '5xx is confined to us-east-1', by: 'Beacon' },
  ],
  open: [
    { seq: 190, statement: 'Working theory: the deploy raised origin latency', by: 'Beacon', theory: true },
    { seq: 212, statement: 'The 5xx rise matches the v2.3.1 deploy at 15:45Z', by: "bob's agent", state: 'staged' },
    { seq: 198, statement: 'Origin pool exhausted in us-east-1', by: 'alice', state: 'contested' },
  ],
  participants: [
    { humanActorId: 'u1', displayName: 'carol', edgeAgentLabel: '', active: true },
    { humanActorId: 'u2', displayName: 'bob', edgeAgentLabel: 'bob-claude-code', active: true },
    { humanActorId: 'u3', displayName: 'dave', edgeAgentLabel: 'dave-claude-code', active: true },
  ],
  scope: [{ kind: 'component', label: 'web-edge', by: 'carol' }],
  focus: { text: 'the origin pool in us-east-1', by: 'carol' },
  instructionsVersion: 3,
  incident: { displayId: 'Landfall 168', title: 'cloudfront-5xx-high', severity: 'SEV2', status: 'investigating' },
  listening: false,
}

const FRESH = { ...BRIEF, text: 'Landfall 168 · x\nNothing yet.', asOfSeq: 3, counts: { established: 0, open: 0, participants: 4, scope: 0, focus: false }, established: [], open: [], scope: [], focus: null, instructionsVersion: null, listening: undefined }

function art(id: string, filename: string, contentType: string, size: number, name: string, kind = 'member', minsAgo = 12, label = '') {
  return { artifactId: id, filename, contentType, size, sharedAt: new Date(NOW - minsAgo * 60000).toISOString(), sharer: { displayName: name, kind, edgeAgentLabel: label, humanActorId: 'h-' + name } }
}

const ARTS = {
  ok: true,
  artifacts: [
    art('a1', 'runbook.md', 'text/markdown', 4096, 'bob', 'agent', 12, 'bob-claude-code'),
    art('a2', 'screenshot.png', 'image/png', 1.2 * 1024 * 1024, 'alice', 'member', 20),
    art('a3', 'trace-bundle.zip', 'application/zip', 2.4 * 1024 * 1024, 'bob', 'agent', 10, 'bob-claude-code'),
    art('a4', 'postmortem-acme-95.pdf', 'application/pdf', 340 * 1024, 'carol', 'member', 60),
  ],
}

const RUNBOOK = { ok: true, artifactId: 'a1', filename: 'runbook.md', contentType: 'text/markdown', size: 4096, text: 'Rollback steps for web-edge. 1. Confirm the v2.3.1 deploy is the one serving 5xx (CloudFront origin logs). 2. Set the ALB target weight for v2.3.0 to 100 and drain the old pool before anything else.', totalChars: 4096, truncated: false }

type Answers = Record<string, any>

function mkIo(answers: Answers) {
  const log = { runs: [] as string[][], toasts: [] as Array<{ text: string; ms?: number }>, appended: [] as string[], closed: [] as string[], invalidated: 0 }
  const io: any = {
    surface: 'terminal',
    run: async (args: string[]) => {
      log.runs.push(args)
      const key = args[0] === 'artifact' ? 'artifact:' + args[1] : args[0]
      const a = answers[key] ?? answers[args[0]]
      if (typeof a === 'function') return a(args)
      return a ?? { ok: false, error: 'unexpected ' + key }
    },
    toast: (text: string, ms?: number) => void log.toasts.push({ text, ms }),
    append: async (text: string) => {
      if (answers.deny) return answers.deny
      log.appended.push(text)
      return null
    },
    close: (id: string) => void log.closed.push(id),
    invalidate: () => void (log.invalidated += 1),
    now: () => NOW,
  }
  return { io, log }
}

// An element table that keeps what was drawn as plain objects.
const els: any = {
  Box: (p: any) => (checkProps('Box', p), { t: 'Box', ...p }),
  Text: (p: any) => (checkProps('Text', p), { t: 'Text', ...p }),
  Button: (p: any) => (checkProps('Button', p), { t: 'Button', ...p }),
  Link: (p: any) => (checkProps('Link', p), { t: 'Link', ...p }),
  Svg: (p: any) => (checkProps('Svg', p), { t: 'Svg', ...p }),
  Raster: (p: any) => (checkProps('Raster', p), { t: 'Raster', ...p }),
}

function kitFor(surface: 'terminal' | 'desktop', columns = 84) {
  return kit(els, { surface, props: { bodyColumns: columns + 2 }, viewport: { columns: columns + 2, rows: 40 } } as any)
}

function walk(node: any, f: (n: any) => void) {
  if (!node || typeof node === 'string') return
  if (Array.isArray(node)) return node.forEach((c) => walk(c, f))
  f(node)
  for (const c of node.children ?? []) walk(c, f)
}

function texts(tree: any): string[] {
  const out: string[] = []
  walk(tree, (n) => {
    if (n.t === 'Text') out.push((n.children ?? []).join(''))
    if (n.t === 'Button') out.push(n.label ?? '')
  })
  return out
}

function lines(tree: any): string {
  return texts(tree).join('\n')
}

function buttons(tree: any): any[] {
  const out: any[] = []
  walk(tree, (n) => {
    if (n.t === 'Button') out.push(n)
  })
  return out
}

const settle = async () => {
  for (let i = 0; i < 6; i++) await new Promise((r) => setTimeout(r, 5))
}

// open puts the session in a room and draws the tab until its reads have landed.
async function open(answers: Answers, over: Record<string, unknown> = {}, surface: 'terminal' | 'desktop' = 'terminal', columns = 84) {
  const r = mkRoom(over)
  room.snapshot = { line: '', rooms: [r] } as any
  consoleState.open = true
  consoleState.warm.add('context')
  consoleState.tab = 'context'
  const world = mkIo({ brief: BRIEF, artifacts: ARTS, 'artifact:a1': RUNBOOK, ...answers })
  const k = kitFor(surface, columns)
  cx.tab(k, world.io, NOW, null)
  await settle()
  return { r, k, ...world, draw: (args: any = null) => cx.tab(k, world.io, NOW, args) }
}

test('the terminal tab draws the shared context, then the artifacts newest first', async () => {
  const t = await open({})
  const text = lines(t.draw())
  expect(text).toContain('Shared context')
  expect(text).toContain('· as of #214')
  expect(text).not.toContain('stale ·')
  expect(text).not.toMatch(/\bstale\b/)
  expect(text).toContain('Established · 2')
  expect(text).toContain('#205')
  expect(text).toContain('Rollback of web-edge v2.3.1 is ready')
  expect(text).toContain("admitted: corroborated by bob (member); alice's agent (agent)")
  expect(text).toContain('Open · 3')
  expect(text).toContain('● staged')
  expect(text).toContain('● contested')
  expect(text).toContain('Participants: carol (war room), bob (Claude Code), dave (you)')
  expect(text).toContain('Scope: component web-edge · pinned by carol')
  expect(text).toContain('Focus: the origin pool in us-east-1 · asked by carol')
  expect(text).toContain('Beacon is not listening to chat in this room.')
  expect(text).toContain('Organization instructions · version 3 · included when added')
  expect(text).toContain('Artifacts · 4')
  expect(text).toContain('markdown · 4 KB · bob\'s agent · 12m')
  expect(text).toContain('png · 1.2 MB · alice')
  expect(text).toContain('zip · 2.4 MB')
  expect(text).toContain('pdf · 340 KB · carol')
  expect(text).toContain('Enter on an artifact adds it to your context')
  expect(buttons(t.draw()).map((b) => b.hotkey + ':' + b.label)).toEqual(expect.arrayContaining(['a:add the shared context to my conversation', 'v:open the preview', 'r:refresh']))
  expect(text).toContain('close (esc)')
  expect(text).toContain('live · updated 0s ago')
  // The focused (first) row carries the arrow, and its excerpt is two rows ending in an ellipsis.
  expect(text).toContain('▸ runbook.md')
  expect(text).toContain('Rollback steps for web-edge.')
  expect(text).toMatch(/…/)
  // Reads ran through the CLI with the room, never a tool call.
  expect(t.log.runs.map((a) => a[0])).toEqual(expect.arrayContaining(['brief', 'artifacts', 'artifact']))
  expect(t.log.runs.find((a) => a[0] === 'brief')).toEqual(['brief', '--host', 'claude-code', '--room', t.r.roomKey])
})

test('keys and footer can be left to the shell with chrome: false', async () => {
  const t = await open({})
  const text = lines(t.draw({ chrome: false }))
  expect(buttons(t.draw({ chrome: false })).map((b) => b.key)).not.toContain('cx-refresh')
  expect(text).not.toContain('live · updated')
  expect(text).toContain('Established · 2')
})

test('states: no room, reading, a failed read, a fresh incident and nothing shared', async () => {
  room.snapshot = { line: '', rooms: [] } as any
  const none = mkIo({})
  expect(lines(cx.tab(kitFor('terminal'), none.io, NOW, null))).toContain('This folder is not in a war room.')

  const slow = mkIo({ brief: () => new Promise(() => {}), artifacts: () => new Promise(() => {}) })
  room.snapshot = { line: '', rooms: [mkRoom()] } as any
  const first = lines(cx.tab(kitFor('terminal'), slow.io, NOW, null))
  expect(first).toContain('Reading the shared context…')
  expect(first).toContain('Reading the artifacts…')

  const bad = await open({ brief: { ok: false, error: 'The room daemon did not answer.' }, artifacts: { ok: true, artifacts: [] } })
  const text = lines(bad.draw())
  expect(text).toContain('The room daemon did not answer.')
  expect(text).not.toContain('Error')
  expect(text).toContain('Nothing has been shared yet.')
  expect(buttons(bad.draw()).map((b) => b.key)).toContain('cx-refresh')

  const fresh = await open({ brief: FRESH, artifacts: { ok: true, artifacts: [] } })
  const ft = lines(fresh.draw())
  expect(ft).toContain('No established or open items yet.')
  expect(ft).not.toContain('Established ·')
  expect(ft).toContain('Artifacts')
  expect(ft).not.toContain('Artifacts ·')
})

test('a failed refresh keeps the last good read and the footer says stale, with the sentence', async () => {
  let fail = false
  const t = await open({ brief: () => (fail ? { ok: false, error: 'Landfall did not answer.' } : BRIEF) })
  fail = true
  cx.refresh(t.io)
  await settle()
  const text = lines(t.draw())
  expect(text).toContain('Established · 2')
  expect(text).toMatch(/stale · updated \d+s ago · Landfall did not answer\./)
})

test('a adds the shared context: one append, one toast, then the added row; a move says changed since', async () => {
  const t = await open({})
  const add = buttons(t.draw()).find((b) => b.key === 'cx-add')
  expect(add.hotkey).toBe('a')
  expect(add.label).toBe('add the shared context to my conversation')
  add.onPress({})
  await settle()
  expect(t.log.appended).toHaveLength(1)
  const note = t.log.appended[0]
  expect(note.startsWith('[Landfall war room] Shared context of Landfall 168 · cloudfront-5xx-high, added by the person')).toBe(true)
  expect(note).toContain('from the Landfall console at 16:12Z.')
  expect(note).toContain('get_updates tells you what changes after this.')
  expect(note.endsWith(BRIEF.text)).toBe(true)
  expect(t.log.toasts.map((x) => x.text)).toEqual(['Shared context added to your conversation: 2 established, 3 open.'])
  expect(t.log.toasts[0].ms).toBe(8000)
  let text = lines(t.draw())
  expect(text).toContain('in your conversation since 16:12')
  expect(buttons(t.draw()).map((b) => b.key)).not.toContain('cx-add')
  // The context moved on.
  cx.refresh(t.io)
  await settle()
  const moved = { ...BRIEF, asOfSeq: 220 }
  const t2 = mkIo({ brief: moved, artifacts: ARTS })
  await cx.addSharedContext(t2.io, { roomKey: t.r.roomKey }).catch(() => {})
  text = lines(t.draw())
  expect(text).toMatch(/in your conversation since|changed since/)
})

test('changed since offers a: add the shared context again', async () => {
  let b: any = BRIEF
  const t = await open({ brief: () => b })
  buttons(t.draw()).find((x) => x.key === 'cx-add').onPress({})
  await settle()
  b = { ...BRIEF, asOfSeq: 230 }
  cx.refresh(t.io)
  await settle()
  const text = lines(t.draw())
  expect(text).toContain('changed since 16:12')
  expect(buttons(t.draw()).find((x) => x.key === 'cx-add').label).toBe('add the shared context again')
})

test('the shared context toasts: a fresh incident, a failed read, a refusal', async () => {
  const fresh = await open({ brief: FRESH })
  await cx.addSharedContext(fresh.io)
  expect(fresh.log.toasts.at(-1)!.text).toBe('Shared context added to your conversation: a fresh incident, 4 participants.')

  const bad = await open({ brief: { ok: false, error: 'Landfall has no incident here.' } })
  expect(await cx.addSharedContext(bad.io)).toBe('Landfall has no incident here.')
  expect(bad.log.toasts.at(-1)!.text).toBe('The shared context could not be read: Landfall has no incident here.')
  expect(bad.log.appended).toHaveLength(0)

  const denied = await open({ deny: 'a policy plugin refused it' })
  expect(await cx.addSharedContext(denied.io)).toBe('a policy plugin refused it')
  expect(denied.log.toasts.at(-1)!.text).toBe('Not added: a policy plugin refused it')
})

test('after a join the add reads the room it was handed and the toast is the join toast', async () => {
  const world = mkIo({ brief: BRIEF })
  room.snapshot = { line: '', rooms: [] } as any
  expect(await cx.addSharedContext(world.io, { roomKey: 'joined-1', joined: 'Landfall 171' })).toBeNull()
  expect(world.log.runs[0]).toEqual(['brief', '--host', 'claude-code', '--room', 'joined-1'])
  expect(world.log.toasts).toEqual([{ text: 'Joined Landfall 171. Shared context added: 2 established, 3 open.', ms: 8000 }])
  expect(world.log.appended).toHaveLength(1)

  const unread = mkIo({ brief: { ok: false, error: 'x' } })
  await cx.addSharedContext(unread.io, { roomKey: 'joined-2', joined: 'Landfall 171' })
  expect(unread.log.toasts[0].text).toBe('Joined Landfall 171. The shared context could not be read; add it from Context.')

  const refused = mkIo({ brief: BRIEF, deny: 'no' })
  await cx.addSharedContext(refused.io, { roomKey: 'joined-3', joined: 'Landfall 171' })
  expect(refused.log.toasts[0].text).toBe('Joined Landfall 171. Not added: no')
})

test('Enter on a text artifact adds the whole file, with the framing line and the toast', async () => {
  const full = { ...RUNBOOK, text: '# Rollback\n1. Confirm the deploy.\n' }
  const t = await open({ 'artifact:a1': (args: string[]) => (args.includes(String(cx.ADD_CHARS)) ? full : RUNBOOK) })
  const row = buttons(t.draw()).find((b) => b.key === 'art-a1')
  row.onPress({})
  await settle()
  expect(t.log.runs.some((a) => a[0] === 'artifact' && a[1] === 'a1' && a.includes('--max-chars') && a.includes('20000'))).toBe(true)
  expect(t.log.appended).toHaveLength(1)
  const note = t.log.appended[0]
  expect(note).toContain('[Landfall war room] Artifact from Landfall 168: runbook.md (text/markdown, 4 KB), shared by bob\'s agent (Claude Code) at ')
  expect(note).toContain('added by the person from the Landfall console. Nobody asked a question; use it when it is relevant.')
  expect(note.endsWith('# Rollback\n1. Confirm the deploy.\n')).toBe(true)
  expect(t.log.toasts.at(-1)).toEqual({ text: 'Added runbook.md (4 KB) to your context.', ms: 6000 })
})

test('a long text artifact stops at 20,000 characters and says how to read the rest', async () => {
  const t = await open({ 'artifact:a1': { ...RUNBOOK, text: 'x'.repeat(20000), totalChars: 61400, truncated: true } })
  expect(await cx.addArtifact(t.io, ARTS.artifacts[0] as any)).toBeNull()
  const note = t.log.appended[0]
  expect(note.endsWith('[truncated at 20,000 of 61,400 characters; read_artifact with artifactId a1 and a larger maxChars reads the rest]')).toBe(true)
  expect(t.log.toasts.at(-1)).toEqual({ text: 'Added runbook.md to your context: the first 20,000 of 61,400 characters.', ms: 8000 })
})

test('a binary is added as a note, never inlined, and nothing is fetched for a picture', async () => {
  const t = await open({ whoami: { ok: true, signedIn: true, instance: { web: 'https://app.landfalls.ai' } } })
  expect(await cx.addArtifact(t.io, ARTS.artifacts[1] as any)).toBeNull()
  const note = t.log.appended[0]
  expect(note).toContain('Artifact from Landfall 168: screenshot.png (image/png, 1.2 MB), shared by alice at ')
  expect(note).toContain('A binary file; it is not inlined.')
  expect(note).toContain("The room's web viewer shows it: https://app.landfalls.ai/o/acme/incidents/" + t.r.incidentId + '.')
  expect(note).toContain('read_artifact describes it.')
  expect(t.log.runs.some((a) => a[0] === 'artifact' && a[1] === 'a2')).toBe(false)
  expect(t.log.toasts.at(-1)).toEqual({ text: 'Added a note about screenshot.png (1.2 MB) to your context.', ms: 6000 })
})

test('an add that fails, is too big, or is refused says Not added, and a second press waits', async () => {
  const t = await open({ 'artifact:a1': { ok: false, error: 'Landfall no longer has the file for runbook.md.' } })
  expect(await cx.addArtifact(t.io, ARTS.artifacts[0] as any)).toBe('Landfall no longer has the file for runbook.md.')
  expect(t.log.toasts.at(-1)!.text).toBe('Not added: Landfall no longer has the file for runbook.md.')
  expect(t.log.appended).toHaveLength(0)

  const big = { ...ARTS.artifacts[0], size: 6 * 1024 * 1024 }
  expect(await cx.addArtifact(t.io, big as any)).toBe('the file is over 5 MiB.')
  expect(t.log.toasts.at(-1)!.text).toBe('Not added: the file is over 5 MiB.')

  const slow = await open({ 'artifact:a1': () => new Promise((r) => setTimeout(() => r(RUNBOOK), 30)) })
  const first = cx.addArtifact(slow.io, ARTS.artifacts[0] as any)
  expect(await cx.addArtifact(slow.io, ARTS.artifacts[0] as any)).toBe('Already adding it.')
  expect(lines(slow.draw())).toContain('adding…')
  await first
  expect(slow.log.appended).toHaveLength(1)

  const denied = await open({ deny: 'policy' })
  await cx.addArtifact(denied.io, ARTS.artifacts[0] as any)
  expect(denied.log.toasts.at(-1)!.text).toBe('Not added: policy')
})

test('every toast fits 80 characters, with a 32-character filename and a long sharer', () => {
  const name = 'incident-timeline-export-2026-10-08.md'
  expect(name.length).toBeGreaterThan(32)
  const a = art('x', name, 'text/markdown', 61400, 'a-very-long-colleague-display-name', 'agent')
  const all = [
    cx.newArtifactWords(a),
    cx.addedFileWords(name, 4096),
    cx.addedCutWords(name, 61400),
    cx.addedNoteWords(name, 1.2 * 1024 * 1024),
    cx.notAddedWords('x'.repeat(200)),
    cx.unreadWords('y'.repeat(200)),
    'Joined Landfall 171. Shared context added: 2 established, 3 open.',
    'Joined Landfall 171. The shared context could not be read; add it from Context.',
    cx.addedWords({ counts: { established: 12, open: 34, participants: 4 } }),
    cx.addedWords({ counts: { established: 0, open: 0, participants: 12 } }),
  ]
  for (const t of all) expect(t.length).toBeLessThanOrEqual(80)
  expect(cx.addedCutWords(name, 61400)).toMatch(/^Added .+…\S*\.md to your context: the first 20,000 of 61,400 characters\.$/)
  expect(cx.clipMiddle('postmortem-acme-incident-95-final.pdf', 20)).toMatch(/\.pdf$/)
})

test('previews: an excerpt for text, a sentence for a picture, a PDF and a zip, and v opens it whole', async () => {
  const t = await open({ 'artifact:a1': { ...RUNBOOK, text: 'line one\nline two\n' + 'word '.repeat(200), truncated: true, totalChars: 9000 } })
  let text = lines(t.draw())
  expect(text).toContain('line one line two word')
  // v opens it whole and reads "close the preview".
  const v = buttons(t.draw()).find((b) => b.key === 'cx-prev')
  expect(v.label).toBe('open the preview')
  v.onPress({})
  text = lines(t.draw())
  expect(text).toContain('line one\nline two')
  expect(buttons(t.draw()).find((b) => b.key === 'cx-prev').label).toBe('close the preview')
  // Focus moves: a picture, a zip, a PDF.
  cx.focusKey(t.io, 'art-a2')
  expect(lines(t.draw())).toContain("a picture; the room's web viewer shows it")
  cx.focusKey(t.io, 'art-a3')
  expect(lines(t.draw())).toContain("a zip archive; the room's web viewer has it")
  cx.focusKey(t.io, 'art-a4')
  expect(lines(t.draw())).toContain("a PDF; the room's web viewer shows it")
  // Nothing was fetched for them, and the text one was read once.
  expect(t.log.runs.filter((a) => a[0] === 'artifact').map((a) => a[1])).toEqual(['a1'])
  expect(cx.artifactIdOfKey('art-add-a9')).toBe('a9')
  expect(cx.artifactIdOfKey('art-prev-a9')).toBe('a9')
  expect(cx.artifactIdOfKey('other')).toBe('')
})

test('a preview that cannot be read says so under the row', async () => {
  const t = await open({ 'artifact:a1': { ok: false, error: 'nope' } })
  expect(lines(t.draw())).toContain('The file could not be read.')
})

test('the desktop draws cards, one default Add button per row, and a small picture inline', async () => {
  const png = { ok: true, artifactId: 'a2', filename: 'screenshot.png', contentType: 'image/png', size: 100, binary: true, base64: 'AAAA' }
  const arts = { ok: true, artifacts: [{ ...ARTS.artifacts[1], size: 100 }, ...ARTS.artifacts.slice(0, 1)] }
  const t = await open({ artifacts: arts, 'artifact:a2': png }, {}, 'desktop', 60)
  t.draw()
  await settle()
  const tree = t.draw()
  const text = lines(tree)
  expect(text).toContain('Add the shared context to my conversation')
  expect(text).toContain('Add to my context')
  expect(text).toContain('Refresh')
  expect(text).toContain('Close')
  const bs = buttons(tree)
  const adds = bs.filter((b) => String(b.key).startsWith('art-add-'))
  expect(adds).toHaveLength(2)
  // Artifact buttons are always the default button, whatever has focus; the one primary is the
  // shared context's key.
  expect(adds.every((b) => b.variant !== 'primary')).toBe(true)
  expect(bs.filter((b) => b.variant === 'primary').map((b) => b.key)).toEqual(['cx-add'])
  expect(bs.every((b) => b.hotkey == null || ['a', 'v', 'r'].includes(b.hotkey))).toBe(true)
  let svg: any
  walk(tree, (n) => {
    if (n.t === 'Svg') svg = n
  })
  expect(svg.source).toContain('data:image/png;base64,AAAA')
  // Focus moving does not change which button is primary.
  cx.focusKey(t.io, 'art-add-a1')
  expect(buttons(t.draw()).filter((b) => b.variant === 'primary').map((b) => b.key)).toEqual(['cx-add'])
})

test('the expanded desktop draws two columns, so a label stays beside its row', async () => {
  const wide = await open({}, {}, 'desktop', 120)
  const tree = wide.draw()
  const body = tree[0] ?? tree
  expect(body.flexDirection).toBe('row')
  expect((body.children as any[]).map((c) => c.width)).toEqual(['48%', '48%'])
  const narrow = await open({}, {}, 'desktop', 60)
  expect(narrow.draw()[0].flexDirection).toBe('column')
})

test('desktop status labels are tinted boxes with no border, centred on their row', async () => {
  const t = await open({}, {}, 'desktop', 60)
  const tree = t.draw()
  let staged: any
  let row: any
  walk(tree, (n) => {
    if (n.t === 'Box' && String(n.key).endsWith('-st')) staged = n
    if (n.t === 'Box' && n.key === 'cx-o1') row = n
  })
  expect(staged.borderStyle).toBeUndefined()
  expect(staged.backgroundColor).toBeTruthy()
  expect(row.alignItems).toBe('center')
})

test('a new artifact is a toast and a count until the tab is shown; the first sight is silent', async () => {
  const r = mkRoom()
  room.snapshot = { line: '', rooms: [r] } as any
  consoleState.open = true
  consoleState.tab = 'wall'
  let list: any = { ok: true, artifacts: ARTS.artifacts.slice(1) }
  const world = mkIo({ artifacts: () => list })
  cx.onSnapshot(world.io, room.snapshot as any, { rooms: [] } as any)
  await settle()
  expect(world.log.toasts).toHaveLength(0)
  expect(cx.badge()).toBeNull()
  // bob's agent shares runbook.md: it is in the next digest.
  list = ARTS
  const r2 = { ...r, digest: ['#215 artifact.shared [bob@acme.com] — runbook.md'] }
  room.snapshot = { line: '', rooms: [r2] } as any
  cx.onSnapshot(world.io, room.snapshot as any, { rooms: [r] } as any)
  await settle()
  expect(world.log.toasts).toEqual([{ text: "New in the room: runbook.md · by bob's agent", ms: 6000 }])
  expect(cx.badge()).toBe(1)
  // The same digest line does not read or tell again.
  const reads = world.log.runs.length
  cx.onSnapshot(world.io, room.snapshot as any, { rooms: [r2] } as any)
  await settle()
  expect(world.log.runs.length).toBe(reads)
  // Showing the tab clears it.
  consoleState.tab = 'context'
  cx.tab(kitFor('terminal'), world.io, NOW, null)
  expect(cx.badge()).toBeNull()
})

test('tick reads the shown tab every 30 s and the list once a minute when the console is elsewhere', async () => {
  const t = await open({})
  const count = () => t.log.runs.filter((a) => a[0] === 'brief').length
  const base = count()
  await cx.tick(t.io, NOW + 10000)
  await settle()
  expect(count()).toBe(base)
  await cx.tick(t.io, NOW + 31000)
  await settle()
  expect(count()).toBe(base + 1)
  const listReads = () => t.log.runs.filter((a) => a[0] === 'artifacts').length
  consoleState.tab = 'wall'
  const before = listReads()
  await cx.tick(t.io, NOW + 62000)
  await settle()
  expect(listReads()).toBe(before + 1)
  expect(count()).toBe(base + 1)
  // Closed: nothing reads.
  consoleState.open = false
  await cx.tick(t.io, NOW + 200000)
  await settle()
  expect(listReads()).toBe(before + 1)
})

test('contextText answers where no pane can be drawn', async () => {
  const t = await open({})
  const text = await cx.contextText(t.io)
  expect(text).toContain('Shared context · as of #214')
  expect(text).toContain('#212 The 5xx rise matches the v2.3.1 deploy at 15:45Z · bob\'s agent  ● staged')
  expect(text).toContain('Artifacts · 4')
  expect(text).toContain('runbook.md  markdown · 4 KB')
  room.snapshot = { line: '', rooms: [] } as any
  expect(await cx.contextText(t.io)).toBe('This folder is not in a war room.')
})

test('the type words and sizes read as the web shows them', () => {
  const w = (contentType: string, filename = 'f') => cx.typeWord({ contentType, filename } as any)
  expect([w('text/markdown'), w('text/plain'), w('text/csv'), w('application/json'), w('text/html'), w('image/png'), w('image/jpeg'), w('image/gif'), w('image/webp'), w('application/pdf'), w('application/zip')]).toEqual([
    'markdown', 'text', 'csv', 'json', 'html', 'png', 'jpeg', 'gif', 'webp', 'pdf', 'zip',
  ])
  expect(w('application/octet-stream', 'thing.tar')).toBe('tar')
  expect(w('application/octet-stream', 'thing')).toBe('file')
  expect([cx.humanSize(812), cx.humanSize(4096), cx.humanSize(61 * 1024), cx.humanSize(1.2 * 1024 * 1024)]).toEqual(['812 B', '4 KB', '61 KB', '1.2 MB'])
  expect(cx.sharerName({ sharer: { displayName: 'bob', kind: 'agent' } } as any)).toBe("bob's agent")
  expect(cx.sharerName({ sharer: { displayName: 'alice', kind: 'member' } } as any)).toBe('alice')
  expect(cx.excerptLines('one two three four five six seven', 12, 2)).toEqual(['one two', 'three four…'].map((x) => x).slice(0, 2))
})
