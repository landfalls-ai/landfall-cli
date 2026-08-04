// run.mjs — the hook handlers a registered entry actually invokes.
//
// SCOPE: `stop` (#225) and `file-changed` (#227) are still contracts rather
// than behaviour — both wait on what the local bridge daemon turns out to be,
// which is an open question on #225. Their default is the one that is always
// safe:
//
//   exit 0, print nothing = "nothing pending, carry on"
//
// which is precisely what both events should do when there is no room context
// waiting. `pre-tool-use` (#233) is implemented here in full, because it needs
// no daemon at all: it reads a file, compares strings, asks the human, and
// POSTs. Its only state is the engineer's own policy file.
//
// ── The contract `pre-tool-use` keeps ─────────────────────────────────────
// Exit 0, always. In Claude Code a non-zero PreToolUse exit BLOCKS the tool
// call, and this hook exists to observe an investigation, not to gate one. An
// unreachable API, an expired session, a malformed policy file and a declined
// prompt are all "carry on" — the command the engineer is running is none of
// this hook's business. stdout stays empty because the host parses it; anything
// meant for the human goes to stderr.
import { HOOK_EVENTS } from './spec.mjs';
import { loadPolicy, matchCommand, intentFor } from './policy.mjs';
import { confirmOnTty } from './confirm.mjs';
import { resolveTarget, sendIntent, describeOutcome } from './declare.mjs';

export const HOOK_EVENT_IDS = HOOK_EVENTS.map((e) => e.id);

/** Read the harness's JSON event payload from stdin. `{}` if there is none. */
async function readEventPayload(stream) {
  if (!stream || stream.isTTY) return {};
  const chunks = [];
  try {
    for await (const chunk of stream) chunks.push(Buffer.from(chunk));
  } catch {
    return {};
  }
  const text = Buffer.concat(chunks).toString('utf8').trim();
  if (!text) return {};
  try {
    return JSON.parse(text);
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
    stdin = process.stdin,
    log = () => {},
    readPolicy = loadPolicy,
    confirm = confirmOnTty,
    resolve = resolveTarget,
    send = sendIntent,
    now = () => new Date(),
  } = deps;

  const command = shellCommandOf(await readEventPayload(stdin));
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
 * 0 allows the turn to proceed. #225 introduces the non-zero path.
 */
export async function runHookEvent(eventId, deps = {}) {
  if (!HOOK_EVENT_IDS.includes(eventId)) return { exitCode: 2, error: `unknown hook event: ${eventId}` };
  if (eventId === 'pre-tool-use') return runPreToolUse(deps);
  return { exitCode: 0 };
}
