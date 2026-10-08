import { expect, mock, test } from 'claude-code/testing'

// Sound cues: one chime, off by default, once per event, never looped.

function snapshot(severity: string, votes?: Array<{ claimSeq: number; mine?: boolean }>) {
  return (
    JSON.stringify({
      type: 'rooms',
      line: '🔴 Landfall 172 · 1 new',
      rooms: [
        {
          roomKey: 'k1',
          displayId: 'Landfall 172',
          title: 'orders-db-replica-lag',
          count: 1,
          addressed: 0,
          votesAwaited: votes ? votes.length : 0,
          maxSeq: 10,
          digest: [],
          ...(votes ? { votes: votes.map((v) => ({ statement: 'replica lag starts at 15:45Z', authoredBy: 'bob', ...v })) } : {}),
          status: { status: 'investigating', severity, people: [] },
        },
      ],
    }) + '\n'
  )
}

const HINT = {
  plugin: 'landfall',
  component: 'PromptHint',
  requestId: 'hint',
  viewport: { columns: 120, rows: 30 },
  props: { isDraft: false, isWorking: false, hint: '? for shortcuts' },
} as const

// run starts the session, draws the hint once (which reads the switch), then
// feeds the rest of the stream one line at a time.
async function run($: any, on: any, surface: 'terminal' | 'desktop', switchOn: boolean | undefined, lines: string[]) {
  const plays: Array<{ asset?: string; shouldLoop: boolean }> = []
  let fed = 0
  let release: () => void = () => {}
  const gate = new Promise<void>((r) => (release = r))
  mock.clock(on)
  on('env.get', () => ({ value: undefined }))
  on('env.set', () => ({ value: undefined }))
  on('command.register', () => ({ value: undefined }))
  on('config.list', () => ({
    value: switchOn === undefined ? [] : [{ key: 'landfall.sound', label: 'Sound cues', kind: 'toggle', value: switchOn, provider: { plugin: 'landfall' }, isLocked: false }],
  }))
  on('ui.status', () => {
    fed += 1
    return { value: undefined }
  })
  on('ui.toast', () => ({ value: undefined }))
  on('ui.render', () => ({ type: 'Text', props: {}, children: [''] }))
  on('audio.play', ($: any, e: any) => {
    plays.push({ asset: e.clip.asset, shouldLoop: e.shouldLoop })
    return { value: undefined }
  })
  on('process.spawn', async function* () {
    yield { stream: 'stdout', text: lines[0] }
    await gate
    for (const l of lines.slice(1)) yield { stream: 'stdout', text: l }
    return { value: { code: 0, signal: null } }
  })
  on('session.start', () => ({ cwd: '/work' }))
  await $.session.start({ surface, isInteractive: true, cwd: '/work' } as never)
  for (let i = 0; i < 20 && fed === 0; i++) await new Promise((r) => setTimeout(r, 5))
  const hint = await $.ui.mount({ ...HINT, surface } as never)
  await hint.unmount()
  release()
  for (let i = 0; i < 40 && fed < lines.length; i++) await new Promise((r) => setTimeout(r, 5))
  // Every line reached the mod (each one sets the status line once).
  expect(fed).toBe(lines.length)
  return plays
}

for (const surface of ['terminal', 'desktop'] as const) {
  test(`${surface}: with the switch on, a room turning SEV1 and a new vote each chime once`, { options: { sound: true } }, async ($, on) => {
    const plays = await run($, on, surface, true, [
      snapshot('SEV2', []),
      snapshot('SEV1', []), // turned SEV1: chime
      snapshot('SEV1', []), // still SEV1: quiet
      snapshot('SEV1', [{ claimSeq: 212 }]), // a new vote: chime
      snapshot('SEV1', [{ claimSeq: 212 }]), // the same vote: quiet
      snapshot('SEV1', [{ claimSeq: 212 }, { claimSeq: 213, mine: true }]), // their own: quiet
    ])
    expect(plays).toEqual([
      { asset: 'sounds/chime.wav', shouldLoop: false },
      { asset: 'sounds/chime.wav', shouldLoop: false },
    ])
  })

  test(`${surface}: off by default, nothing plays`, async ($, on) => {
    const plays = await run($, on, surface, undefined, [snapshot('SEV2', []), snapshot('SEV1', [{ claimSeq: 212 }])])
    expect(plays).toEqual([])
  })
}

test('the first sight of a SEV1 room and of its waiting votes is not news', async ($, on) => {
  const plays = await run($, on, 'terminal', true, [snapshot('SEV1', [{ claimSeq: 212 }]), snapshot('SEV1', [{ claimSeq: 212 }])])
  expect(plays).toEqual([])
})

test('/landfall-sound says whether the chime is on and how to change it', async ($, on) => {
  on('command.register', () => ({ value: undefined }))
  on('config.list', () => ({ value: [{ key: 'landfall.sound', label: 'Sound cues', kind: 'toggle', value: false, provider: { plugin: 'landfall' }, isLocked: false }] }))
  const answer = await $.command.run({ command: 'landfall-sound', args: '' })
  expect(answer.text).toMatch(/^Sound cues are off\. To turn them on, open \/config/)
  expect(answer.text).toMatch(/macOS only/)
  expect(answer.text).not.toMatch(/—/)
})

test('/landfall-sound reads the switch as on', { options: { sound: true } }, async ($, on) => {
  on('command.register', () => ({ value: undefined }))
  on('config.list', () => ({ value: [{ key: 'landfall.sound', label: 'Sound cues', kind: 'toggle', value: true, provider: { plugin: 'landfall' }, isLocked: false }] }))
  const answer = await $.command.run({ command: 'landfall-sound', args: '' })
  expect(answer.text).toMatch(/^Sound cues are on/)
})
