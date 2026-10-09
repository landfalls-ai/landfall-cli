import { expect, test } from 'claude-code/testing'
import { mock } from 'claude-code/testing'
import { baseRoom, pane } from './_room'
import { ARRIVAL, WALL_BEFORE } from './_wall-real'

// Live run 2026-10-09 (the Claude desktop app, bob on the shared wall): carol's widget landed, the
// band said "New on the wall", the Wall neither selected it nor scrolled to it. The cause was timing
// the first test never had: `landfall wall` takes a moment, and the Wall drew while it ran. loadWall
// had already moved lp.seq to the new widget's seq, so that draw read the OLD answer as "read since
// the widget landed", found no card, and spent the focus. These data are what the daemon's watch
// stream and `landfall wall` really emitted: newestWidget has no scope, the CLI puts the new stat
// before the pinned chart, and the wall holds more cards than it draws (the cap is six).

const wait = (ms: number) => new Promise((r) => setTimeout(r, ms))
const ROOM = { ...baseRoom({}), roomKey: 'k1', agent: { inRoom: true, label: 'claude-code' } }
const before = (WALL_BEFORE as any).widgets as any[]
const after = [...before.slice(0, -1), ARRIVAL, before[before.length - 1]]

test('desktop: a read that is still running when the Wall draws does not spend the arrival', async ($, on) => {
  let wallReads = 0
  let release: () => void = () => {}
  const gate = new Promise<void>((r) => (release = r))
  let releaseRead: () => void = () => {}
  const readGate = new Promise<void>((r) => (releaseRead = r))
  const mcp = { calls: 0 }
  mock.clock(on)
  on('ui.render', () => ({ type: 'Text', props: {}, children: [''] }))
  on('env.get', () => ({ value: undefined }))
  on('env.set', () => ({ value: undefined }))
  on('command.register', () => ({ value: undefined }))
  on('process.spawn', async function* () {
    const line = (over: Record<string, unknown>) => JSON.stringify({ type: 'rooms', line: '168', rooms: [{ ...ROOM, ...over }] }) + '\n'
    yield { stream: 'stdout', text: line({ widgetSeq: 61, maxSeq: 77, newestWidget: { seq: 61, title: 'NAT packet drops by gateway', type: 'chart' } }) }
    await gate
    yield { stream: 'stdout', text: line({ widgetSeq: 79, maxSeq: 79, newestWidget: { seq: 79, title: 'Probe widget one', type: 'stat' } }) }
    return { value: { code: 0, signal: null } }
  })
  on('process.run', async (_$: any, e: any) => {
    let answer: any = { ok: false, error: 'x' }
    if (e.argv[1] === 'wall') {
      wallReads += 1
      // The first read answers at once; the one after the arrival takes its time, as the CLI does.
      if (wallReads > 1) await readGate
      answer = { ...(WALL_BEFORE as any), widgets: wallReads > 1 ? after : before }
    }
    return { value: { exitCode: 0, stdout: JSON.stringify(answer) + '\n', stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
  })
  on('mcp.call', () => {
    mcp.calls += 1
    return { value: { content: [], isError: false } }
  })
  on('ui.status', () => ({ value: undefined }))
  on('ui.toast', () => ({ value: undefined }))
  on('ui.close', () => ({ value: undefined }))
  on('ui.focus', () => ({ value: {} }))
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('prompt.fill', () => ({ isFilled: true }))
  on('session.start', () => ({ cwd: '/work' }))
  await $.session.start({ surface: 'desktop', isInteractive: true, cwd: '/work' } as never)
  await wait(40)
  await $.command.run({ command: 'landfall', args: 'wall' })
  const props = { title: 'Landfall', isFocused: true, bodyColumns: 56, placement: 'dock', scroll: { offset: 0, bodyRows: 46 }, view: {} }
  const c = await $.ui.mount({ ...pane('landfall'), surface: 'desktop', props } as never)
  await wait(60)
  // The wall as the person left it: the top, the first card selected.
  expect(await c.find({ type: 'Button', text: '▸ Investigation status' })).toBeDefined()
  expect(wallReads).toBe(1)

  release()
  await wait(80)
  // The arrival is known, its read is running, and the pane has drawn meanwhile.
  expect(wallReads).toBe(2)
  // (the engine draws on the Wall's own invalidate, right after it starts the read)
  await c.drawn()
  await wait(40)

  releaseRead()
  await wait(120)
  expect(await c.find({ type: 'Button', text: '▸ Probe widget one' })).toBeDefined()
  // The window moved on to it: the cards above are out of it.
  expect(await c.find({ type: 'Button', text: /Investigation status$/ })).toBeUndefined()
  expect(mcp.calls).toBe(0)
})
