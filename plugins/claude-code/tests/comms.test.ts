import { expect, test } from 'claude-code/testing'
import { baseRoom, pane, startWith } from './_room'

const ROOM = baseRoom({})
const PANE = pane('landfall-comms')
const COMMS = {
  ok: true,
  messages: [
    { id: 'm1', state: 'sent', channel: 'slack', approvedBy: 'carol', at: '2026-10-08T15:58:00Z', text: 'We are investigating elevated errors on the storefront.' },
    { id: 'm2', state: 'draft', channel: 'statuspage', at: '2026-10-08T16:20:00Z', text: 'A fix is rolling out; error rates are falling.' },
  ],
}

for (const surface of ['terminal', 'desktop'] as const) {
  test(`/comms shows the latest updates, their state and who approved them, read only (${surface})`, async ($, on) => {
    const w = await startWith($, on, { rooms: [ROOM], answers: { comms: COMMS } })
    await $.command.run({ command: 'comms', args: '' })
    expect(w.runs[0]).toEqual(['landfall', 'comms', '--room', 'k1', '--host', 'claude-code'])
    const p = await $.ui.mount({ ...PANE, surface } as never)
    expect(await p.find({ type: 'Text', text: '● sent' })).toBeDefined()
    expect(await p.find({ type: 'Text', text: 'slack · approved by carol · 15:58Z' })).toBeDefined()
    expect(await p.find({ type: 'Text', text: '● draft' })).toBeDefined()
    expect(await p.find({ type: 'Text', text: 'A fix is rolling out; error rates are falling.' })).toBeDefined()
    expect(await p.find({ type: 'Text', text: 'Approving an update happens in the war room, in the browser.' })).toBeDefined()
    // Nothing to approve or send from here.
    const keys = (await p.findAll({ type: 'Button' })).map((b) => b.key)
    expect(keys).toEqual(['refresh', 'close'])
    await p.press({ key: 'refresh' })
    expect(w.runs.length).toBe(2)
  })

  test(`/comms shows the CLI's sentence when it cannot read (${surface})`, async ($, on) => {
    await startWith($, on, { rooms: [ROOM], answers: { comms: { ok: false, error: 'Stakeholder updates are not available to the CLI yet.' } } })
    await $.command.run({ command: 'comms', args: '' })
    const p = await $.ui.mount({ ...PANE, surface } as never)
    expect(await p.find({ type: 'Text', text: 'Stakeholder updates are not available to the CLI yet.' })).toBeDefined()
  })
}

test('/comms answers in text, newest first, where no pane can be drawn', async ($, on) => {
  await startWith($, on, { rooms: [ROOM], answers: { comms: COMMS }, placed: false })
  const answer = await $.command.run({ command: 'comms', args: '' })
  expect(answer.text).toBe(
    [
      'draft · statuspage · 16:20Z',
      '  A fix is rolling out; error rates are falling.',
      'sent · slack · approved by carol · 15:58Z',
      '  We are investigating elevated errors on the storefront.',
      'Approving an update happens in the war room, in the browser.',
    ].join('\n'),
  )
})

test('/comms outside a room says how to get into one', async ($, on) => {
  await startWith($, on, {})
  const answer = await $.command.run({ command: 'comms', args: '' })
  expect(answer.text).toBe('This folder is not in a war room. Open a share link from the room, or run /incidents to join one.')
})
