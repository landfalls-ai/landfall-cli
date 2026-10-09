import { expect, mock, test } from 'claude-code/testing'

// Beacon's ticker (FR-09): `landfall watch` status.beaconStep and
// status.beaconConclusion (contracts/cli-json.md §1, review.md #7).

const STEP = { text: 'comparing 5xx per target group in us-east-1', step: 6, runSeq: 2 }
const CONCLUSION = { text: 'origin-b in us-east-1c returns 5xx since 15:45Z', seq: 233 }

function snapshot(status: Record<string, unknown>) {
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
          votesAwaited: 0,
          maxSeq: 233,
          digest: [],
          status: { status: 'investigating', severity: 'SEV2', people: [], ...status },
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

async function startWith($: any, on: any, lines: string[]) {
  const filled: string[] = []
  let read = 0
  mock.clock(on)
  on('ui.render', () => ({ type: 'Text', props: {}, children: [''] }))
  on('env.get', () => ({ value: undefined }))
  on('env.set', () => ({ value: undefined }))
  on('command.register', () => ({ value: undefined }))
  on('ui.status', () => {
    read += 1
    return { value: undefined }
  })
  on('ui.toast', () => ({ value: undefined }))
  on('prompt.fill', ($: any, e: any) => {
    filled.push(e.text)
    return { isFilled: true }
  })
  on('process.spawn', async function* () {
    for (const l of lines) yield { stream: 'stdout', text: l }
    return { value: { code: 0, signal: null } }
  })
  on('session.start', () => ({ cwd: '/work' }))
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' } as never)
  for (let i = 0; i < 40 && read < lines.length; i++) await new Promise((r) => setTimeout(r, 5))
  return { filled }
}

for (const surface of ['terminal', 'desktop'] as const) {
  test(`${surface}: while Beacon runs, the band carries its step`, async ($, on) => {
    await startWith($, on, [snapshot({ beacon: 'investigating', beaconStep: { ...STEP, step: 5, text: 'listing target groups' } }), snapshot({ beacon: 'investigating', beaconStep: STEP })])
    const band = await $.ui.mount({ ...BAND, surface } as never)
    if (surface === 'terminal') {
      expect(await band.find({ type: 'Text', text: '◆ Beacon step 6 · comparing 5xx per target group in us-east-1' })).toBeDefined()
    } else {
      expect(await band.find({ type: 'Text', text: 'Beacon step 6 · comparing 5xx per target group in us-east-1' })).toBeDefined()
      // The step track: six steps, the fifth titled with what Beacon said then.
      const track = (await band.findAll({ type: 'Svg' })).find((x: any) => x.props.alt === 'Beacon at step 6') as any
      expect(track).toBeDefined()
      expect(track.props.source).toContain('Step 5: listing target groups')
      // A plain image: an interactive frame is a white page on a dark band (round 2 review, issue 3).
      expect(track.props.isInteractive).toBeFalsy()
    }
    expect(await band.find({ type: 'Button', key: 'beacon-read' })).toBeUndefined()
  })

  test(`${surface}: a conclusion replaces the step, and 9 drafts a prompt to read it`, async ($, on) => {
    const { filled } = await startWith($, on, [snapshot({ beacon: 'investigating', beaconStep: STEP }), snapshot({ beacon: 'concluded', beaconConclusion: CONCLUSION })])
    const band = await $.ui.mount({ ...BAND, surface } as never)
    expect(await band.find({ type: 'Text', text: /Beacon step/ })).toBeUndefined()
    expect(await band.find({ type: 'Text', text: surface === 'terminal' ? '◆ Beacon concluded' : 'Beacon concluded' })).toBeDefined()
    expect(await band.find({ type: 'Text', text: 'origin-b in us-east-1c returns 5xx since 15:45Z' })).toBeDefined()
    await band.press({ key: 'beacon-read' })
    // A draft, never sent.
    expect(filled).toEqual(["Read me Beacon's conclusion in Acme 168 · cloudfront-5xx-high: origin-b in us-east-1c returns 5xx since 15:45Z"])
    expect(await band.find({ type: 'Button', key: 'beacon-read' })).toBeUndefined()
  })
}

test('a conclusion the room already had when the session started is not news', async ($, on) => {
  await startWith($, on, [snapshot({ beacon: 'concluded', beaconConclusion: CONCLUSION })])
  const band = await $.ui.mount({ ...BAND, surface: 'terminal' } as never)
  expect(await band.find({ type: 'Button', key: 'beacon-read' })).toBeUndefined()
})

test('mobile: the step draws as a row with the mark', async ($, on) => {
  await startWith($, on, [snapshot({ beacon: 'investigating', beaconStep: STEP })])
  const band = await $.ui.mount({ ...BAND, surface: 'mobile' } as never)
  expect(await band.find({ type: 'Text', text: 'Beacon step 6 · comparing 5xx per target group in us-east-1' })).toBeDefined()
})
