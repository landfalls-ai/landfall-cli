// Delivery to the agent: on the person's next message the mod runs the same
// `landfall hooks user-prompt-submit` the settings hook ran, with --from-mod,
// and attaches what it returns as context only Claude reads. After a turn it
// offers the catch-up as a dim suggestion (Tab takes it; never sent alone).

import { CATCH_UP, HOST, pending, room } from '../core.js'
import { PANE } from './room.js'
import { scopeChanged } from './scope.js'

export function install(on) {
  on('prompt.submit', async ($, e, next) => {
    // The person has moved on: a room pane left open would keep the keyboard
    // and eat the first keys of their next message.
    $.ui.close({ id: PANE }).catch(() => {})
    // The room's scope block (scope.js) is refreshed when it changed, so this
    // message's request carries the room as it is now.
    if (scopeChanged()) await $.ui.invalidate('prompt.context').catch(() => {})
    let context = ''
    try {
      const payload = JSON.stringify({
        hook_event_name: 'UserPromptSubmit',
        session_id: await $.session.id(),
        cwd: await $.session.cwd(),
        prompt: e.text,
      })
      const run = await $.process.run([room.bin, 'hooks', 'user-prompt-submit', '--host', HOST, '--from-mod'], {
        stdin: payload,
        timeoutMs: 5000,
      })
      context = additionalContext(run.stdout)
    } catch {
      // No landfall on PATH, or it timed out: the prompt goes as typed.
    }
    if (!context) return next(e)
    room.laterAt = {}
    return next({ ...e, context: [...(e.context ?? []), context] })
  }).catch(async ($, e, next) => next(e)) // a failure never holds the person's message

  on('turn.complete', async ($, e, next) => {
    const result = await next(e)
    if (!e.agentId && !e.isAborted && pending().length > 0) {
      void $.prompt.suggest({ text: CATCH_UP })
    }
    return result
  })
}

// additionalContext reads the Go hook's stdout: one JSON object, or nothing.
function additionalContext(stdout) {
  const body = (stdout || '').trim()
  if (!body) return ''
  try {
    return JSON.parse(body)?.hookSpecificOutput?.additionalContext || ''
  } catch {
    return ''
  }
}

export async function band(io, e, k) {
  return null
}

export function onSnapshot(io, snap, prev) {}

export function start(io) {}
