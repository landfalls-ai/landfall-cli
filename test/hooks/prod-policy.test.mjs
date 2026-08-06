// prod-policy.test.mjs — the local prod allow-list (#233, story #192).
//
// Two properties are worth more than the rest of this file put together, and
// both are adversarial:
//
//   1. NO PART OF A COMMAND LINE CAN REACH THE PAYLOAD. Not by extraction, not
//      through a hint, not through a category. Tested by feeding command lines
//      full of secrets and asserting the produced body is byte-identical to the
//      one produced from the rule alone.
//   2. MATCHING IS LITERAL. A rule that reads like a regex is treated as text,
//      so a policy cannot silently widen into a heuristic.
import test from 'node:test';
import assert from 'node:assert/strict';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import {
  loadPolicy,
  validateRule,
  matchCommand,
  ruleMatches,
  programName,
  intentFor,
  entityHintPattern,
  INTENT_CATEGORIES,
} from '../../src/hooks/policy.mjs';

const KUBECTL = {
  id: 'kubectl-prod',
  command: 'kubectl',
  allOf: ['--context=prod'],
  category: 'kubernetes',
  entityHints: ['prod-cluster'],
};

function ruleOf(raw) {
  const { rule, error } = validateRule(raw, 0);
  assert.equal(error, undefined, `expected a valid rule, got: ${error}`);
  return rule;
}

async function withPolicyFile(value, fn) {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'landfall-policy-'));
  const file = path.join(dir, 'prod-policy.json');
  await fs.writeFile(file, typeof value === 'string' ? value : JSON.stringify(value), 'utf8');
  try {
    return await fn(file);
  } finally {
    await fs.rm(dir, { recursive: true, force: true });
  }
}

// ── The property: the command line cannot leak ────────────────────────────

test('the payload is built only from the rule — no command line can change it', () => {
  const rule = ruleOf(KUBECTL);
  const at = new Date('2026-08-04T12:00:00.000Z');
  const baseline = intentFor(rule, at);

  // Every one of these MATCHES the rule, so each one really does produce a
  // report. None of them may alter a byte of it.
  const hostile = [
    'kubectl --context=prod exec -it payments-7f9 -- env',
    'kubectl --context=prod get secret db-root -o jsonpath={.data.password}',
    'kubectl --context=prod -n customer-pii logs pod/ssn-export-42',
    'AWS_SECRET_ACCESS_KEY=wJalrXUt kubectl --context=prod apply -f -',
    'kubectl --context=prod get po # internal-hostname.corp.example',
  ];
  for (const line of hostile) {
    assert.equal(ruleMatches(rule, line), true, `expected a match for: ${line}`);
    assert.deepEqual(intentFor(rule, at), baseline);
  }

  // And the payload is exactly the three contract keys, nothing more.
  assert.deepEqual(Object.keys(baseline).sort(), ['category', 'entityHints', 'startedAt']);
  assert.deepEqual(baseline, {
    category: 'kubernetes',
    entityHints: ['prod-cluster'],
    startedAt: '2026-08-04T12:00:00.000Z',
  });
});

test('intentFor cannot see a command line at all (arity is the guarantee)', () => {
  // A signature that accepts the command line is a signature that can leak it.
  // This is the structural version of the test above: it fails the moment
  // somebody adds a parameter, before any extraction logic can be written.
  assert.equal(intentFor.length, 2);
});

test('every hint a rule can send survives the server-side entity-hint bound', () => {
  const rule = ruleOf({ ...KUBECTL, entityHints: ['prod-cluster', 'api.prod.example.com', 'svc/payments-7f9'] });
  for (const hint of intentFor(rule, new Date(0)).entityHints) {
    assert.match(hint, entityHintPattern);
    assert.doesNotMatch(hint, /\s/);
  }
});

// ── Validation refuses anything the server would reject ───────────────────

test('a hint that could carry a command line is refused at load time', () => {
  const smuggles = [
    'kubectl -n prod exec pod',      // whitespace
    'prod; rm -rf /',                // metacharacter + whitespace
    '$(cat /etc/passwd)',            // substitution
    '`id`',                          // backticks
    '-rf',                           // leading flag
    'a'.repeat(129),                 // over the length bound
    'pods|grep secret',              // pipe
  ];
  for (const hint of smuggles) {
    const { rule, error } = validateRule({ ...KUBECTL, entityHints: [hint] }, 0);
    assert.equal(rule, undefined, `expected ${JSON.stringify(hint)} to be refused`);
    assert.match(error, /bare identifier/);
  }
});

test('a category outside the closed vocabulary is refused', () => {
  const { error } = validateRule({ ...KUBECTL, category: 'exfiltrate' }, 0);
  assert.match(error, /category/);
  // and the vocabulary is exactly the server's
  assert.deepEqual(INTENT_CATEGORIES, [
    'kubernetes', 'cloud', 'database', 'logs', 'deployment', 'network', 'other',
  ]);
});

