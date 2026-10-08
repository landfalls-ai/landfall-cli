// Shared by the console's own tests (console, incidents, room, live): a session whose `landfall
// watch` says `rooms` (none: no session start at all), a CLI that answers each `landfall <command>`
// from `answers` (a value, or a function of the argv), a `landfall login` stream from `login`,
// and the console pane as the engine raises it.
import { mock } from 'claude-code/testing'

export type Surface = 'terminal' | 'desktop' | 'vscode' | 'mobile'

export function consolePane(surface: Surface, bodyRows = 48, bodyColumns = 84) {
  return {
    plugin: 'landfall',
    surface,
    component: 'Pane',
    requestId: 'landfall',
    viewport: { columns: 168, rows: 52, isFullscreen: true },
    props: { title: 'Landfall', isFocused: true, bodyColumns, placement: 'dock', scroll: { offset: 0, bodyRows }, view: {} },
  } as const
}

export const BAND = {
  plugin: 'landfall',
  component: 'AbovePrompt',
  requestId: 'band',
  viewport: { columns: 120, rows: 40 },
  props: { hasSurvey: false, isWorking: false, maxRows: 10, bodyColumns: 110, scroll: { offset: 0, bodyRows: 10 }, view: {} },
} as const

export function roomOf(over: Record<string, unknown> = {}) {
  return {
    roomKey: 'k168',
    incidentId: 'i168',
    displayId: 'Landfall 168',
    title: 'cloudfront-5xx-high',
    slug: 'acme',
    connection: 'live',
    count: 0,
    addressed: 0,
    votesAwaited: 0,
    maxSeq: 233,
    digest: [] as string[],
    agent: { inRoom: true },
    status: {
      status: 'investigating',
      severity: 'SEV2',
      people: [
        { name: 'alice', here: true, agents: [{ tool: 'Codex', label: 'alice-codex', here: true }], latest: { seq: 198, text: 'Origin pool exhausted in us-east-1', state: 'contested', ageMs: 540000 } },
        { name: 'carol', here: true, browser: true, latest: { seq: 205, text: 'Rollback of web-edge to v2.3.0 is ready', state: 'admitted', ageMs: 180000 } },
        { name: 'dave', you: true, here: true, agents: [{ tool: 'Claude Code', label: 'dave-claude-code', here: true }] },
      ],
    },
    ...over,
  }
}

type Answer = unknown | ((argv: readonly string[]) => unknown)
type Opts = {
  rooms?: unknown[]
  answers?: Record<string, Answer>
  placed?: boolean
  login?: { pieces: Array<{ stream: 'stdout' | 'stderr'; text: string }>; code?: number; gate?: Promise<void> }
  surface?: Surface
}

export async function world($: any, on: any, opts: Opts = {}) {
  const runs: string[][] = []
  const toasts: string[] = []
  const filled: string[] = []
  const opened: Array<{ id: string; focus?: boolean; columns?: number }> = []
  const closed: string[] = []
  const copied: string[] = []
  const spawned: string[][] = []
  const counts = { mcp: 0, status: 0 }
  const clock = mock.clock(on)
  const session = mock.session(on)
  on('ui.render', () => ({ type: 'Text', props: {}, children: [''] }))
  on('env.get', () => ({ value: undefined }))
  on('env.set', () => ({ value: undefined }))
  const registered: string[] = []
  on('command.register', ($: any, e: any) => {
    registered.push(e.name)
    return { value: undefined }
  })
  on('session.id', () => ({ value: 's1' }))
  on('session.cwd', () => ({ value: '/work' }))
  on('prompt.submit', ($: any, e: any) => ({ text: e.text, context: e.context }))
  on('config.list', () => ({ value: [] }))
  on('process.spawn', async function* ($: any, e: any) {
    spawned.push([...e.argv])
    if (e.argv[1] === 'watch') {
      if (opts.rooms) yield { stream: 'stdout', text: JSON.stringify({ type: 'rooms', line: '🔴 Landfall 168 · 0 new', rooms: opts.rooms }) + '\n' }
      return { value: { code: 0, signal: null } }
    }
    if (e.argv[1] === 'login' && opts.login) {
      for (const p of opts.login.pieces) yield p
      if (opts.login.gate) await opts.login.gate
      return { value: { code: opts.login.code ?? 0, signal: null } }
    }
    return { value: { code: 1, signal: null } }
  })
  on('process.run', ($: any, e: any) => {
    runs.push([...e.argv])
    const key = e.argv[1] === 'lines' ? 'lines ' + e.argv[2] : e.argv[1]
    const given = opts.answers?.[key]
    const answer = typeof given === 'function' ? (given as (argv: readonly string[]) => unknown)(e.argv) : given ?? { ok: false, error: 'unexpected ' + key }
    return { value: { exitCode: 0, stdout: JSON.stringify(answer) + '\n', stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
  })
  on('mcp.call', () => {
    counts.mcp += 1
    return { value: { content: [], isError: false } }
  })
  on('ui.status', () => {
    counts.status += 1
    return { value: undefined }
  })
  on('ui.toast', ($: any, e: any) => {
    toasts.push(e.text)
    return { value: undefined }
  })
  on('ui.close', ($: any, e: any) => {
    closed.push(e.id)
    return { value: undefined }
  })
  on('ui.focus', () => ({ value: {} }))
  on('ui.copy', ($: any, e: any) => {
    copied.push(e.text)
    return { value: { isCopied: true } }
  })
  on('ui.open', ($: any, e: any) => {
    opened.push({ id: e.id, focus: e.focus, columns: e.columns })
    return { value: opts.placed === false ? { isPlaced: false, reason: 'headless' } : { isPlaced: true } }
  })
  on('prompt.fill', ($: any, e: any) => {
    filled.push(e.text)
    return { isFilled: true }
  })
  on('session.start', () => ({ cwd: '/work' }))
  if (opts.rooms) {
    await $.session.start({ surface: opts.surface ?? 'terminal', isInteractive: true, cwd: '/work' } as never)
    for (let i = 0; i < 40 && counts.status === 0; i++) await new Promise((r) => setTimeout(r, 5))
  }
  const count = (cmd: string) => runs.filter((r) => r[1] === cmd).length
  return { runs, toasts, filled, opened, closed, copied, spawned, counts, clock, session, count, registered }
}

// texts is every Text and Button's words in a mounted drawing, in order.
export async function texts(m: any): Promise<string[]> {
  const out: string[] = []
  walk(await m.drawn(), out)
  return out
}

function walk(n: any, out: string[]) {
  if (!n || typeof n !== 'object') return
  if (n.type === 'Button') out.push(String(n.props?.label ?? ''))
  if (n.type === 'Text') out.push((n.children ?? []).map((c: any) => (typeof c === 'string' ? c : '')).join(''))
  for (const c of n.children ?? []) walk(c, out)
}
