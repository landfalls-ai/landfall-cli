// confirm.mjs — the one-keystroke confirmation a matched command must get
// before anything leaves the machine (#233).
//
// ── Why /dev/tty and not stdin ────────────────────────────────────────────
// A hook process is spawned by the agent harness with the event payload on
// stdin, so stdin is already spoken for and is not a terminal. `/dev/tty` is
// the controlling terminal of the process group — the engineer's actual
// keyboard — which is the only channel that can carry a prompt the human sees
// while their agent is mid-turn.
//
// ── Every failure mode declines ───────────────────────────────────────────
// No controlling terminal (CI, a headless daemon, a harness that detaches the
// process group), a read error, or silence past the timeout all resolve to
// `false`. The acceptance criterion is "nothing leaves without the confirm", so
// the absence of a human is not a soft case to be lenient about — it is the
// clearest possible "no". The default answer to the prompt itself is likewise
// no: `y` confirms, and every other key, including Enter, declines.
//
// The timeout exists because this prompt sits in front of a command the
// engineer is trying to run. A hook that waits forever for a keystroke nobody
// is there to press would hang their shell, which would make the first
// experience of this feature a reason to uninstall it.
import { promises as fs } from 'node:fs';
import tty from 'node:tty';

/** How long to wait for a keystroke before declining. */
export const CONFIRM_TIMEOUT_MS = 30_000;

/**
 * Ask on the controlling terminal and resolve true only on an explicit `y`.
 *
 * @param {string} question rendered as-is; the caller owns the wording
 * @param {{timeoutMs?: number, ttyPath?: string}} [opts]
 */
export async function confirmOnTty(question, opts = {}) {
  const { timeoutMs = CONFIRM_TIMEOUT_MS, ttyPath = '/dev/tty' } = opts;

  let handle;
  try {
    handle = await fs.open(ttyPath, 'r+');
  } catch {
    return false; // no controlling terminal — nobody to ask, so: no
  }

  let input;
  let output;
  let restoreRaw = false;
  try {
    output = new tty.WriteStream(handle.fd);
    input = new tty.ReadStream(handle.fd);
  } catch {
    await handle.close().catch(() => {});
    return false; // the fd is not a terminal (redirected /dev/tty in a test/CI)
  }

  const cleanup = async () => {
    try {
      if (restoreRaw && input.isRaw) input.setRawMode(false);
    } catch { /* already gone */ }
    input.pause();
    try { input.destroy(); } catch { /* already gone */ }
    await handle.close().catch(() => {});
  };

  try {
    output.write(`${question} [y/N] `);
  } catch {
    await cleanup();
    return false;
  }

  const answer = await new Promise((resolve) => {
    let settled = false;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve(value);
    };
    const timer = setTimeout(() => finish(false), timeoutMs);
    // Do not hold the process open purely to wait for a key: if everything else
    // has finished, this hook has nothing left to confirm.
    if (typeof timer.unref === 'function') timer.unref();

    try {
      if (input.isTTY && typeof input.setRawMode === 'function') {
        input.setRawMode(true);
        restoreRaw = true;
      }
    } catch { /* not raw-capable; a line-buffered answer still works */ }

    input.on('error', () => finish(false));
    input.on('close', () => finish(false));
    input.on('data', (chunk) => {
      const key = chunk.toString('utf8').trim().slice(0, 1).toLowerCase();
      finish(key === 'y');
    });
    input.resume();
  });

  try {
    output.write(`${answer ? 'yes' : 'no'}\n`);
  } catch { /* terminal went away between the prompt and the answer */ }
  await cleanup();
  return answer;
}
