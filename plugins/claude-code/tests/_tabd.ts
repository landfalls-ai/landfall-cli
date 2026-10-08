// Shared by the console-tab tests (vote, timeline, lb, more, comms, brain). The console itself is
// console.js's (another stream); a tab's body is the component's own `tab(k, io, nowMs, args)`, so
// these tests draw it the way console.js does: through a Pane render hook of the test's own that
// builds the kit from the engine's element table and an `io` over the hook's `$`, then mount it
// on each surface and act on it by key.
import { room } from '../hooks/core.js'
import { kit } from '../hooks/kit.js'
import { checkProps } from './_props'

export const NOW = 1_800_000_000_000

export type Draw = (k: any, io: any, nowMs: number, args: unknown) => unknown[] | Promise<unknown[]>

export type Ctl = {
  args: unknown
  nowMs: number
  toasts: string[]
  filled: Array<{ text: string; mode?: string }>
  opened: Array<{ id: string; title?: string }>
  appended: string[]
  io: any
  runs: string[][]
}

// ioOf is register.js's makeIo, spelled for the tests: it records what a tab asked for and never
// touches `$` (the Pane hook below draws; a press is acted on through the element's own handler).
export function ioOf(_$: any, surface: string, ctl: Ctl) {
  return {
    surface,
    fill: (text: string, mode?: string) => void ctl.filled.push({ text, mode }),
    toast: (text: string) => void ctl.toasts.push(text),
    open: (id: string, title: string) => void ctl.opened.push({ id, title }),
    close: () => {},
    invalidate: () => {},
    run: async (args: string[]) => {
      ctl.runs.push(args)
      return { ok: false, error: 'no answer' }
    },
    now: () => ctl.nowMs,
    append: async (text: string) => {
      ctl.appended.push(text)
      return null
    },
  }
}

// hookTab installs the test's Pane hook for `requestId` and answers the controls the test reads
// and moves (args, the clock, what io did).
export function hookTab(on: any, requestId: string, draw: Draw): Ctl {
  const ctl: Ctl = { args: null, nowMs: NOW, toasts: [], filled: [], opened: [], appended: [], io: null, runs: [] }
  on('ui.render', { component: 'Pane', requestId }, async ($: any, e: any) => {
    const k = kit($.ui.resolve(e), e)
    ctl.io = ioOf($, e.surface || 'terminal', ctl)
    const rows = await draw(k, ctl.io, ctl.nowMs, ctl.args)
    return k.els.Box({ flexDirection: 'column', children: rows.filter(Boolean) })
  })
  return ctl
}

export function paneProps(requestId: string, over: Record<string, unknown> = {}) {
  return {
    plugin: 'landfall',
    component: 'Pane',
    requestId,
    viewport: { columns: 120, rows: 40 },
    props: { bodyColumns: 84, bodyRows: 40, view: {} },
    ...over,
  } as const
}

export function ran(answer: unknown) {
  return { value: { exitCode: 0, stdout: JSON.stringify(answer) + '\n', stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
}

// A watch room as core.js keeps it.
export function watchRoom(over: Record<string, unknown> = {}) {
  return {
    roomKey: uniq('k'),
    incidentId: 'i168',
    displayId: 'Acme 168',
    title: 'cloudfront-5xx-high',
    slug: 'acme',
    connection: 'live',
    count: 0,
    addressed: 0,
    votesAwaited: 0,
    maxSeq: 233,
    digest: [],
    status: { status: 'investigating', severity: 'SEV2', people: [] },
    ...over,
  }
}

export function setRoom(rooms: unknown[]) {
  room.snapshot = { line: '', rooms: rooms as any }
  room.bin = 'landfall'
}

// uniq is a room key no other test used: a component keeps its state per room, so a test that
// takes its own room starts clean.
let n = 0
export function uniq(prefix = 'r') {
  n += 1
  return prefix + n
}

// A plain stand-in for the engine's element table: every element is { type, props, children }, so
// a test can read a tab's tree without mounting it (in-flight labels, toast copy).
export type Node = { type: string; props: any; children: unknown[] }
export function fakeKit(surface: 'terminal' | 'desktop' | 'mobile' = 'terminal') {
  const mk = (type: string) => (props: any) => (checkProps(type, props), { type, props: props || {}, children: (props && props.children) || [] })
  const els: any = { Box: mk('Box'), Text: mk('Text'), Button: mk('Button'), Link: mk('Link') }
  if (surface !== 'terminal') els.Svg = mk('Svg')
  if (surface === 'terminal') els.Raster = mk('Raster')
  if (surface !== 'mobile') els.Input = mk('Input')
  return kit(els, { surface, props: { bodyColumns: 84 }, viewport: { columns: 100, rows: 40 } } as any)
}

export function walk(tree: unknown, visit: (node: Node) => void) {
  if (Array.isArray(tree)) return tree.forEach((t) => walk(t, visit))
  if (!tree || typeof tree !== 'object') return
  const node = tree as Node
  visit(node)
  for (const c of node.children || []) walk(c, visit)
}

export function nodes(tree: unknown, type?: string): Node[] {
  const out: Node[] = []
  walk(tree, (x) => {
    if (!type || x.type === type) out.push(x)
  })
  return out
}

export function textsOf(tree: unknown): string[] {
  return nodes(tree, 'Text').map((t) => t.children.map(String).join(''))
}

export function buttonOf(tree: unknown, key: string): Node | undefined {
  return nodes(tree, 'Button').find((b) => b.props.key === key)
}

export function fakeIo(over: Record<string, unknown> = {}) {
  const log = { toasts: [] as string[], runs: [] as string[][], filled: [] as Array<{ text: string; mode?: string }>, opened: [] as string[], invalidations: 0 }
  const io: any = {
    surface: 'terminal',
    toast: (text: string) => void log.toasts.push(text),
    fill: (text: string, mode?: string) => void log.filled.push({ text, mode }),
    open: (id: string) => void log.opened.push(id),
    close: () => {},
    invalidate: () => void (log.invalidations += 1),
    run: async (args: string[]) => {
      log.runs.push(args)
      return { ok: true }
    },
    now: () => NOW,
    ...over,
  }
  return { io, log }
}

// What the console draws for a tab: its body, then the footer() and the keys() it exports. The tab's
// own body draws neither (round 7: one footer and one keys row, the console's).
export function chromed(mod: any, k: any, io: any, now: number, args?: unknown): any[] {
  const out = [...mod.tab(k, io, now, args)]
  const foot = typeof mod.footer === 'function' ? mod.footer(k, now) : null
  if (foot) out.push(foot)
  if (typeof mod.keys === 'function') out.push(...mod.keys(k, io, now, args))
  return out
}
