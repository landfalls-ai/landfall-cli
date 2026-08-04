// pre-tool-use.test.mjs — the hook handler that turns a matched command into a
// declared intent (#233), and the three guarantees it exists to keep:
//
//   nothing leaves without the confirm     — every non-`y` path sends nothing
//   nothing unclassified leaves            — the request body is the rule's
//   non-matching commands cost nothing     — no policy read, no tty, no network
import test from 'node:test';
import assert from 'node:assert/strict';
import { Readable } from 'node:stream';
import { runHookEvent, shellCommandOf } from '../../src/hooks/run.mjs';
import { validateRule } from '../../src/hooks/policy.mjs';

const RULE = validateRule(
  {
    id: 'kubectl-prod',
    command: 'kubectl',
    allOf: ['--context=prod'],
    category: 'kubernetes',
    entityHints: ['prod-cluster'],
  },
  0,
).rule;

const POLICY = { exists: true, rules: [RULE], errors: [] };

function stdinOf(payload) {
  return Readable.from([Buffer.from(typeof payload === 'string' ? payload : JSON.stringify(payload))]);
}

function bashEvent(command) {
  return { tool_name: 'Bash', tool_input: { command } };
}

/**
 * A harness that records every side effect the hook could have, so a test can
 * assert on what did NOT happen as easily as on what did.
 */
function spy({ policy = POLICY, confirmed = false, target, outcome } = {}) {
  const calls = { policyReads: 0, confirms: [], resolves: 0, sends: [], logs: [] };
  return {
    calls,
    deps: {
      log: (line) => calls.logs.push(line),
      readPolicy: async () => {
        calls.policyReads += 1;
        return policy;
      },
      confirm: async (question) => {
        calls.confirms.push(question);
        return confirmed;
      },
      resolve: async () => {
        calls.resolves += 1;
        return target ?? { ok: true, baseUrl: 'https://api.example', slug: 'acme', token: 't' };
      },
      send: async (t, intent) => {
        calls.sends.push({ target: t, intent });
        return outcome ?? { decision: 'none' };
      },
      now: () => new Date('2026-08-04T12:00:00.000Z'),
    },
  };
}

// ── Nothing leaves without the confirm ────────────────────────────────────

test('a matched command with no confirmation sends nothing', async () => {
  const s = spy({ confirmed: false });
  const { exitCode, result } = await runHookEvent('pre-tool-use', {
    ...s.deps,
    stdin: stdinOf(bashEvent('kubectl --context=prod get po')),
  });

  assert.equal(result, 'declined');
  assert.equal(exitCode, 0);
  assert.equal(s.calls.confirms.length, 1);
  assert.deepEqual(s.calls.sends, []); // the whole point
});

test('the prompt names the rule and the classification, never the command', async () => {
  const s = spy({ confirmed: false });
  await runHookEvent('pre-tool-use', {
    ...s.deps,
    stdin: stdinOf(bashEvent('kubectl --context=prod exec -it payments -- cat /run/secrets/db')),
  });

  const [question] = s.calls.confirms;
  assert.match(question, /kubectl-prod/);
  assert.match(question, /kubernetes/);
  assert.match(question, /prod-cluster/);
  assert.doesNotMatch(question, /exec|secrets|payments/);
});

test('on confirmation the intent is sent — and it is exactly the rule', async () => {
  const s = spy({ confirmed: true, outcome: { decision: 'attach', incidentId: 'abc', workspaceUrl: 'https://app/x' } });
  const { result } = await runHookEvent('pre-tool-use', {
    ...s.deps,
    stdin: stdinOf(bashEvent('kubectl --context=prod get secret db-root -o yaml')),
  });

  assert.equal(result, 'declared');
  assert.equal(s.calls.sends.length, 1);
  assert.deepEqual(s.calls.sends[0].intent, {
    category: 'kubernetes',
    entityHints: ['prod-cluster'],
    startedAt: '2026-08-04T12:00:00.000Z',
  });
  // No fragment of the command line appears anywhere in what was sent.
  const wire = JSON.stringify(s.calls.sends[0]);
  for (const fragment of ['secret', 'db-root', 'yaml', 'get ']) {
    assert.equal(wire.includes(fragment), false, `"${fragment}" reached the request`);
  }
  assert.match(s.calls.logs.join('\n'), /joined the matching war room/);
});

// ── A non-matching command costs nothing ──────────────────────────────────

