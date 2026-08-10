// connect.test.mjs — feature 081 (landfalls-ai/landfall#1168): the SC-007
// failure-mode matrix + the happy path for `landfall connect aws`, with the
// customer-tooling boundary FAKED (injected run/fetch/prompt) so nothing here
// executes an AWS command or touches the network.
import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { parseConnectFlags, runConnectAws } from '../src/connect/commands.mjs';
import {
  TEMPLATE_PIN,
  memberInstructions,
  terraformSnippet,
  validateAccountId,
  validateRoleArn,
} from '../src/connect/aws.mjs';

const PRINCIPAL = 'arn:aws:iam::692539599137:role/landfall-dev-app-instance';
const ROLE_ARN = 'arn:aws:iam::111122223333:role/landfall-readonly';
const TEMPLATE = 'AWSTemplateFormatVersion: 2010-09-09\n# fake template for tests\n';
const PIN = { ...TEMPLATE_PIN, sha256: createHash('sha256').update(TEMPLATE, 'utf8').digest('hex') };

/** A capturing test harness around runConnectAws's injectable deps. */
function harness(overrides = {}) {
  const out = [];
  const errs = [];
  const runs = [];
  const apiCalls = [];

  const responses = {
    info: { status: 200, body: { available: true, principalArn: PRINCIPAL, accountId: '692539599137', suggestedRegion: 'us-east-1' } },
    connectionsList: { status: 200, body: { connections: [] } },
    create: { status: 201, body: { connectionId: 'aws-111122223333' } },
    health: { status: 200, body: { ok: true, status: 'connected', detail: 'Authenticated to AWS account 111122223333' } },
    ...overrides.responses,
  };

  const fetchImpl = async (url, init = {}) => {
    // The template fetch is the only non-API URL.
    if (String(url).includes('raw.githubusercontent.com')) {
      apiCalls.push({ url: String(url), kind: 'template' });
      return { ok: true, status: 200, text: async () => TEMPLATE };
    }
    apiCalls.push({ url: String(url), method: init.method ?? 'GET', body: init.body });
    const pick = String(url).includes('onboarding-info')
      ? responses.info
      : String(url).endsWith('/connections') && (init.method ?? 'GET') === 'GET'
        ? responses.connectionsList
        : String(url).endsWith('/connections')
          ? responses.create
          : responses.health;
    return { ok: pick.status < 400, status: pick.status, json: async () => pick.body };
  };

  const run = overrides.run ?? (async (cmd, args) => {
    runs.push([cmd, ...args]);
    const joined = args.join(' ');
    if (args[0] === '--version') return { code: 0, stdout: 'aws-cli/2.17.0', stderr: '' };
    if (joined.startsWith('sts get-caller-identity')) {
      return { code: 0, stdout: JSON.stringify({ Account: '111122223333', Arn: 'arn:aws:iam::111122223333:user/dev' }), stderr: '' };
    }
    if (args[0] === 'cloudformation' && args[1] === 'deploy') return { code: 0, stdout: '', stderr: '' };
    if (args[0] === 'cloudformation' && args[1] === 'describe-stacks') return { code: 0, stdout: `${ROLE_ARN}\n`, stderr: '' };
    return { code: 1, stdout: '', stderr: `unexpected: ${joined}` };
  });

  const deps = {
    fetchImpl,
    run,
    log: (...a) => out.push(a.join(' ')),
    error: (...a) => errs.push(a.join(' ')),
    getToken: overrides.getToken ?? (async () => 'tok-123'),
    getSlug: overrides.getSlug ?? (async () => 'acme'),
    baseUrl: 'https://api.test',
    env: {},
    pin: PIN,
    prompt: overrides.prompt,
  };
  return { deps, out, errs, runs, apiCalls };
}

// ── flag parsing ────────────────────────────────────────────────────────────

test('parseConnectFlags: unknown flag, member/management pairing, and bad ids all refuse', () => {
  assert.match(parseConnectFlags(['--wat']).error, /unknown flag/);
  assert.match(parseConnectFlags(['--member', '111122223333']).error, /--member requires --management/);
  assert.match(parseConnectFlags(['--management']).error, /at least one --member/);
  assert.match(parseConnectFlags(['--management', '--member', '123']).error, /12-digit/);
  const ok = parseConnectFlags(['--management', '--member', '111122223333,444455556666', '--name', 'prod']);
  assert.deepEqual(ok.flags.members, ['111122223333', '444455556666']);
  assert.equal(ok.flags.name, 'prod');
});

test('validators: role ARN and account id shapes', () => {
  assert.equal(validateRoleArn(ROLE_ARN), true);
  assert.equal(validateRoleArn('arn:aws:iam::123:role/x'), false);
  assert.equal(validateAccountId('111122223333'), true);
  assert.equal(validateAccountId('ctf{nope}'), false);
});

