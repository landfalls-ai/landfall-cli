import { expect, mock, test } from 'claude-code/testing'

// The vote card (FR-01, FR-16): `landfall watch` rooms[].votes (the human
// view, contracts/cli-json.md §1 as amended by review.md #2), and
// `landfall vote` (§2, review.md #1) mocked.

function vote(over: Record<string, unknown> = {}) {
  return {
    claimSeq: 212,
    class: 'finding',
    statement: 'The 5xx rise starts at 15:45Z, the same bucket as the v2.3.1 deploy',
    authoredBy: 'bob-claude-code',
    authorIsAgent: true,
    authorHuman: 'bob',
    positionsSoFar: 1,
    needed: 2,
    shortfall: 1,
    expiresInMs: 250000,
    stale: false,
    evidence: 'CloudWatch 5xxErrorRate · deploy record 15:48:59Z',
    mine: false,
    ...over,
  }
}

const MINE = vote({ claimSeq: 210, statement: 'My own finding', authoredBy: 'alice', authorIsAgent: false, authorHuman: undefined, mine: true, expiresInMs: 1000 })
const LATER_ONE = vote({ claimSeq: 214, statement: 'Pool sits at 41%', authoredBy: 'carol', authorIsAgent: false, authorHuman: undefined, expiresInMs: undefined, needed: undefined, shortfall: 2, positionsSoFar: 0 })

function snapshot(votes: unknown[]) {
  return (
    JSON.stringify({
      type: 'rooms',
      line: '🔴 Acme 168 · 1 new',
      rooms: [
        {
          roomKey: 'k1',
          incidentId: 'i1',
          displayId: 'Acme 168',
          title: 'cloudfront-5xx-high',
          slug: 'acme',
          connection: 'live',
          count: 0,
          addressed: 0,
          votesAwaited: votes.length,
          maxSeq: 214,
          digest: [],
          votes,
          status: { status: 'investigating', severity: 'SEV2', people: [] },
        },
      ],
    }) + '\n'
  )
}

const BAND = {
  plugin: 'landfall',
  component: 'AbovePrompt',
  requestId: 'band',
  viewport: { columns: 100, rows: 30 },
  props: { hasSurvey: false, isWorking: false, maxRows: 8, bodyColumns: 96, scroll: { offset: 0, bodyRows: 8 }, view: {} },
} as const

