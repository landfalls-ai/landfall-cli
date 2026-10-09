import { expect, test } from 'claude-code/testing'
import { NOW, chromed, fakeIo, fakeKit, hookTab, nodes, paneProps, setRoom, textsOf, watchRoom } from './_tabd'
import { consoleState } from '../hooks/core.js'
import * as commsMod from '../hooks/components/comms.js'
import { labelIn, labelsIn } from './_label'

// `landfall comms` as the Go CLI prints it (internal/cli/comms.go): newest first, {id, state,
// channel, kind, text, approvedBy?, simulated?}, no time.
const COMMS = {
  ok: true,
  messages: [
    { id: 'm3', state: 'sent', channel: 'slack', kind: 'milestone', text: 'Practice run: mitigation in progress.', approvedBy: 'dana', simulated: true },
    { id: 'm2', state: 'draft', channel: 'statuspage', kind: 'status-update', text: 'A fix is rolling out; error rates are falling.' },
    { id: 'm1', state: 'approved', channel: 'slack', kind: 'status-update', text: 'We are investigating elevated errors on the storefront.', approvedBy: 'carol' },
  ],
}

function setup(answer: unknown) {
  const room = watchRoom()
  setRoom([room])
  const f = fakeIo({ run: async (args: string[]) => (f.log.runs.push(args), answer) })
  return { room, ...f }
}

for (const surface of ['terminal', 'desktop'] as const) {
  test(`${surface}: the engine draws the latest updates, their state and who approved them, read only`, async ($, on) => {
    const { io, log, room } = setup(COMMS)
    await commsMod.warm(io)
    expect(log.runs[0]).toEqual(['comms', '--room', room.roomKey, '--host', 'claude-code'])
    hookTab(on, 'd-comms', (k, i, now, args) => chromed(commsMod, k, i, now, args))
    const p = await $.ui.mount({ ...paneProps('d-comms'), surface } as never)
    expect((await p.find({ type: 'Text', text: 'Stakeholder updates' }))?.props.bold).toBe(true)
    expect(labelIn(await p.drawn(), 'sent')).toBeDefined()
    expect(labelIn(await p.drawn(), 'approved')?.tone).toBe('#2a78d6')
    expect(await p.find({ type: 'Text', text: 'status update · slack · approved by carol' })).toBeDefined()
    expect(await p.find({ type: 'Text', text: 'status update · statuspage' })).toBeDefined()
    // A simulated update says so; only one does.
    expect(labelsIn(await p.drawn(), /^simulated$/)).toHaveLength(1)
    expect(labelIn(await p.drawn(), 'simulated')?.tone).toBe('#8a5cd6')
    expect(await p.find({ type: 'Text', text: /\d\d:\d\dZ/ })).toBeUndefined()
    expect(labelIn(await p.drawn(), 'draft')).toBeDefined()
    expect(await p.find({ type: 'Text', text: 'A fix is rolling out; error rates are falling.' })).toBeDefined()
    expect((await p.find({ type: 'Text', text: 'Approving an update happens in the war room, in the browser.' }))?.props.dimColor).toBe(true)
    expect(await p.find({ type: 'Text', text: 'live · updated 0s ago' })).toBeDefined()
    // Nothing to approve or send from here: no key at all.
    expect(await p.findAll({ type: 'Button' })).toEqual([])
    expect(commsMod.keys(fakeKit(surface), io, NOW, null)).toEqual([])
  })
}

test('the chip count is the updates not yet sent, known only once read', async () => {
  const { io } = setup(COMMS)
  expect(commsMod.count()).toBeNull()
  await commsMod.warm(io)
  expect(commsMod.count()).toBe(2)
  setRoom([watchRoom()])
  expect(commsMod.count()).toBeNull()
  const sent = setup({ ok: true, messages: [COMMS.messages[0]] })
  await commsMod.warm(sent.io)
  expect(commsMod.count()).toBeNull()
})

