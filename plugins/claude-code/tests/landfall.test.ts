import { expect, mock, test } from 'claude-code/testing'

// One snapshot line as `landfall watch` writes it.
const ROOM = {
  roomKey: 'k1',
  incidentId: 'i1',
  displayId: 'Landfall 166',
  title: 'Checkout 5xx',
  slug: 'acme',
  connection: 'live',
  count: 3,
  addressed: 1,
  votesAwaited: 0,
  maxSeq: 41,
  // The daemon's digest format: narrate.FormatEventLine, addressed lines marked.
  digest: [
    '#40 chat.message [maya@acme.com] — @alex can you check the pool?  ← addressed to a person',
    '#41 claim.staged [beacon@acme.com] — pool exhausted at 14:02',
    '#42 claim.admitted — #41 corroborated and admitted',
  ],
}
const SNAPSHOT = JSON.stringify({ type: 'rooms', line: 'Landfall 166 · 2 new', rooms: [ROOM] }) + '\n'

const BAND = {
  plugin: 'landfall',
  component: 'AbovePrompt',
  requestId: 'band',
  viewport: { columns: 100, rows: 30 },
  props: { hasSurvey: false, isWorking: false, maxRows: 6, bodyColumns: 90, scroll: { offset: 0, bodyRows: 6 }, view: {} },
} as const

const INJECTION = JSON.stringify({
  hookSpecificOutput: { hookEventName: 'UserPromptSubmit', additionalContext: 'Room update: pool exhausted' },
})

function processResult(stdout: string) {
  return { value: { exitCode: 0, stdout, stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
}

test('a prompt carries what the Go hook delivers, run with --from-mod', async ($, on) => {
  const runs: { argv: readonly string[]; stdin?: string }[] = []
  on('session.id', () => ({ value: 's1' }))
  on('session.cwd', () => ({ value: '/work' }))
  on('process.run', ($, e) => {
    runs.push({ argv: e.argv, stdin: e.init?.stdin })
    return processResult(INJECTION)
  })
  on('prompt.submit', ($, e) => ({ text: e.text, context: e.context }))

  const out = await $.prompt.submit({ text: 'what broke?', wait: false, origin: { kind: 'user' } } as never)

  expect(out.context).toEqual(['Room update: pool exhausted'])
  expect(runs[0].argv).toEqual(['landfall', 'hooks', 'user-prompt-submit', '--host', 'claude-code', '--from-mod'])
  expect(JSON.parse(runs[0].stdin ?? '{}')).toMatchObject({ hook_event_name: 'UserPromptSubmit', session_id: 's1', cwd: '/work', prompt: 'what broke?' })
})

test('a prompt goes as typed when the room has nothing', async ($, on) => {
  on('session.id', () => ({ value: 's1' }))
  on('session.cwd', () => ({ value: '/work' }))
  on('process.run', () => processResult(''))
  on('prompt.submit', ($, e) => ({ text: e.text, context: e.context }))

  const out = await $.prompt.submit({ text: 'hi', wait: false, origin: { kind: 'user' } } as never)

  expect(out.text).toBe('hi')
  expect(out.context).toBeUndefined()
})

test('a prompt goes as typed when landfall cannot run', async ($, on) => {
  on('session.id', () => ({ value: 's1' }))
  on('session.cwd', () => ({ value: '/work' }))
  on('process.run', () => ({ deny: 'landfall: not found' }))
  on('prompt.submit', ($, e) => ({ text: e.text, context: e.context }))

  const out = await $.prompt.submit({ text: 'hi', wait: false, origin: { kind: 'user' } } as never)

  expect(out.text).toBe('hi')
  expect(out.context).toBeUndefined()
})

test('the session announces the mod, and the room stream draws the band', async ($, on) => {
  const env = new Map<string, string | undefined>()
  const toasts: string[] = []
  const filled: string[] = []
  mock.clock(on)
  on('env.get', () => ({ value: undefined }))
  on('env.set', ($, e) => {
    env.set(e.name, e.value)
    return { value: undefined }
  })
  on('command.register', () => ({ value: undefined }))
  on('process.spawn', async function* () {
    yield { stream: 'stdout', text: SNAPSHOT }
    return { code: 0, signal: null }
  })
  on('ui.status', () => ({ value: undefined }))
  on('ui.toast', ($, e) => {
    toasts.push(e.text)
    return { value: undefined }
  })
  on('prompt.fill', ($, e) => {
    filled.push(e.text)
    return { isFilled: true }
  })
  on('ui.render', () => ({ type: 'Text', props: {}, children: [''] }))
  on('session.start', () => ({ cwd: '/work' }))

  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' } as never)
  // Let the stream's first line arrive.
  for (let i = 0; i < 20 && toasts.length === 0; i++) await new Promise((r) => setTimeout(r, 5))

  expect(env.get('LANDFALL_MOD')).toBe('claude-code')
  expect(toasts).toEqual(['Landfall 166 · Checkout 5xx: maya: @alex can you check the pool?'])

  const band = await $.ui.mount({ ...BAND, surface: 'terminal' })
  expect(await band.find({ type: 'Text', text: 'Landfall · Landfall 166 · Checkout 5xx · 3 new' })).toBeDefined()
  expect(await band.find({ type: 'Text', text: 'maya: @alex can you check the pool?' })).toBeDefined()
  await band.press({ key: 'catch-up' })
  expect(filled).toEqual(['Catch me up on what changed in the war room.'])

  // "later" sets the news aside until the room moves on.
  await band.press({ key: 'later' })
  await band.unmount()
  const again = await $.ui.mount({ ...BAND, surface: 'terminal' })
  expect(await again.find({ type: 'Text', text: /^Landfall · / })).toBeUndefined()
})

test('/room lists every untold line, a finding labelled as one', async ($, on) => {
  mock.clock(on)
  on('env.get', () => ({ value: undefined }))
  on('env.set', () => ({ value: undefined }))
  on('command.register', () => ({ value: undefined }))
  on('process.spawn', async function* () {
    yield { stream: 'stdout', text: SNAPSHOT }
    return { code: 0, signal: null }
  })
  on('ui.status', () => ({ value: undefined }))
  on('ui.toast', () => ({ value: undefined }))
  on('ui.open', () => ({ value: { isPlaced: false, reason: 'headless' } }))
  on('session.start', () => ({ cwd: '/work' }))

  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' } as never)
  for (let i = 0; i < 20; i++) await new Promise((r) => setTimeout(r, 5))

  // Where no pane can be drawn, /room answers in text.
  const answer = await $.command.run({ command: 'room', args: '' })
  expect(answer.text).toBe(
    'Landfall 166 · Checkout 5xx · 3 new\n  maya: @alex can you check the pool?\n  beacon (finding, awaiting a second person): pool exhausted at 14:02\n  finding admitted: #41 corroborated and admitted',
  )
})
