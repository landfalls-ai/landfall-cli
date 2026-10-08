// Shared by the component tests: start a session whose `landfall watch`
// stream says one line, and stand in for the engine around the mod.
import { mock } from 'claude-code/testing'

export function baseRoom(status: Record<string, unknown>) {
  return {
    roomKey: 'k1',
    incidentId: 'i168',
    displayId: '168',
    title: 'cloudfront-5xx-high',
    slug: 'acme',
    connection: 'live',
    count: 0,
    addressed: 0,
    votesAwaited: 0,
    maxSeq: 233,
    digest: [],
    status: { status: 'investigating', severity: 'SEV2', people: [], ...status },
  }
}

export function pane(requestId: string) {
  return {
    plugin: 'landfall',
    component: 'Pane',
    requestId,
    viewport: { columns: 120, rows: 40 },
    props: { bodyColumns: 110, bodyRows: 30, view: {} },
  } as const
}

export const BAND = {
  plugin: 'landfall',
  component: 'AbovePrompt',
  requestId: 'band',
  viewport: { columns: 100, rows: 30 },
  props: { hasSurvey: false, isWorking: false, maxRows: 6, bodyColumns: 90, scroll: { offset: 0, bodyRows: 6 }, view: {} },
} as const

// startWith starts the session with `rooms` on the watch stream (none: no
// session start at all) and answers each `landfall <command>` from `answers`.
export async function startWith($: any, on: any, opts: { rooms?: unknown[]; answers?: Record<string, unknown>; placed?: boolean } = {}) {
  const runs: string[][] = []
  const toasts: string[] = []
  const filled: string[] = []
  const opened: string[] = []
  const counts = { mcp: 0, status: 0 }
  mock.clock(on)
  on('ui.render', () => ({ type: 'Text', props: {}, children: [''] }))
  on('env.get', () => ({ value: undefined }))
  on('env.set', () => ({ value: undefined }))
  on('command.register', () => ({ value: undefined }))
  on('process.spawn', async function* () {
    yield { stream: 'stdout', text: JSON.stringify({ type: 'rooms', line: '🔴 168 · 0 new', rooms: opts.rooms ?? [] }) + '\n' }
    return { value: { code: 0, signal: null } }
  })
  on('process.run', ($: any, e: any) => {
    runs.push([...e.argv])
    const key = e.argv[1] === 'lines' ? 'lines ' + e.argv[2] : e.argv[1]
    const answer = opts.answers?.[key] ?? { ok: false, error: 'unexpected ' + key }
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
  on('ui.close', () => ({ value: undefined }))
  on('ui.focus', () => ({ value: {} }))
  on('ui.open', ($: any, e: any) => {
    opened.push(e.id)
    return { value: opts.placed === false ? { isPlaced: false, reason: 'headless' } : { isPlaced: true } }
  })
  on('prompt.fill', ($: any, e: any) => {
    filled.push(e.text)
    return { isFilled: true }
  })
  on('session.start', () => ({ cwd: '/work' }))
  if (opts.rooms) {
    await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' } as never)
    for (let i = 0; i < 20 && counts.status === 0; i++) await new Promise((r) => setTimeout(r, 5))
  }
  return { runs, toasts, filled, opened, counts }
}
