// input.mjs — read the JSON payload a host writes to a hook's stdin.
//
// Claude Code and Codex both hand a lifecycle hook its context this way, and
// for `stop` that payload carries the loop guard (`stop_hook_active`). But a
// hook must also survive being run by hand, by a host that sends nothing, or
// with stdin left open — so this NEVER blocks indefinitely: no stdin, a TTY, or
// silence past the deadline all resolve to '' and the caller treats it as a
// first attempt.
export const STDIN_DEADLINE_MS = 250;

export function readHookInput(stream = process.stdin, { timeoutMs = STDIN_DEADLINE_MS } = {}) {
  if (!stream || stream.isTTY) return Promise.resolve('');
  return new Promise((resolve) => {
    let buffer = '';
    let settled = false;
    const finish = () => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      stream.off('data', onData);
      stream.off('end', finish);
      stream.off('error', finish);
      resolve(buffer);
    };
    const timer = setTimeout(finish, timeoutMs);
    timer.unref?.();
    const onData = (chunk) => {
      buffer += chunk.toString('utf8');
      if (buffer.length > 1_000_000) finish(); // a hook payload is not a stream
    };
    stream.on('data', onData);
    stream.once('end', finish);
    stream.once('error', finish);
    stream.resume?.();
  });
}
