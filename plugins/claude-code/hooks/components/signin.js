// Sign-in from the console (spec §2.6): the one state machine behind the Incidents tab while
// there is no sign-in. `idle` is the sign-in state, `waiting` the CLI's browser handoff in
// flight, `failed` how it ended when it did not finish.
//
// Pressing `s` runs the CLI's own handoff, nothing of the mod's: `landfall login --json`. The
// CLI opens the browser itself and polls; the mod reads its lines (`{"event":"url"}` first,
// then `{"ok":true,"org"}` or `{"ok":false,"error"}`) and never opens a browser. A CLI that
// does not speak `--json` yet logs `visit:` then the URL on stderr and exits 0 once signed in,
// which the mod reads as well (§9's fallback). Cancel and the person's close end the spawn
// loop, which kills the CLI; the server's unredeemed grant simply expires.
//
// Nothing here holds `$`: the console hands an `io` whose `spawn(args, onPiece)` runs the
// loop where `$` is, and answers { stop, done }.

import { HOST, clip } from '../core.js'

export const TIMEOUT = 'Your browser did not finish within 5 minutes.'

// The machine. `signedIn` is what `landfall whoami --json` (or a refusal) last said: true,
// false, or null when unknown (an older CLI). `expired`: a credential is on disk but a command
// answered `Sign in to …`. `web` is the instance's web address, for the sentence.
export const signin = {
  phase: 'idle',
  signedIn: null,
  expired: false,
  org: '',
  web: '',
  url: '',
  reason: '',
  startedAt: 0,
  run: null, // the spawn's { stop }: what cancel and close end
  token: 0, // which run is current: a cancelled run's late lines are ignored
}

// noteWhoami keeps what `landfall whoami --json` answered. Unknown (an older CLI, a failed run)
// changes nothing: the next read that refuses decides.
export function noteWhoami(answer) {
  if (!answer || !answer.ok || typeof answer.signedIn !== 'boolean') return
  signin.signedIn = answer.signedIn
  if (answer.org) signin.org = String(answer.org)
  const web = answer.instance && answer.instance.web
  if (web) signin.web = String(web)
}

// signedOut: the console draws the sign-in state rather than the list.
export function signedOut() {
  return signin.signedIn === false
}

// isSignInRefusal: a CLI sentence that means "sign in first" (`Sign in to list incidents: run
// landfall login.`, `Sign in to join an incident from here: …`).
export function isSignInRefusal(error) {
  return /^sign in to\b/i.test(String(error || '').trim())
}

// noteRefusal turns a `Sign in to …` answer into the sign-in state. With a credential on disk
// (whoami said signed in) it says the sign-in expired.
export function noteRefusal(error) {
  if (!isSignInRefusal(error)) return false
  if (signin.signedIn === true) signin.expired = true
  signin.signedIn = false
  if (signin.phase !== 'waiting') signin.phase = 'idle'
  return true
}

// webHost is the host a person reads: "app.landfalls.ai".
export function webHost(web) {
  const m = /^[a-z]+:\/\/([^/]+)/i.exec(String(web || ''))
  return m ? m[1] : String(web || '')
}

// elapsed is the waiting clock: "1m 12s", "8s".
export function elapsed(ms) {
  const s = Math.max(0, Math.floor((Number(ms) || 0) / 1000))
  const m = Math.floor(s / 60)
  return m > 0 ? m + 'm ' + (s % 60) + 's' : s + 's'
}

// reasonWords is why it did not finish, plain: the designed sentence for the CLI's timeout,
// the CLI's own sentence for anything else.
export function reasonWords(error) {
  const e = String(error || '').trim()
  if (!e) return 'The sign-in did not answer.'
  if (/timed out waiting for the browser/i.test(e)) return TIMEOUT
  return e
}

// startSignin runs `landfall login --json`. `onSignedIn(org)` runs once it answers ok (the
// Incidents tab reads the list again). A second press while one runs does nothing.
export async function startSignin(io, onSignedIn) {
  if (signin.phase === 'waiting') return
  const token = ++signin.token
  signin.phase = 'waiting'
  signin.url = ''
  signin.reason = ''
  signin.startedAt = await nowOf(io)
  io.invalidate()
  let final = null
  let out = ''
  let err = ''
  let afterVisit = false
  const take = (piece) => {
    if (token !== signin.token) return
    if (piece.stream === 'stdout') {
      out += piece.text
      let nl = out.indexOf('\n')
      while (nl >= 0) {
        const line = out.slice(0, nl).trim()
        out = out.slice(nl + 1)
        nl = out.indexOf('\n')
        const got = parseLine(line)
        if (!got) continue
        if (got.event === 'url' && got.url) {
          signin.url = String(got.url)
          io.invalidate()
        } else if (typeof got.ok === 'boolean') final = got
      }
    } else if (piece.stream === 'stderr') {
      // The CLI before --json: "…if it does not open, visit:\n<url>".
      err += piece.text
      for (const raw of err.split('\n')) {
        const line = raw.trim()
        if (afterVisit && /^https?:\/\//.test(line) && !signin.url) {
          signin.url = line
          io.invalidate()
        }
        if (/visit:$/.test(line)) afterVisit = true
      }
    }
  }
  let ended = null
  try {
    const run = io.spawn(['login', '--json', '--host', HOST], take)
    signin.run = run
    ended = await run.done
  } catch (e) {
    ended = { code: null, signal: null, error: String(e).slice(0, 200) }
  }
  if (token !== signin.token) return // cancelled: cancel already said so
  signin.run = null
  const last = out.trim() ? parseLine(out.trim()) : null
  if (!final && last && typeof last.ok === 'boolean') final = last
  if (final && final.ok) return signedInNow(io, final, onSignedIn)
  if (!final && ended && ended.code === 0 && !ended.error) return signedInNow(io, {}, onSignedIn)
  signin.phase = 'failed'
  signin.reason = reasonWords((final && final.error) || (ended && ended.error) || lastLine(err))
  io.invalidate()
}