// ── the SC-007 failure matrix ───────────────────────────────────────────────

test('no session: stops with the exact login instruction before ANY network or AWS call', async () => {
  const h = harness({ getToken: async () => null });
  const code = await runConnectAws({ members: [] }, h.deps);
  assert.equal(code, 1);
  assert.match(h.errs.join('\n'), /landfall login/);
  assert.equal(h.apiCalls.length, 0);
  assert.equal(h.runs.length, 0);
});

test('--org mismatch: guards the session pin, never re-targets', async () => {
  const h = harness();
  const code = await runConnectAws({ members: [], org: 'other-org' }, h.deps);
  assert.equal(code, 1);
  assert.match(h.errs.join('\n'), /signed in to "acme", not "other-org"/);
  assert.equal(h.apiCalls.length, 0);
});

test('404 onboarding-info: an OLD platform degrades to the manual path, not an error dump (FR-015)', async () => {
  const h = harness({ responses: { info: { status: 404, body: {} } } });
  const code = await runConnectAws({ members: [] }, h.deps);
  assert.equal(code, 1);
  assert.match(h.errs.join('\n'), /does not support automated AWS onboarding yet/);
  assert.equal(h.runs.length, 0);
});

test('403: names the admin requirement plainly', async () => {
  const h = harness({ responses: { info: { status: 403, body: {} } } });
  await runConnectAws({ members: [] }, h.deps);
  assert.match(h.errs.join('\n'), /organization administrator/);
});

test('available:false: surfaces the deployment reason and the keys-mode pointer (SC-005)', async () => {
  const h = harness({ responses: { info: { status: 200, body: { available: false, reason: 'no AWS identity here' } } } });
  const code = await runConnectAws({ members: [] }, h.deps);
  assert.equal(code, 1);
  assert.match(h.errs.join('\n'), /no AWS identity here/);
  assert.match(h.errs.join('\n'), /keys-mode/);
  assert.equal(h.runs.length, 0);
});

test('missing aws binary (ENOENT): actionable message, nothing registered', async () => {
  const h = harness({ run: async () => null });
  const code = await runConnectAws({ members: [] }, h.deps);
  assert.equal(code, 1);
  assert.match(h.errs.join('\n'), /"aws" CLI is not installed/);
  // no registration attempt: the only API call was onboarding-info
  assert.equal(h.apiCalls.filter((c) => String(c.url).endsWith('/connections')).length, 0);
});

test('customer credentials do not resolve: their own tool error is surfaced beneath ours (FR-014)', async () => {
  const h = harness({
    run: async (cmd, args) =>
      args[0] === '--version'
        ? { code: 0, stdout: 'aws-cli/2', stderr: '' }
        : { code: 255, stdout: '', stderr: 'Unable to locate credentials' },
  });
  const code = await runConnectAws({ members: [] }, h.deps);
  assert.equal(code, 1);
  assert.match(h.errs.join('\n'), /credentials did not resolve/);
  assert.match(h.errs.join('\n'), /Unable to locate credentials/);
});

test('checksum mismatch: ZERO AWS mutation commands run (SC-004)', async () => {
  const h = harness();
  h.deps.pin = { ...PIN, sha256: 'f'.repeat(64) };
  const code = await runConnectAws({ members: [] }, h.deps);
  assert.equal(code, 1);
  assert.match(h.errs.join('\n'), /does not match the checksum/);
  // preflight ran; cloudformation NEVER did
  assert.equal(h.runs.filter((r) => r.includes('cloudformation')).length, 0);
});

test('deploy failure: what completed, what did not, and how to resume', async () => {
  const h = harness({
    run: async (cmd, args) => {
      if (args[0] === '--version') return { code: 0, stdout: 'aws-cli/2', stderr: '' };
      if (args[0] === 'sts') return { code: 0, stdout: JSON.stringify({ Account: '111122223333', Arn: 'x' }), stderr: '' };
      return { code: 1, stdout: '', stderr: 'AccessDenied: iam:CreateRole' };
    },
  });
  const code = await runConnectAws({ members: [] }, h.deps);
  assert.equal(code, 1);
  const err = h.errs.join('\n');
  assert.match(err, /AccessDenied: iam:CreateRole/); // their tool's words
  assert.match(err, /allowed to create an IAM role/); // our one-line interpretation
  assert.match(err, /re-run/); // the resume
  assert.equal(h.apiCalls.filter((c) => (c.method ?? 'GET') === 'POST').length, 0); // nothing registered
});

test('name collision: refuses, names the existing connection, changes nothing (FR-011)', async () => {
  const h = harness({
    responses: { connectionsList: { status: 200, body: { connections: [{ connectionId: 'aws-111122223333' }] } } },
  });
  const code = await runConnectAws({ members: [] }, h.deps);
  assert.equal(code, 1);
  assert.match(h.errs.join('\n'), /"aws-111122223333" already exists/);
  assert.equal(h.apiCalls.filter((c) => c.method === 'POST' && String(c.url).endsWith('/connections')).length, 0);
});