test('a non-shell tool stops before the policy is even read', async () => {
  const s = spy();
  const { result, exitCode } = await runHookEvent('pre-tool-use', {
    ...s.deps,
    stdin: stdinOf({ tool_name: 'Edit', tool_input: { file_path: '/etc/hosts' } }),
  });

  assert.equal(result, 'not-a-shell-command');
  assert.equal(exitCode, 0);
  assert.equal(s.calls.policyReads, 0);
  assert.deepEqual(s.calls.confirms, []);
  assert.equal(s.calls.resolves, 0);
  assert.deepEqual(s.calls.sends, []);
});

test('a shell command matching no rule reaches neither the terminal nor the network', async () => {
  const s = spy();
  const { result } = await runHookEvent('pre-tool-use', {
    ...s.deps,
    stdin: stdinOf(bashEvent('git status')),
  });

  assert.equal(result, 'no-match');
  assert.equal(s.calls.policyReads, 1);
  assert.deepEqual(s.calls.confirms, []);
  assert.equal(s.calls.resolves, 0);
  assert.deepEqual(s.calls.sends, []);
});

test('with no policy file nothing on the machine is reportable', async () => {
  const s = spy({ policy: { exists: false, rules: [], errors: [] } });
  const { result } = await runHookEvent('pre-tool-use', {
    ...s.deps,
    stdin: stdinOf(bashEvent('kubectl --context=prod get po')),
  });

  assert.equal(result, 'no-policy');
  assert.deepEqual(s.calls.confirms, []);
  assert.deepEqual(s.calls.sends, []);
});

test('an unreadable policy is announced, not silently ignored', async () => {
  const s = spy({ policy: { exists: true, rules: [], errors: ['prod-policy.json is not valid JSON'] } });
  const { result } = await runHookEvent('pre-tool-use', {
    ...s.deps,
    stdin: stdinOf(bashEvent('kubectl --context=prod get po')),
  });

  assert.equal(result, 'no-policy');
  assert.match(s.calls.logs.join('\n'), /not valid JSON/);
});

// ── Failure modes never disturb the engineer's command ────────────────────

test('an unsendable intent is not worth a prompt — and still exits 0', async () => {
  const s = spy({ confirmed: true, target: { ok: false, reason: 'not signed in — run `landfall login`' } });
  const { result, exitCode } = await runHookEvent('pre-tool-use', {
    ...s.deps,
    stdin: stdinOf(bashEvent('kubectl --context=prod get po')),
  });

  assert.equal(result, 'no-target');
  assert.equal(exitCode, 0);
  assert.deepEqual(s.calls.confirms, []); // asked nobody: it could not have been sent
  assert.deepEqual(s.calls.sends, []);
  assert.match(s.calls.logs.join('\n'), /not signed in/);
});

test('a server that opened nothing is reported plainly, exit 0', async () => {
  const s = spy({ confirmed: true, outcome: { decision: 'none', reason: 'declared intent not accepted (HTTP 404)' } });
  const { result, exitCode } = await runHookEvent('pre-tool-use', {
    ...s.deps,
    stdin: stdinOf(bashEvent('kubectl --context=prod get po')),
  });

  assert.equal(result, 'declared-none');
  assert.equal(exitCode, 0);
  assert.match(s.calls.logs.join('\n'), /no war room opened \(declared intent not accepted \(HTTP 404\)\)/);
});

test('garbage on stdin is inert, never an error in the agent turn', async () => {
  for (const payload of ['', 'not json', '[]', '{"tool_name":"Bash"}', '{"tool_name":"Bash","tool_input":{"command":"   "}}']) {
    const s = spy();
    const { exitCode, result } = await runHookEvent('pre-tool-use', { ...s.deps, stdin: stdinOf(payload) });
    assert.equal(exitCode, 0, `payload ${JSON.stringify(payload)}`);
    assert.equal(result, 'not-a-shell-command');
    assert.equal(s.calls.policyReads, 0);
  }
});

// ── The payload reader ────────────────────────────────────────────────────

test('shellCommandOf accepts both key spellings and only shell tools', () => {
  assert.equal(shellCommandOf({ tool_name: 'Bash', tool_input: { command: 'ls' } }), 'ls');
  assert.equal(shellCommandOf({ toolName: 'shell', toolInput: { command: 'ls' } }), 'ls');
  assert.equal(shellCommandOf({ tool_name: 'run_command', tool_input: { cmd: 'ls' } }), 'ls');
  assert.equal(shellCommandOf({ tool_name: 'Read', tool_input: { command: 'ls' } }), null);
  assert.equal(shellCommandOf({}), null);
  assert.equal(shellCommandOf(null), null);
});

test('an unknown hook event is still a usage error', async () => {
  const { exitCode, error } = await runHookEvent('not-an-event');
  assert.equal(exitCode, 2);
  assert.match(error, /unknown hook event/);
});