function ran(stdout: string) {
  return { value: { exitCode: 0, stdout, stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
}

type World = {
  votes: string[][]
  toasts: string[]
  opened: Array<{ id: string; focus?: boolean }>
  closed: string[]
  mcpCalls: number
  feed: (line: string) => Promise<void>
}

type Options = { answer?: string; placed?: boolean; viewport?: Record<string, unknown> }

// startWith starts the session with a watch stream the test feeds, and a CLI
// that answers `landfall vote` with `opts.answer`.
async function startWith($: any, on: any, first: string, opts: Options = {}) {
  const answer = opts.answer ?? '{"ok":true,"claimSeq":212,"position":"corroborate","admitted":true}'
  const placed = opts.placed ?? true
  const w: World = { votes: [], toasts: [], opened: [], closed: [], mcpCalls: 0, feed: async () => {} }
  const queue: string[] = [first]
  const clock = mock.clock(on)
  // A new line arrives as the watch child the mod starts again: the stream
  // ends after each batch, and register.js restarts it 3 s later.
  w.feed = async (line) => {
    queue.push(line)
    await clock.advance(3000)
  }
  on('ui.render', () => ({ type: 'Text', props: {}, children: [''] }))
  on('env.get', () => ({ value: undefined }))
  on('env.set', () => ({ value: undefined }))
  on('command.register', () => ({ value: undefined }))
  on('ui.status', () => ({ value: undefined }))
  on('process.spawn', async function* () {
    while (queue.length > 0) yield { stream: 'stdout', text: queue.shift() as string }
    return { value: { code: 0, signal: null } }
  })
  on('process.run', async ($: any, e: any) => {
    if (e.argv[1] === 'vote') {
      w.votes.push(e.argv.slice(1))
      await new Promise((r) => setTimeout(r, 5))
      return ran(answer + '\n')
    }
    return ran('')
  })
  on('mcp.call', () => {
    w.mcpCalls += 1
    return { value: { content: [], isError: false } }
  })
  on('ui.toast', ($: any, e: any) => {
    w.toasts.push(e.text)
    return { value: undefined }
  })
  on('ui.open', ($: any, e: any) => {
    w.opened.push({ id: e.id, focus: e.focus })
    return { value: placed ? { isPlaced: true } : { isPlaced: false, reason: 'headless' } }
  })
  on('ui.close', ($: any, e: any) => {
    w.closed.push(e.id)
    return { value: undefined }
  })
  on('session.start', () => ({ cwd: '/work' }))
  if (opts.viewport) {
    // The band is drawn before the vote arrives, so the mod knows the surface.
    const early = await $.ui.mount({ ...BAND, viewport: opts.viewport, surface: 'terminal' } as never)
    await early.unmount()
  }
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' } as never)
  await settle(w, 1)
  return w
}

async function settle(w: World, toasts: number) {
  for (let i = 0; i < 40 && w.toasts.length < toasts; i++) await new Promise((r) => setTimeout(r, 5))
}

for (const surface of ['terminal', 'desktop'] as const) {
  test(`${surface}: the band shows the most urgent vote, never the person's own, and 5 votes as the person`, async ($, on) => {
    const w = await startWith($, on, snapshot([LATER_ONE, MINE, vote()]))
    // One toast per request; the person's own claim is never one.
    expect(w.toasts).toEqual(["carol asked for your vote on #214", "bob's agent asked for your vote on #212"])

    const band = await $.ui.mount({ ...BAND, surface } as never)
    expect(await band.find({ type: 'Text', text: /vote waiting · 4m 10s left/ })).toBeDefined()
    expect(await band.find({ type: 'Text', text: /1 more waiting/ })).toBeDefined()
    expect(await band.find({ type: 'Text', text: "bob's agent: “The 5xx rise starts at 15:45Z, the same bucket as the v2.3.1 deploy”" })).toBeDefined()
    expect(await band.find({ type: 'Text', text: 'positions 1 of 2 · your vote would admit it' })).toBeDefined()
    expect(await band.find({ type: 'Text', text: /My own finding/ })).toBeUndefined()
    for (const key of ['vote-corroborate', 'vote-contest', 'vote-evidence', 'vote-later']) {
      expect(await band.find({ type: 'Button', key })).toBeDefined()
    }

    await band.press({ key: 'vote-corroborate' })
    expect(w.votes).toEqual([['vote', '--room', 'k1', '--claim', '212', '--position', 'corroborate']])
    expect(w.mcpCalls).toBe(0)
    expect(w.toasts.at(-1)).toBe('Corroborated #212 · admitted')
    // The next vote waiting takes the card.
    expect(await band.find({ type: 'Text', text: 'carol: “Pool sits at 41%”' })).toBeDefined()
    expect(await band.find({ type: 'Text', text: 'positions 0 · needs 2 more' })).toBeDefined()
  })

  test(`${surface}: a guest's recorded vote carries the CLI's note in its toast`, async ($, on) => {
    // internal/cli/vote.go: a guest's position counts as a person's, not a member's, and the answer says so.
    const note = "You joined as a guest, so your vote counts as a person's but not as a member's."
    const w = await startWith($, on, snapshot([vote()]), { answer: JSON.stringify({ ok: true, claimSeq: 212, position: 'corroborate', admitted: false, note }) })
    const band = await $.ui.mount({ ...BAND, surface } as never)
    await band.press({ key: 'vote-corroborate' })
    // A toast is at most 80 characters (the engine's box holds two rows): the outcome survives.
    expect(w.toasts.at(-1)!.length).toBeLessThanOrEqual(80)
    expect(w.toasts.at(-1)!.startsWith('Corroborated #212. You joined as a guest')).toBe(true)
  })

  test(`${surface}: a second press while the first vote runs records nothing more`, async ($, on) => {
    const w = await startWith($, on, snapshot([vote()]))
    const band = await $.ui.mount({ ...BAND, surface } as never)
    await Promise.allSettled([band.press({ key: 'vote-corroborate' }), band.press({ key: 'vote-corroborate' })])
    expect(w.votes).toHaveLength(1)
  })

  test(`${surface}: a refused vote says why and the card stays`, async ($, on) => {
    const w = await startWith($, on, snapshot([vote()]), { answer: '{"ok":false,"error":"Sign in to vote as yourself: run landfall login."}' })
    const band = await $.ui.mount({ ...BAND, surface } as never)
    await band.press({ key: 'vote-corroborate' })
    expect(w.toasts.at(-1)).toBe('Sign in to vote as yourself: run landfall login.')
    expect(await band.find({ type: 'Button', key: 'vote-corroborate' })).toBeDefined()
  })

  test(`${surface}: 6 and 7 open the console on Vote, never a pane of their own, and the person asked`, async ($, on) => {
    const w = await startWith($, on, snapshot([vote()]))
    const band = await $.ui.mount({ ...BAND, surface } as never)
    await band.press({ key: 'vote-contest' })
    expect(w.opened.at(-1)).toEqual({ id: 'landfall', focus: undefined })
    await band.press({ key: 'vote-evidence' })
    expect(w.opened.at(-1)).toEqual({ id: 'landfall', focus: undefined })
    expect(w.opened.map((o) => o.id)).not.toContain('landfall-vote')
    // Nothing was recorded by looking.
    expect(w.votes).toEqual([])
  })

  test(`${surface}: 8 sets the vote aside until a new one arrives`, async ($, on) => {
    const w = await startWith($, on, snapshot([vote()]))
    const band = await $.ui.mount({ ...BAND, surface } as never)
    await band.press({ key: 'vote-later' })
    expect(await band.find({ type: 'Button', key: 'vote-corroborate' })).toBeUndefined()
    await band.unmount()

    // The same vote again is still set aside, and told no second time.
    await w.feed(snapshot([vote({ positionsSoFar: 1, expiresInMs: 200000 })]))
    let again = await $.ui.mount({ ...BAND, surface } as never)
    expect(await again.find({ type: 'Button', key: 'vote-corroborate' })).toBeUndefined()
    await again.unmount()
    expect(w.toasts).toEqual(["bob's agent asked for your vote on #212"])

    // A new one brings the card back.
    await w.feed(snapshot([vote(), LATER_ONE]))
    await settle(w, 2)
    again = await $.ui.mount({ ...BAND, surface } as never)
    expect(await again.find({ type: 'Text', text: 'carol: “Pool sits at 41%”' })).toBeDefined()
    expect(w.toasts.at(-1)).toBe('carol asked for your vote on #214')
  })
}

test('a new vote opens the console on Vote unasked only where the surface docks it and is wide', async ($, on) => {
  const w = await startWith($, on, snapshot([vote()]), { viewport: { columns: 160, rows: 40, isFullscreen: true } })
  expect(w.toasts).toEqual(["bob's agent asked for your vote on #212"])
  // Unasked, so without the keyboard.
  expect(w.opened).toEqual([{ id: 'landfall', focus: undefined }])
})

test('on the main screen a new vote is a band row and a toast, no console', async ($, on) => {
  const w = await startWith($, on, snapshot([vote()]), { viewport: { columns: 200, rows: 40, isFullscreen: false } })
  expect(w.toasts).toEqual(["bob's agent asked for your vote on #212"])
  expect(w.opened).toEqual([])
})

test('a narrow docked terminal opens no console unasked', async ($, on) => {
  const w = await startWith($, on, snapshot([vote()]), { viewport: { columns: 100, rows: 40, isFullscreen: true } })
  expect(w.toasts).toEqual(["bob's agent asked for your vote on #212"])
  expect(w.opened).toEqual([])
})

test('mobile: a compact card with Corroborate and Later, and no contest', async ($, on) => {
  const w = await startWith($, on, snapshot([vote()]))
  const band = await $.ui.mount({ ...BAND, surface: 'mobile' } as never)
  expect(await band.find({ type: 'Text', text: 'Acme 168 · cloudfront-5xx-high' })).toBeDefined()
  expect(await band.find({ type: 'Text', text: '● SEV2' })).toBeDefined()
  expect(await band.find({ type: 'Text', text: "bob's agent: “The 5xx rise starts at 15:45Z, the same bucket as the v2.3.1 deploy”" })).toBeDefined()
  expect(await band.find({ type: 'Text', text: 'your vote · 1 of 2 · 4m 10s left' })).toBeDefined()
  expect(await band.find({ type: 'Button', key: 'vote-contest' })).toBeUndefined()
  expect(await band.find({ type: 'Button', key: 'vote-later' })).toBeDefined()
  await band.press({ key: 'vote-corroborate' })
  expect(w.votes).toEqual([['vote', '--room', 'k1', '--claim', '212', '--position', 'corroborate']])
  expect(w.toasts.at(-1)).toBe('Corroborated #212 · admitted')
})
