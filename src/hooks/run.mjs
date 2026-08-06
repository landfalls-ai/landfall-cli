// run.mjs — the hook handlers a registered entry actually invokes.
//
// #222 shipped the installer and this contract; #225 filled in `stop`, #227
// `file-changed` + `user-prompt-submit`, and #233 `pre-tool-use`. Each speaks
// on a different channel, because each host event allows a different one:
//
//   stop          exit 2 + stderr — a REFUSAL every host understands. The agent
//                 is concluding, and the point is that it may not yet.
//   file-changed  exit 0, stderr only — a WAKE. The host discards this event's
//                 output entirely, so it stages the digest and nudges the
//                 human, and consumes nothing.
//   user-prompt-submit
//                 exit 0 + a structured JSON object on stdout — the INJECTION.
//                 The one event that can both run on resumption and be heard.
//   pre-tool-use  exit 0, always. In Claude Code a non-zero PreToolUse exit
//                 BLOCKS the tool call, and this hook exists to observe an
//                 investigation, not to gate one — an unreachable API, an
//                 expired session, a malformed policy file and a declined
//                 prompt are all "carry on". It needs no daemon: it reads a
//                 file, compares strings, asks the human, and POSTs. stdout
//                 stays empty because the host parses it; anything meant for
//                 the human goes to stderr.
//
// The shared default for an event with nothing to say is the one that is always
// safe, because the installer registers a command the host runs on every
// matching event from the moment it is written:
//
//   exit 0, print nothing = "nothing pending, carry on"
import { HOOK_EVENTS } from './spec.mjs';
import { runStopHook } from './stop.mjs';
import { protocolForHost, renderNoOp } from './protocol.mjs';
import { runFileChangedHook } from './file-changed.mjs';
import { runUserPromptSubmitHook } from './user-prompt-submit.mjs';
import { loadPolicy, matchCommand, intentFor } from './policy.mjs';
import { confirmOnTty } from './confirm.mjs';
import { resolveTarget, sendIntent, describeOutcome } from './declare.mjs';

export const HOOK_EVENT_IDS = HOOK_EVENTS.map((e) => e.id);

/**
 * Parse the harness's JSON event payload. `{}` if there is none or it doesn't
 * parse — a malformed or absent payload is never an error in the agent turn,
 * only an event with nothing this handler can act on.
 *
 * Takes the already-read `input` string `bin/landfall.mjs` hands every hook
 * (`readHookInput`, #227) rather than reading stdin itself: stdin is read once,
 * with one bounded deadline, and shared by every event — a second independent
 * read here would race it.
 */
function parseEventPayload(input) {
  if (!input) return {};
  try {
    const parsed = typeof input === 'string' ? JSON.parse(input) : input;
    return parsed && typeof parsed === 'object' ? parsed : {};
  } catch {
    return {};
  }
}

/**
 * The shell command a PreToolUse payload is about to run, or null for any other
 * tool. Accepts both the snake_case shape Claude Code and Codex send and the
 * camelCase one Cursor's adapter will (#228) — reading two key spellings costs
 * nothing and being wrong on one host costs the whole feature there.
 *
 * Anything that is not a shell tool returns null, and nothing further happens:
 * no policy read, no terminal, no token, no network.
 */
export function shellCommandOf(payload) {
  const toolName = payload?.tool_name ?? payload?.toolName ?? '';
  if (!/^(bash|shell|terminal|run_?command)$/i.test(String(toolName))) return null;
  const input = payload?.tool_input ?? payload?.toolInput ?? {};
  const command = input?.command ?? input?.cmd;
  return typeof command === 'string' && command.trim() ? command : null;
}

/**
 * `pre-tool-use` (#233): match, ask, declare — in that order, never skipping
 * the middle one.
 *
 * The ordering IS the privacy design, not an implementation detail. Each step
 * is a gate that returns early, so the common case (a command matching nothing)
 * touches only the policy file, and the rare case still cannot reach the
 * network until a human has pressed `y`.
 */
async function runPreToolUse(deps = {}) {
  const {
    input,
    log = () => {},
    readPolicy = loadPolicy,
    confirm = confirmOnTty,
    resolve = resolveTarget,
    send = sendIntent,
    now = () => new Date(),
  } = deps;

  const command = shellCommandOf(parseEventPayload(input));
  if (!command) return { exitCode: 0, result: 'not-a-shell-command' };

  const { exists, rules, errors } = await readPolicy();
  // A policy file that cannot be understood is announced rather than ignored:
  // an engineer who believes they are covered and is not should hear about it
  // the first time a shell command runs, not never.
  for (const error of errors) log(`prod-policy: ${error}`);
  if (!exists || !rules.length) return { exitCode: 0, result: 'no-policy' };

  const rule = matchCommand(rules, command);
  if (!rule) return { exitCode: 0, result: 'no-match' };

  // Resolved before the prompt: an intent that could not be sent anyway is not
  // worth interrupting anyone for.
  const target = await resolve();
  if (!target.ok) {
    log(`matched prod policy "${rule.id}" but ${target.reason}`);
    return { exitCode: 0, result: 'no-target' };
  }

  const intent = intentFor(rule, now());
  const confirmed = await confirm(
    `landfall: this looks like a production investigation (${rule.id}). ` +
      `Report "${intent.category}"${intent.entityHints.length ? ` [${intent.entityHints.join(', ')}]` : ''} ` +
      'and open a war room for it?',
  );
  if (!confirmed) return { exitCode: 0, result: 'declined' };

  const outcome = await send(target, intent);
  log(describeOutcome(outcome));
  return { exitCode: 0, result: outcome?.decision === 'none' ? 'declared-none' : 'declared', outcome };
}

/**
 * Run one hook event. Returns the process exit code the host should see:
 * 0 allows the turn to proceed, 2 blocks it with stderr fed back to the model.
 *
 * `deps.input` is the hook payload the host writes to stdin (all three hosts
 * do) — it carries the loop guard. `deps.host` is the id the registered
 * command was written with (#228); it selects the output protocol and nothing
 * else. Everything else is injectable so the handlers are testable without a
 * serve process.
 */
export async function runHookEvent(eventId, deps = {}) {
  if (!HOOK_EVENT_IDS.includes(eventId)) return { exitCode: 2, error: `unknown hook event: ${eventId}` };
  if (eventId === 'pre-tool-use') return runPreToolUse(deps);
  const protocol = protocolForHost(deps.host);
  if (eventId === 'stop') {
    const { exitCode } = await runStopHook({ ...deps, protocol });
    return { exitCode };
  }
  if (eventId === 'file-changed') {
    const { exitCode } = await runFileChangedHook(deps);
    return { exitCode };
  }
  if (eventId === 'user-prompt-submit') {
    const { exitCode } = await runUserPromptSubmitHook(deps);
    return { exitCode };
  }
  // An event with no behaviour yet still owes its host a well-formed answer:
  // silence under exit2, `{}` under a protocol that parses stdout every time.
  const noop = renderNoOp(protocol);
  return { exitCode: noop.exitCode, stdout: noop.channel === 'stdout' ? noop.text : '' };
}