// ── the happy path ──────────────────────────────────────────────────────────

test('happy path: one command → role created, registered, health-checked, account echoed — and the external id appears in NO output', async () => {
  const h = harness();
  const code = await runConnectAws({ members: [] }, h.deps);
  assert.equal(code, 0);

  // The full customer-side sequence, argv arrays only.
  const kinds = h.runs.map((r) => r.slice(0, 2).join(' '));
  assert.deepEqual(kinds, ['aws --version', 'aws sts', 'aws cloudformation', 'aws cloudformation']);

  // Registration body: role mode, the created ARN, a strong external id.
  const create = h.apiCalls.find((c) => c.method === 'POST' && String(c.url).endsWith('/connections'));
  const body = JSON.parse(create.body);
  assert.equal(body.connectionId, 'aws-111122223333');
  assert.match(body.config.roleArn, /^arn:aws:iam::111122223333:role\//);
  assert.equal(body.config.authMode, 'role');
  assert.ok(body.config.externalId.length >= 22, 'external id carries >=16 bytes of entropy');

  // The deploy carried the SAME external id the registration did.
  const deploy = h.runs.find((r) => r[1] === 'cloudformation' && r[2] === 'deploy');
  assert.ok(deploy.some((a) => a === `ExternalId=${body.config.externalId}`));

  // Health check ran and the account is echoed to the user.
  assert.match(h.out.join('\n'), /Connected ✓/);
  assert.match(h.out.join('\n'), /111122223333/);

  // FR-007: the external id never reaches stdout/stderr.
  assert.ok(!h.out.join('\n').includes(body.config.externalId));
  assert.ok(!h.errs.join('\n').includes(body.config.externalId));
});

test('region order: --region wins over the platform suggestion; both reach the aws calls', async () => {
  const h = harness();
  await runConnectAws({ members: [], region: 'eu-west-1' }, h.deps);
  const deploy = h.runs.find((r) => r[1] === 'cloudformation' && r[2] === 'deploy');
  assert.ok(deploy.includes('--region') && deploy.includes('eu-west-1'));
});

// ── --terraform (FR-012) ────────────────────────────────────────────────────

test('--terraform: prints the pinned snippet, executes NO aws command, re-prompts on a bad ARN', async () => {
  const answers = ['not-an-arn', ROLE_ARN];
  const h = harness({ prompt: async () => answers.shift() });
  const code = await runConnectAws({ members: [], terraform: true }, h.deps);
  assert.equal(code, 0);
  assert.equal(h.runs.length, 0); // no aws CLI at all
  const printed = h.out.join('\n');
  assert.match(printed, new RegExp(`ref=${TEMPLATE_PIN.tag}`));
  assert.ok(printed.includes(PRINCIPAL));
  assert.match(h.errs.join('\n'), /not an IAM role ARN/); // the re-prompt happened
  // registration still completed with the pasted ARN
  const create = h.apiCalls.find((c) => c.method === 'POST' && String(c.url).endsWith('/connections'));
  assert.equal(JSON.parse(create.body).config.roleArn, ROLE_ARN);
});

test('terraformSnippet pins the tagged release and fills both values', () => {
  const s = terraformSnippet({ principalArn: PRINCIPAL, externalId: 'x'.repeat(32) });
  assert.match(s, /ref=v0\.1\.0/);
  assert.ok(s.includes(PRINCIPAL));
  assert.ok(s.includes('x'.repeat(32)));
});

// ── --management (FR-013) ───────────────────────────────────────────────────

test('--management: registration carries declared members + role name; per-member instructions print', async () => {
  const h = harness();
  const code = await runConnectAws(
    { members: ['444455556666'], management: true, memberRoleName: 'landfall-readonly' },
    h.deps,
  );
  assert.equal(code, 0);
  const create = h.apiCalls.find((c) => c.method === 'POST' && String(c.url).endsWith('/connections'));
  const body = JSON.parse(create.body);
  assert.equal(body.config.memberRoleName, 'landfall-readonly');
  assert.deepEqual(body.config.memberAccounts, [{ accountId: '444455556666' }]);
  const printed = h.out.join('\n');
  assert.match(printed, /account 444455556666/);
  assert.match(printed, /iam create-role/);
});

test('memberInstructions: plain trust to the MANAGEMENT role, no external-id condition (matches 079 chaining)', () => {
  const s = memberInstructions({ managementRoleArn: ROLE_ARN, memberRoleName: 'landfall-readonly', members: ['444455556666'] });
  assert.ok(s.includes(ROLE_ARN));
  assert.match(s, /ReadOnlyAccess/);
  assert.ok(!s.includes('ExternalId'), 'the management→member hop presents no external id');
});
