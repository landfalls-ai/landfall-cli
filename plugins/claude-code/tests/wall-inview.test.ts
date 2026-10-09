import { expect, test } from 'claude-code/testing'
import { mock } from 'claude-code/testing'
import { baseRoom, pane } from './_room'
import { texts } from './_console'

// The console, whole: a desktop dock too short for every card shows the card that just landed,
// not the top of the wall (live run 2026-10-09: a pin sat below the fold for a minute). The
// arrival comes down the real `landfall watch` stream, the wall is read through the real hooks.

const stat = (id: string, title: string, seq: number) => ({ id, type: 'stat', title, value: String(seq), seq })
const ROOM = { ...baseRoom({}), roomKey: 'k1', widgetSeq: 100, maxSeq: 100, agent: { inRoom: true, label: 'claude-code' } }
const consoleOn = (surface: string, bodyRows: number) => ({ ...pane('landfall'), surface, props: { title: 'Landfall', isFocused: true, bodyColumns: 60, placement: 'dock', scroll: { offset: 0, bodyRows }, view: {} } })
const wait = (ms: number) => new Promise((r) => setTimeout(r, ms))

for (const surface of ['desktop', 'terminal'] as const) {
  test(`${surface}: a pin that lands last on a long wall ${surface === 'desktop' ? 'is what the dock shows' : 'leaves the pane as it was'}`, async ($, on) => {
    let widgets = Array.from({ length: 5 }, (_, i) => stat('c' + i, 'Stat ' + i, 100 + i))
    let release: () => void = () => {}
    const gate = new Promise<void>((r) => (release = r))
    const toasts: string[] = []
    const mcp = { calls: 0 }
    mock.clock(on)
    on('ui.render', () => ({ type: 'Text', props: {}, children: [''] }))
    on('env.get', () => ({ value: undefined }))
    on('env.set', () => ({ value: undefined }))
    on('command.register', () => ({ value: undefined }))
    on('process.spawn', async function* () {
      const line = (over: Record<string, unknown>) => JSON.stringify({ type: 'rooms', line: '168', rooms: [{ ...ROOM, ...over }] }) + '\n'
      yield { stream: 'stdout', text: line({ newestWidget: { seq: 100, title: 'Stat 0', type: 'stat' } }) }
      await gate
      yield { stream: 'stdout', text: line({ widgetSeq: 120, maxSeq: 120, newestWidget: { seq: 120, title: 'NAT packet drops by gateway', type: 'stat', by: 'carol' } }) }
      return { value: { code: 0, signal: null } }
    })
    on('process.run', (_$: any, e: any) => {
      const answer = e.argv[1] === 'wall' ? { ok: true, sharedBy: 'carol', widgets, unavailable: [] } : { ok: false, error: 'x' }
      return { value: { exitCode: 0, stdout: JSON.stringify(answer) + '\n', stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
    })
    on('mcp.call', () => {
      mcp.calls += 1
      return { value: { content: [], isError: false } }
    })
    on('ui.status', () => ({ value: undefined }))
    on('ui.toast', (_$: any, e: any) => {
      toasts.push(e.text)
      return { value: undefined }
    })
    on('ui.close', () => ({ value: undefined }))
    on('ui.focus', () => ({ value: {} }))
    on('ui.open', () => ({ value: { isPlaced: true } }))
    on('prompt.fill', () => ({ isFilled: true }))
    on('session.start', () => ({ cwd: '/work' }))
    await $.session.start({ surface, isInteractive: true, cwd: '/work' } as never)
    await wait(40)
    await $.command.run({ command: 'landfall', args: 'wall' })
    const c = await $.ui.mount(consoleOn(surface, 14) as never)
    await wait(40)
    expect(await c.find({ type: 'Button', text: /Stat 0$/ })).toBeDefined()
    expect(await c.find({ type: 'Button', text: /NAT packet/ })).toBeUndefined()

    // carol pins a chart; the arrangement puts it last.
    widgets = [...widgets, stat('pin', 'NAT packet drops by gateway', 120)]
    release()
    await wait(80)
    expect(toasts.at(-1)).toBe('New on the wall: NAT packet drops by gateway · by carol')
    expect(await c.find({ type: 'Button', text: '▸ NAT packet drops by gateway' })).toBeDefined()
    if (surface === 'desktop') {
      // What is above it is out of the window: the dock shows the new card.
      expect(await c.find({ type: 'Button', text: /Stat 0$/ })).toBeUndefined()
    } else {
      expect(await c.find({ type: 'Button', text: /Stat 0$/ })).toBeDefined()
    }
    // No turn started and no MCP call made.
    expect(mcp.calls).toBe(0)
  })
}