test('more than ten hints is refused (a dump, not a classification)', () => {
  const { error } = validateRule(
    { ...KUBECTL, entityHints: Array.from({ length: 11 }, (_, i) => `svc-${i}`) },
    0,
  );
  assert.match(error, /at most 10/);
});

test('a rule missing id/command is refused, and names itself in the error', () => {
  assert.match(validateRule({ command: 'kubectl', category: 'logs' }, 3).error, /#4: missing "id"/);
  assert.match(validateRule({ id: 'x', category: 'logs' }, 0).error, /"x": missing "command"/);
});

// ── Matching is literal, and only literal ─────────────────────────────────

test('a rule matches on the program name, not on the string appearing anywhere', () => {
  const rule = ruleOf(KUBECTL);
  assert.equal(ruleMatches(rule, 'kubectl --context=prod get po'), true);
  assert.equal(ruleMatches(rule, '/usr/local/bin/kubectl --context=prod get po'), true);
  // the program is `echo`, not kubectl — a substring match would report this
  assert.equal(ruleMatches(rule, 'echo "kubectl --context=prod get po"'), false);
  assert.equal(ruleMatches(rule, 'kubectx --context=prod'), false);
});

test('allOf needles are literal text, never patterns', () => {
  const rule = ruleOf({ ...KUBECTL, allOf: ['--context=prod.*'] });
  assert.equal(ruleMatches(rule, 'kubectl --context=prod.* get po'), true);
  // as a regex this would match; as literal text it must not
  assert.equal(ruleMatches(rule, 'kubectl --context=production get po'), false);
});

test('every allOf needle must appear; noneOf vetoes the match', () => {
  const rule = ruleOf({ ...KUBECTL, allOf: ['--context=prod', 'delete'], noneOf: ['--dry-run'] });
  assert.equal(ruleMatches(rule, 'kubectl --context=prod delete po/x'), true);
  assert.equal(ruleMatches(rule, 'kubectl --context=prod get po'), false);
  assert.equal(ruleMatches(rule, 'kubectl --context=prod delete po/x --dry-run=client'), false);
});

test('an empty allOf means every invocation of that program, and says so', () => {
  const rule = ruleOf({ id: 'any-psql', command: 'psql', category: 'database' });
  assert.equal(ruleMatches(rule, 'psql -h localhost'), true);
  assert.deepEqual(rule.allOf, []);
  assert.deepEqual(intentFor(rule, new Date(0)).entityHints, []);
});

test('matchCommand returns the first matching rule in file order', () => {
  const rules = [
    ruleOf({ id: 'first', command: 'aws', allOf: ['--profile=prod'], category: 'cloud' }),
    ruleOf({ id: 'second', command: 'aws', allOf: ['--profile=prod'], category: 'logs' }),
  ];
  assert.equal(matchCommand(rules, 'aws --profile=prod s3 ls').id, 'first');
  assert.equal(matchCommand(rules, 'aws --profile=staging s3 ls'), null);
});

test('programName tolerates paths, quotes and leading whitespace', () => {
  assert.equal(programName('  /opt/bin/psql -h db'), 'psql');
  assert.equal(programName('"/opt/my tools/kubectl"'), 'kubectl'); // no split-on-space surprise
  assert.equal(programName(''), '');
  assert.equal(programName(null), '');
});

// ── Loading ───────────────────────────────────────────────────────────────

test('a missing policy file is the shipped state, not an error', async () => {
  const { exists, rules, errors } = await loadPolicy(path.join(os.tmpdir(), 'landfall-absent-policy.json'));
  assert.equal(exists, false);
  assert.deepEqual(rules, []);
  assert.deepEqual(errors, []);
});

test('one bad rule is reported and skipped; the good ones still load', async () => {
  await withPolicyFile(
    { version: 1, rules: [KUBECTL, { ...KUBECTL, id: 'bad', category: 'nope' }] },
    async (file) => {
      const { rules, errors } = await loadPolicy(file);
      assert.deepEqual(rules.map((r) => r.id), ['kubectl-prod']);
      assert.equal(errors.length, 1);
      assert.match(errors[0], /"bad"/);
    },
  );
});

test('malformed JSON reports and reports nothing at all — it never guesses', async () => {
  await withPolicyFile('{ "rules": [', async (file) => {
    const { exists, rules, errors } = await loadPolicy(file);
    assert.equal(exists, true);
    assert.deepEqual(rules, []);
    assert.match(errors[0], /not valid JSON/);
  });
});

test('a policy without a rules array yields no rules', async () => {
  await withPolicyFile({ version: 1 }, async (file) => {
    const { rules, errors } = await loadPolicy(file);
    assert.deepEqual(rules, []);
    assert.match(errors[0], /expected a "rules" array/);
  });
});
