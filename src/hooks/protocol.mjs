// protocol.mjs — how a hook says "block" to the host that invoked it (#228,
// story #190).
//
// The DECISION a lifecycle hook makes is host-independent: `buildStopDecision`
// looks at what the room owes this session and returns block/allow plus the
// text. How that decision is DELIVERED is not — and the two hosts we shipped
// first happen to share a convention that Cursor does not:
//
//   exit2 (Claude Code, Codex)
//     exit 2 with the message on stderr; the host feeds stderr back to the
//     model. stdout is the host's own structured channel and must stay empty.
//
//   cursor-json (Cursor)
//     exit 0 ALWAYS, with exactly one JSON object on stdout and nothing else.
//     A non-zero exit is "the hook failed", not "the hook objected", and
//     Cursor JSON.parse()s stdout — plain text or an empty stdout is a parse
//     error, not a silent allow. So the allow path is `{}`, printed; it is not
//     the absence of output.
//
// Cursor's `stop` is a NOTIFICATION hook: it cannot refuse a conclusion the
// way Claude Code's can. What it can do is `{"followup_message": "..."}`,
// which Cursor submits as the next user message and which continues the agent
// loop — the same effect our Stop refusal is after (the agent does not walk
// away from an incident holding stale context), reached by a different verb.
// `{continue:false}` — the shape issue #228 names — is the vocabulary of
// Cursor's PERMISSION hooks (`beforeShellExecution`, `beforeMCPExecution`)
// and of `beforeSubmitPrompt`, not of `stop`. Using it here would register a
// hook whose output Cursor ignores.
//
// TERMINATION under cursor-json rests on three independent things, because
// Cursor sends no `stop_hook_active`:
//   1. consume-after-block — whatever was reported is dequeued, so a second
//      followup needs genuinely new events (see stop.mjs);
//   2. `loop_count`, when Cursor sends it, read as the equivalent guard;
//   3. Cursor's own cap of 5 auto-followups per turn.
// (1) and (3) hold even if (2) is absent from a given Cursor version.

/** Exit-2-with-stderr: Claude Code, Codex, and the default for anything unknown. */
export const EXIT2 = 'exit2';

/** One JSON object on stdout, exit 0: Cursor. */
export const CURSOR_JSON = 'cursor-json';

const HOST_PROTOCOL = {
  'claude-code': EXIT2,
  codex: EXIT2,
  cursor: CURSOR_JSON,
};

/**
 * The output protocol one host id speaks.
 *
 * An unknown id resolves to {@link EXIT2} rather than throwing: `--host` is
 * read off a command line a host runs on every turn, and a hook that dies on
 * an argument it does not recognize is worse than one that falls back to the
 * convention two of the three hosts share.
 */
export function protocolForHost(hostId) {
  return HOST_PROTOCOL[hostId] ?? EXIT2;
}

/** True when this host wants output even to say "nothing to report". */
export function speaksOnSilence(protocol) {
  return protocol === CURSOR_JSON;
}

/**
 * Render a stop decision into what the invoking host understands.
 *
 * Returns `{exitCode, text, channel}` — `text` empty means "say nothing at
 * all", which is the exit2 allow path and the only one where silence is a
 * valid answer.
 *
 * @param {string} protocol {@link EXIT2} or {@link CURSOR_JSON}
 * @param {{block: boolean, reason: string}} decision
 */
export function renderStopVerdict(protocol, { block = false, reason = '' } = {}) {
  if (protocol === CURSOR_JSON) {
    // `{}` = "let it stop"; a followup_message = "here is what you missed,
    // keep going". Both are a successful run of the hook, hence exit 0.
    const body = block && reason ? { followup_message: reason } : {};
    return { exitCode: 0, text: `${JSON.stringify(body)}\n`, channel: 'stdout' };
  }
  return block && reason
    ? { exitCode: 2, text: `${reason}\n`, channel: 'stderr' }
    : { exitCode: 0, text: '', channel: 'stderr' };
}

/**
 * What a hook with no behaviour yet should print. Silence for exit2 (the
 * documented "nothing pending, carry on"), `{}` for a host that parses stdout
 * unconditionally.
 */
export function renderNoOp(protocol) {
  return renderStopVerdict(protocol, { block: false, reason: '' });
}
