// remediation.test.mjs — feature 20260812-010632 (T030): `landfall remediation
// approve`. Same injectable-deps harness convention as connect.test.mjs — the
// network is faked, nothing here makes a real HTTP call.
import test from 'node:test';
import assert from 'node:assert/strict';
import { parseRemediationApproveFlags, runRemediationApprove } from '../src/remediation/commands.mjs';

function harness(overrides = {}) {
  const out = [];
  const errs = [];
  const calls = [];
  const response = overrides.response ?? { status: 202, ok: true, body: {} };

  const fetchImpl = async (url, init = {}) => {
    calls.push({ url: String(url), method: init.method, body: init.body ? JSON.parse(init.body) : undefined, headers: init.headers });
    return { ok: response.ok ?? response.status < 300, status: response.status, json: async () => response.body };
  };

  const deps = {
    fetchImpl,
    log: (...a) => out.push(a.join(' ')),
    error: (...a) => errs.push(a.join(' ')),
    getToken: overrides.getToken ?? (async () => 'tok-123'),
    getSlug: overrides.getSlug ?? (async () => 'acme'),
    baseUrl: 'https://api.test',
    env: {},
  };
  return { deps, out, errs, calls };
}

// -------------------------------------------------------------- flag parsing

test('parseRemediationApproveFlags requires a remediation id', () => {
  const { error } = parseRemediationApproveFlags(['--incident', 'inc-1']);
  assert.match(error, /usage: landfall remediation approve/);
});

test('parseRemediationApproveFlags requires --incident — a remediation id alone does not name its incident', () => {
  const { error } = parseRemediationApproveFlags(['rem-123']);
  assert.match(error, /requires --incident/);
});

test('parseRemediationApproveFlags: the happy path, with --override and --org', () => {
  const { flags } = parseRemediationApproveFlags(['rem-123', '--incident', 'inc-1', '--org', 'acme', '--override', 'solo on-call']);
  assert.deepEqual(flags, { incident: 'inc-1', org: 'acme', version: null, override: { reason: 'solo on-call' }, remediationId: 'rem-123' });
});

// -------------------------------------------------------------- session gating

test('refuses with a clear message when not signed in — never touches the network', async () => {
  const { deps, errs, calls } = harness({ getToken: async () => null });
  const code = await runRemediationApprove({ remediationId: 'rem-1', incident: 'inc-1' }, deps);
  assert.equal(code, 1);
  assert.match(errs[0], /landfall login/);
  assert.equal(calls.length, 0);
});

test('refuses with a clear message when the session has no org pin', async () => {
  const { deps, errs, calls } = harness({ getSlug: async () => null });
  const code = await runRemediationApprove({ remediationId: 'rem-1', incident: 'inc-1' }, deps);
  assert.equal(code, 1);
  assert.match(errs[0], /no organization pin/);
  assert.equal(calls.length, 0);
});

test('refuses when --org names a different organization than the session is pinned to', async () => {
  const { deps, errs, calls } = harness();
  const code = await runRemediationApprove({ remediationId: 'rem-1', incident: 'inc-1', org: 'other-co' }, deps);
  assert.equal(code, 1);
  assert.match(errs[0], /signed in to "acme", not "other-co"/);
  assert.equal(calls.length, 0);
});

// -------------------------------------------------------------- the real request shape

test('hits the REAL endpoint path — /remediation/proposals/:id/approve, not /remediations/:id/approve', async () => {
  const { deps, calls } = harness();
  await runRemediationApprove({ remediationId: 'rem-mit-1', incident: 'inc-1' }, deps);
  assert.equal(calls[0].url, 'https://api.test/o/acme/incidents/inc-1/remediation/proposals/rem-mit-1/approve');
  assert.equal(calls[0].method, 'POST');
  assert.match(calls[0].headers.authorization, /^Bearer tok-123$/);
});

test('omits version from the body when not passed — the service treats an absent version as "skip the check"', async () => {
  const { deps, calls } = harness();
  await runRemediationApprove({ remediationId: 'rem-1', incident: 'inc-1' }, deps);
  assert.equal('version' in calls[0].body, false);
});

test('includes version when explicitly passed', async () => {
  const { deps, calls } = harness();
  await runRemediationApprove({ remediationId: 'rem-1', incident: 'inc-1', version: 'v7' }, deps);
  assert.equal(calls[0].body.version, 'v7');
});

test('a plain approve (no override) succeeds', async () => {
  const { deps, out } = harness();
  const code = await runRemediationApprove({ remediationId: 'rem-1', incident: 'inc-1' }, deps);
  assert.equal(code, 0);
  assert.match(out[0], /^✓ Approved\.$/);
});

test('an override approve sends {override:{reason}} and reports it distinctly', async () => {
  const { deps, calls, out } = harness();
  const code = await runRemediationApprove(
    { remediationId: 'rem-1', incident: 'inc-1', override: { reason: 'solo on-call, bar unreachable' } },
    deps,
  );
  assert.equal(code, 0);
  assert.deepEqual(calls[0].body.override, { reason: 'solo on-call, bar unreachable' });
  assert.match(out[0], /override recorded: reason="solo on-call, bar unreachable"/);
});

// -------------------------------------------------------------- server refusals, named

test('403 override_requires_human_session gets a clear, specific message', async () => {
  const { deps, errs } = harness({ response: { status: 403, ok: false, body: { error: 'override_requires_human_session' } } });
  const code = await runRemediationApprove({ remediationId: 'rem-1', incident: 'inc-1', override: { reason: 'x' } }, deps);
  assert.equal(code, 1);
  assert.match(errs[0], /never an API key/);
});

test('400 remediation_not_admitted names the shortfall and, without --override, suggests it', async () => {
  const { deps, errs } = harness({
    response: { status: 400, ok: false, body: { error: 'remediation_not_admitted', shortfall: '1/2 corroborators' } },
  });
  const code = await runRemediationApprove({ remediationId: 'rem-1', incident: 'inc-1' }, deps);
  assert.equal(code, 1);
  assert.match(errs[0], /1\/2 corroborators/);
  assert.match(errs[0], /--override/);
});

test('400 remediation_not_admitted WITH --override supplied reports the override was not accepted, not "try --override"', async () => {
  const { deps, errs } = harness({
    response: { status: 400, ok: false, body: { error: 'remediation_not_admitted', shortfall: 'stale' } },
  });
  const code = await runRemediationApprove({ remediationId: 'rem-1', incident: 'inc-1', override: { reason: 'x' } }, deps);
  assert.equal(code, 1);
  assert.match(errs[0], /was not accepted/);
});

test('400 override_reason_required is a distinct, actionable message', async () => {
  const { deps, errs } = harness({ response: { status: 400, ok: false, body: { error: 'override_reason_required' } } });
  const code = await runRemediationApprove({ remediationId: 'rem-1', incident: 'inc-1', override: { reason: '' } }, deps);
  assert.equal(code, 1);
  assert.match(errs[0], /--override "why you are overriding this"/);
});

test('an unrecognized failure still reports something actionable, not a silent 1', async () => {
  const { deps, errs } = harness({ response: { status: 500, ok: false, body: { message: 'boom' } } });
  const code = await runRemediationApprove({ remediationId: 'rem-1', incident: 'inc-1' }, deps);
  assert.equal(code, 1);
  assert.match(errs[0], /HTTP 500/);
  assert.match(errs[0], /boom/);
});