test('the CLI sentence shows when it cannot read; reading and none are said', async () => {
  const room = watchRoom()
  setRoom([room])
  let answer: unknown = { ok: false, error: 'Stakeholder updates are not available to the CLI yet.' }
  const f = fakeIo({ run: async () => answer })
  expect(textsOf(chromed(commsMod, fakeKit('terminal'), f.io, NOW, null))).toContain('Reading stakeholder updates…')
  await commsMod.warm(f.io)
  expect(textsOf(chromed(commsMod, fakeKit('terminal'), f.io, NOW, null))).toContain('Stakeholder updates are not available to the CLI yet.')
  answer = { ok: true, messages: [] }
  await commsMod.refresh(f.io)
  expect(textsOf(chromed(commsMod, fakeKit('terminal'), f.io, NOW, null))).toContain('No stakeholder update has been drafted yet.')
})

test('outside a room the tab says so', async () => {
  setRoom([])
  const f = fakeIo()
  expect(textsOf(chromed(commsMod, fakeKit('terminal'), f.io, NOW, null))).toEqual(['This folder is not in a war room.'])
  expect(await commsMod.text(f.io, '')).toContain('This folder is not in a war room.')
})

test('the list reads every 30 s while shown and stops when the console closes', async () => {
  const { io, log } = setup(COMMS)
  consoleState.open = true
  consoleState.warm.add('more') // Comms reads while More is warm
  await commsMod.warm(io)
  await commsMod.tick(io, NOW + 20000)
  expect(log.runs).toHaveLength(1)
  await commsMod.tick(io, NOW + 30000)
  await new Promise((r) => setTimeout(r, 0))
  expect(log.runs).toHaveLength(2)
  consoleState.open = false
  await commsMod.tick(io, NOW + 90000)
  await commsMod.tick(io, NOW + 200000)
  expect(log.runs).toHaveLength(2)
})

test('a failed refresh keeps the list and the footer says stale', async () => {
  const room = watchRoom()
  setRoom([room])
  let answer: unknown = COMMS
  const f = fakeIo({ run: async () => answer })
  await commsMod.warm(f.io)
  answer = { ok: false, error: 'Your sign-in expired. Run landfall login.' }
  await commsMod.refresh(f.io)
  const texts = textsOf(chromed(commsMod, fakeKit('terminal'), f.io, NOW + 120000, null))
  expect(texts).toContain('A fix is rolling out; error rates are falling.')
  expect(texts).toContain('stale · updated 2m ago · Your sign-in expired. Run landfall login.')
})

test('reconnecting drops the word live from the footer', async () => {
  const room = watchRoom({ connection: 'reconnecting' })
  setRoom([room])
  const f = fakeIo({ run: async () => COMMS })
  await commsMod.warm(f.io)
  const texts = textsOf(chromed(commsMod, fakeKit('terminal'), f.io, NOW + 120000, null))
  expect(texts).toContain('updated 2m ago')
  expect(texts.some((t) => t.startsWith('live'))).toBe(false)
})

test('text answers in the CLI\'s order, newest first, with the approval note', async () => {
  const { io } = setup(COMMS)
  expect(await commsMod.text(io, '')).toBe(
    [
      'sent · simulated · milestone · slack · approved by dana',
      '  Practice run: mitigation in progress.',
      'draft · status update · statuspage',
      '  A fix is rolling out; error rates are falling.',
      'approved · status update · slack · approved by carol',
      '  We are investigating elevated errors on the storefront.',
      'Approving an update happens in the war room, in the browser.',
    ].join('\n'),
  )
})

test('no standalone pane or command is left behind', () => {
  const hooks: unknown[] = []
  commsMod.install((...a: unknown[]) => void hooks.push(a))
  expect(hooks).toEqual([])
  expect(commsMod.badge()).toBeNull()
  expect(nodes(chromed(commsMod, fakeKit('desktop'), fakeIo().io, NOW, null), 'Button')).toEqual([])
})

test('on the desktop the state label sits on the text baseline of its meta line', async () => {
  const { io } = setup(COMMS)
  await commsMod.warm(io)
  const head = nodes(chromed(commsMod, fakeKit('desktop'), io, NOW, null), 'Box').find((b) => b.props.key === 'ch-m1')
  expect(head?.props.alignItems).toBe('center')
})
