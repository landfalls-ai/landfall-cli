import { expect, mock, test } from 'claude-code/testing'

// The room's scope in the conversation's context: one prompt.context block,
// refreshed only when it changes, never room news.

function snapshot(status: Record<string, unknown>) {
  return (
    JSON.stringify({
      type: 'rooms',
      line: '🔴 Landfall 168 · 1 new',
      rooms: [
        {
          roomKey: 'k1',
          displayId: 'Landfall 168',
          title: 'cloudfront-5xx-high',
          count: 1,
          addressed: 0,
          votesAwaited: 0,
          maxSeq: 10,
          digest: ['#10 chat.message [bob@acme.com] — something only delivery carries'],
          status: { status: 'investigating', severity: 'SEV2', people: [], ...status },
        },
      ],
    }) + '\n'
  )
}

async function run($: any, on: any, lines: string[]) {
  const invalidated: string[] = []
  let fed = 0
  let step: () => void = () => {}
  let waiting = Promise.resolve()
  const next = () => {
    waiting = new Promise<void>((r) => (step = r))
  }
  next()
  mock.clock(on)
  on('env.get', () => ({ value: undefined }))
  on('env.set', () => ({ value: undefined }))
  on('command.register', () => ({ value: undefined }))
  on('ui.status', () => {
    fed += 1
    return { value: undefined }
  })
  on('ui.toast', () => ({ value: undefined }))
  on('ui.invalidate', ($: any, e: any) => {
    invalidated.push(e.event)
    return { value: undefined }
  })
  on('ui.close', () => ({ value: undefined }))
  on('session.id', () => ({ value: 's1' }))
  on('session.cwd', () => ({ value: '/work' }))
  on('process.run', () => ({ value: { exitCode: 0, stdout: '', stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }))
  on('prompt.submit', ($: any, e: any) => ({ text: e.text, context: e.context }))
  on('prompt.context', ($: any, e: any) => ({ blocks: e.blocks }))
  on('process.spawn', async function* () {
    for (const l of lines) {
      yield { stream: 'stdout', text: l }
      await waiting
    }
    return { value: { code: 0, signal: null } }
  })
  on('session.start', () => ({ cwd: '/work' }))
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' } as never)
  const settle = async (n: number) => {
    for (let i = 0; i < 40 && fed < n; i++) await new Promise((r) => setTimeout(r, 5))
  }
  await settle(1)
  return {
    invalidated,
    // advance lets the stream's next line through.
    advance: async () => {
      const want = fed + 1
      const s = step
      next()
      s()
      await settle(want)
    },
  }
}

const CORE = [{ name: 'currentDate', text: "Today's date is 2026-10-08." }]

test('the block names the room, its focus and its scope, after the engine\'s own blocks, and no news', async ($, on) => {
  await run($, on, [snapshot({ focus: 'web-edge', scope: ['component:web-edge', 'window:13:00Z-17:00Z'] })])
  const got = await $.prompt.context({ blocks: CORE } as never)
  expect(got.blocks.map((b: any) => b.name)).toEqual(['currentDate', 'landfallRoomScope'])
  const text = got.blocks[1].text
  expect(text).toMatch(/Landfall 168 · cloudfront-5xx-high: focus: web-edge; scope: component:web-edge, window:13:00Z-17:00Z/)
  expect(text).not.toMatch(/something only delivery carries/)
  expect(text).not.toMatch(/—/)
})

test('no focus and no scope: no block', async ($, on) => {
  await run($, on, [snapshot({})])
  const got = await $.prompt.context({ blocks: CORE } as never)
  expect(got.blocks.map((b: any) => b.name)).toEqual(['currentDate'])
})

test('a changed scope is refreshed before the next message; an unchanged one is left cached', async ($, on) => {
  const { invalidated, advance } = await run($, on, [snapshot({ focus: 'web-edge' }), snapshot({ focus: 'web-edge' }), snapshot({ focus: 'origin-b' })])
  await $.prompt.context({ blocks: CORE } as never)

  await advance() // the same focus
  await $.prompt.submit({ text: 'next', wait: false, origin: { kind: 'user' } } as never)
  expect(invalidated.filter((x) => x === 'prompt.context')).toEqual([])

  await advance() // the focus moved
  await $.prompt.submit({ text: 'again', wait: false, origin: { kind: 'user' } } as never)
  expect(invalidated.filter((x) => x === 'prompt.context')).toEqual(['prompt.context'])
  const got = await $.prompt.context({ blocks: CORE } as never)
  expect(got.blocks[1].text).toMatch(/focus: origin-b/)
})
