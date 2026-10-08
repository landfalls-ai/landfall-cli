// Sound cues, off by default (proposal item 11).
//
// One soft chime (sounds/chime.wav, made for the mod) when an incident the
// session is in becomes SEV1, or when a new finding waits on the person's vote.
// Once per event, never looped, never room text spoken aloud: $.audio.speak
// would read other people's words out on someone's machine.
//
// THE SWITCH is the manifest's `userConfig` field `sound` (default false), a
// row in /config. register(on, options) receives it; until register.js hands
// it to install(on, options), the first draw of the prompt hint reads it from
// /config (a change there reloads the mod, which reads it again).
//
// $.audio.play plays through afplay, so the chime sounds on macOS only;
// elsewhere it is a quiet no-op.

import { addCommand, roomName } from '../core.js'

export const CHIME = 'sounds/chime.wav'

// on: true, false, or undefined until read. seen: per room, what the last
// snapshot said, so only a change chimes and the first sight never does.
export const sound = { on: undefined, reading: false, sev1: {}, votes: {} }

export function install(on, options) {
  if (options && typeof options.sound === 'boolean') sound.on = options.sound

  addCommand({ name: 'landfall-sound', description: 'Say whether Landfall plays its chime, and how to change it' })

  on('command.run', { command: 'landfall-sound' }, async ($) => {
    if (sound.on === undefined) sound.on = await readSwitch($)
    return { text: switchText(sound.on) }
  })

  // The first draw reads the switch where register.js did not pass it.
  on('ui.render', { component: 'PromptHint' }, async ($, e, next) => {
    if (sound.on === undefined && !sound.reading) {
      sound.reading = true
      sound.on = await readSwitch($)
      sound.reading = false
    }
    return next(e)
  })
}

export async function band(io, e, k) {
  return null
}

// onSnapshot chimes at most once per snapshot, for a room turning SEV1 or a
// new vote that waits on the person.
export function onSnapshot(io, snap, prev) {
  let why = ''
  for (const r of snap.rooms) {
    const key = r.roomKey
    const st = r.status
    if (st) {
      const sev1 = isSev1(st)
      const was = sound.sev1[key]
      sound.sev1[key] = sev1
      if (sev1 && was === false) why = why || roomName(r) + ' is SEV1'
    }
    if (Array.isArray(r.votes)) {
      const known = sound.votes[key]
      const now = new Set()
      for (const v of r.votes) {
        if (!v || v.mine) continue
        now.add(v.claimSeq)
        if (known && !known.has(v.claimSeq)) why = why || 'a vote waits on you in ' + roomName(r)
      }
      // Keep what was seen: a vote that leaves and comes back is not new.
      sound.votes[key] = new Set([...(known ?? []), ...now])
    }
  }
  if (why && sound.on === true) {
    try {
      void Promise.resolve(io.play(CHIME)).catch(() => {})
    } catch {
      // A chime that cannot play is no reason to stop the room.
    }
  }
  return why
}

export function start(io) {}

function isSev1(st) {
  if (!/^(sev ?1|p1|critical)$/i.test(String(st.severity || '').trim())) return false
  return !/resolved|closed|postmortem|done/i.test(String(st.status || ''))
}

// readSwitch finds the mod's `sound` row in /config.
async function readSwitch($) {
  try {
    const rows = await $.config.list()
    const row = rows.find((r) => /^landfall(@[^.]*)?\.sound$/.test(r.key))
    return row ? row.value === true : false
  } catch {
    return false
  }
}

export function switchText(isOn) {
  if (isOn) {
    return (
      'Sound cues are on: one soft chime when an incident you are in becomes SEV1, or when a new finding waits on your vote. ' +
      'It plays once and never loops. To turn it off, open /config and switch off Sound cues under Landfall. It plays on macOS only.'
    )
  }
  return (
    'Sound cues are off. To turn them on, open /config and switch on Sound cues under Landfall: ' +
    'one soft chime when an incident you are in becomes SEV1, or when a new finding waits on your vote. It plays on macOS only.'
  )
}