async function signedInNow(io, answer, onSignedIn) {
  signin.phase = 'idle'
  signin.signedIn = true
  signin.expired = false
  if (answer.org) signin.org = String(answer.org)
  if (answer.web) signin.web = String(answer.web)
  io.toast(signin.org ? 'Signed in to ' + signin.org + '.' : 'Signed in to Landfall.', 6000)
  io.invalidate()
  if (onSignedIn) await onSignedIn(signin.org)
}

// cancelSignin ends the spawn loop (which kills the CLI) and goes back to the sign-in state.
// `quiet` leaves the toast out.
export function cancelSignin(io, quiet) {
  if (signin.phase !== 'waiting') return false
  signin.token++
  const run = signin.run
  signin.run = null
  signin.phase = 'idle'
  signin.url = ''
  try {
    if (run) run.stop()
  } catch {
    // The child is gone already.
  }
  if (!quiet) io.toast('Sign-in cancelled.', 6000)
  io.invalidate()
  return true
}

// signinBody draws the state for the console: { rows, keys, footer }. The keys are the tab's
// own; the console adds `close (esc)`.
export function signinBody(k, io, nowMs, onSignedIn) {
  const { Text, Link } = k.els
  const rows = []
  const keys = []
  let footer = null
  if (signin.phase === 'waiting') {
    rows.push(Text({ key: 'si-h', bold: true, children: ['Waiting for your browser…'] }))
    rows.push(Text({ key: 'si-1', children: ['Finish signing in there. If no tab opened, open this link:'] }))
    if (signin.url) rows.push(Link ? Link({ key: 'si-link', href: signin.url, label: 'the Landfall sign-in page' }) : Text({ key: 'si-link', children: ['the Landfall sign-in page  ' + signin.url] }))
    rows.push(Text({ key: 'si-2', dimColor: true, children: ['You have 5 minutes. The console goes back to the incident list when you are in.'] }))
    footer = { text: 'waiting · ' + elapsed(nowMs - signin.startedAt), tone: 'dim' }
    keys.push(k.button({ key: 'si-cancel', label: k.terminal ? 'cancel' : 'Cancel', hotkey: 'x', onPress: () => cancelSignin(io) }))
    return { rows, keys, footer }
  }
  if (signin.phase === 'failed') {
    rows.push(Text({ key: 'si-h', bold: true, children: ['Sign-in did not finish'] }))
    rows.push(Text({ key: 'si-why', children: [clip(signin.reason || TIMEOUT, 400)] }))
    rows.push(Text({ key: 'si-2', dimColor: true, children: ['Nothing changed. Try again, or run landfall login in a terminal.'] }))
    keys.push(k.button({ key: 'si-go', label: k.terminal ? 'try again' : 'Try again', hotkey: 's', primary: true, onPress: () => startSignin(io, onSignedIn) }))
    return { rows, keys, footer }
  }
  if (signin.expired) rows.push(Text({ key: 'si-exp', dimColor: true, children: ['Your sign-in expired.'] }))
  rows.push(Text({ key: 'si-h', bold: true, children: ['Sign in to Landfall'] }))
  const host = webHost(signin.web)
  rows.push(Text({ key: 'si-1', children: ['Your browser opens the Landfall sign-in page' + (host ? ' at ' + host : '') + '. Finish there; this console continues on its own.'] }))
  rows.push(Text({ key: 'si-2', dimColor: true, children: ['Signing in is what lets you join an incident from here. A room you join by share link works without it.'] }))
  keys.push(k.button({ key: 'si-go', label: k.terminal ? 'sign in with your browser' : 'Sign in with your browser', hotkey: 's', primary: true, onPress: () => startSignin(io, onSignedIn) }))
  return { rows, keys, footer }
}

function parseLine(line) {
  if (!line || line[0] !== '{') return null
  try {
    const v = JSON.parse(line)
    return v && typeof v === 'object' ? v : null
  } catch {
    return null
  }
}

function lastLine(text) {
  const lines = String(text || '')
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
  return lines[lines.length - 1] || ''
}

async function nowOf(io) {
  try {
    return Number(await io.now())
  } catch {
    return 0
  }
}
