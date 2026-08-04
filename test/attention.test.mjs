// attention.test.mjs — vote requests on the piggyback channel and the Stop
// hook's second reason to refuse (landfalls-ai/landfall#252, story #203).
//
// The adversarial half is the point of this file. Both channels interrupt an
// agent mid-task, so the tests that matter are the ones asserting when they
// DON'T: a quiet room costs nothing, a claim is announced once, a flag that has
// not been quarantined never blocks, and an ordinary vote request never stops a
// conclusion.
import test from 'node:test';
import assert from 'node:assert/strict';

import { EdgeBridgeClient } from '../src/client.mjs';
import { buildBridgeTools, createBridgeSession } from '../src/tools.mjs';
import {
  describeStopBlockers,
  formatDuration,
  hasStopBlockers,
  stopBlockers,
  touchesAttention,
  voteKey,
  voteRequestBlock,
} from '../src/attention.mjs';
import { buildStopDecision } from '../src/hooks/stop.mjs';
import { handleSocketRequest } from '../src/hooks/socket.mjs';

const CFG = { baseUrl: 'http://api.test', slug: 'acme', incidentId: 'inc-1', token: 'tok', agentLabel: 'Claude Code' };

function awaited(over = {}) {
  return {
    claimSeq: 48,
    class: 'causal',
    statement: 'the origin rollback at 14:02 caused the 5xx spike',
    authoredBy: 'Priya',
    authorIsAgent: false,
    positionsSoFar: 1,
    stale: false,
    ...over,
  };
}

function flagged(over = {}) {
  return {
    targetSeq: 21,
    targetKind: 'finding',
    state: 'quarantined',
    relation: 'cited',
    citedByClaimSeqs: [52],
    reason: 'that dashboard is the staging cluster',
    concur: 2,
    dissent: 0,
    ...over,
  };
}

function attention(over = {}) {
  return { votesAwaited: [], flaggedOwnContext: [], expiringClaims: [], ...over };
}

/** A fetch fake that records calls and answers the attention route. */
function fakeFetch({ attention: att = attention(), routes = {} } = {}) {
  const calls = [];
  const fn = async (url, opts = {}) => {
    const u = new URL(url);
    calls.push({ path: u.pathname, search: u.search, method: opts.method ?? 'GET' });
    if (u.pathname.endsWith('/vetting/attention')) return { ok: true, status: 200, json: async () => att };
    const r = routes[u.pathname] ?? { status: 202 };
    return { ok: true, status: r.status ?? 200, json: async () => r.json ?? {} };
  };
  fn.calls = calls;
  return fn;
}

async function joinedSession(fetchImpl) {
  const client = new EdgeBridgeClient(CFG, fetchImpl);
  client.agentInstanceId = 'a-9';
  return createBridgeSession({ client });
}

// ----------------------------------------------------------- the client read

test('getAttention asks for the AGENT’s queue, not its human’s', async () => {
  const f = fakeFetch();
  const c = new EdgeBridgeClient(CFG, f);
  c.agentInstanceId = 'a-9';
  await c.getAttention();
  assert.equal(f.calls[0].path, '/o/acme/incidents/inc-1/vetting/attention');
  assert.equal(f.calls[0].search, '?agentInstanceId=a-9');
});

test('omits the parameter entirely before the server has issued an instance', async () => {
  // Sending an empty one would be a claim about identity the server must then
  // refuse; sending none is the honest "ask as the human" it already supports.
  const f = fakeFetch();
  await new EdgeBridgeClient(CFG, f).getAttention();
  assert.equal(f.calls[0].search, '');
});

// ------------------------------------------------------- the piggyback block

test('renders a vote request with what it is, who asked, and what it needs', () => {
  const { text } = voteRequestBlock(
    attention({ votesAwaited: [awaited({ shortfall: { text: 'Needs 1 more corroboration.' } })] }),
  );
  assert.match(text, /⚠ vote requested: claim #48/);
  assert.match(text, /origin rollback at 14:02/);
  assert.match(text, /from Priya/);
  assert.match(text, /Needs 1 more corroboration\./);
  assert.match(text, /corroborate_claim or contest_claim/);
});

test('says nothing at all for a quiet room', () => {
  assert.equal(voteRequestBlock(attention()).text, '');
  assert.equal(voteRequestBlock(null).text, '');
  assert.equal(voteRequestBlock(undefined).text, '');
});

test('marks an agent author as an agent, and shows the remaining window', () => {
  const { text } = voteRequestBlock(
    attention({ votesAwaited: [awaited({ authoredBy: 'Codex', authorIsAgent: true, expiresInMs: 8_100_000 })] }),
  );
  assert.match(text, /from Codex \(agent\)/);
  assert.match(text, /2h 15m left/);
});

test('a lapsed claim says so instead of counting down', () => {
  const { text } = voteRequestBlock(attention({ votesAwaited: [awaited({ stale: true, expiresInMs: -1000 })] }));
  assert.match(text, /PAST its freshness window/);
  assert.doesNotMatch(text, /left/);
});

test('caps the spelled-out lines and counts the rest', () => {
  const many = Array.from({ length: 7 }, (_, i) => awaited({ claimSeq: i }));
  const { text, keys } = voteRequestBlock(attention({ votesAwaited: many }));
  assert.equal(keys.length, 3);
  assert.match(text, /\+4 more claim\(s\) awaiting your position/);
});

test('announces a claim once — and again only if it later goes stale', () => {
  const notified = new Set();
  const first = voteRequestBlock(attention({ votesAwaited: [awaited()] }), { notified });
  assert.notEqual(first.text, '');
  for (const k of first.keys) notified.add(k);

  // Same claim, same state: silence. Training an agent to skim past the marker
  // is the one way this channel can fail permanently.
  assert.equal(voteRequestBlock(attention({ votesAwaited: [awaited()] }), { notified }).text, '');

  // Now it is about to lapse — genuinely new, and the last moment to act.
  const stale = voteRequestBlock(attention({ votesAwaited: [awaited({ stale: true })] }), { notified });
  assert.match(stale.text, /claim #48/);
  assert.notEqual(voteKey(awaited()), voteKey(awaited({ stale: true })));
});

test('never mutates the caller’s notified set', () => {
  const notified = new Set();
  voteRequestBlock(attention({ votesAwaited: [awaited()] }), { notified });
  assert.equal(notified.size, 0); // the caller marks them, once it has the block
});

// ------------------------------------------------- the block on a tool result

test('a vote request is PREPENDED to the tool result it rides on', async () => {
  const f = fakeFetch({
    attention: attention({ votesAwaited: [awaited()] }),
    routes: { '/o/acme/incidents/inc-1/events': { status: 200, json: [] } },
  });
  const session = await joinedSession(f);
  const tools = buildBridgeTools(session);
  const out = await tools.find((t) => t.name === 'get_brief').handler({});

  assert.match(out.split('\n')[0], /⚠ vote requested: claim #48/);
  assert.match(out, /Incident timeline/);
  assert.ok(out.indexOf('vote requested') < out.indexOf('Incident timeline'));
});

test('a quiet room costs exactly ONE attention read for the whole session', async () => {
  const f = fakeFetch({ routes: { '/o/acme/incidents/inc-1/events': { status: 200, json: [] } } });
  const session = await joinedSession(f);
  const tools = buildBridgeTools(session);
  const brief = tools.find((t) => t.name === 'get_brief');

  await brief.handler({});
  await brief.handler({});
  await brief.handler({});

  const reads = f.calls.filter((c) => c.path.endsWith('/vetting/attention'));
  assert.equal(reads.length, 1); // first call primes it; nothing since said otherwise
});

test('a claim event arriving on the socket is what triggers the next read', async () => {
  const f = fakeFetch({ routes: { '/o/acme/incidents/inc-1/events': { status: 200, json: [] } } });
  const session = await joinedSession(f);
  const tools = buildBridgeTools(session);
  const brief = tools.find((t) => t.name === 'get_brief');

  await brief.handler({});
  session.enqueueEvent({ seq: 10, type: 'edge.finding', payload: { text: 'unrelated' } });
  await brief.handler({});
  assert.equal(f.calls.filter((c) => c.path.endsWith('/vetting/attention')).length, 1);

  session.enqueueEvent({ seq: 11, type: 'claim.staged', payload: { statement: 'x' } });
  await brief.handler({});
  assert.equal(f.calls.filter((c) => c.path.endsWith('/vetting/attention')).length, 2);
});

test('an attention read that fails never breaks the tool call', async () => {
  const f = async (url, opts = {}) => {
    if (String(url).endsWith('/vetting/attention')) throw new Error('network down');
    return { ok: true, status: 200, json: async () => (String(url).includes('/events') ? [] : {}) };
  };
  const session = await joinedSession(f);
  const tools = buildBridgeTools(session);
  const out = await tools.find((t) => t.name === 'get_brief').handler({});
  assert.match(out, /Incident timeline/);
  assert.doesNotMatch(out, /vote requested/);
});

test('joining a different war room forgets the previous room’s questions', async () => {
  // A different room asks different questions of a different participant, so
  // carrying either the answers or the "already told them" set across would
  // both misinform and silence.
  const session = createBridgeSession({
    redeemImpl: async () => ({ ...CFG, incidentId: 'inc-2' }),
    clientFactory: () => ({ join: async () => {}, leave: async () => {}, agentInstanceId: 'a-2' }),
  });
  session.attention = attention({ votesAwaited: [awaited()] });
  session.attentionNotified.add(voteKey(awaited()));
  session.attentionDirty = false;

  await session.joinWarRoom('http://api.test/share/xyz');
  assert.equal(session.attention, null);
  assert.equal(session.attentionNotified.size, 0);
  assert.equal(session.attentionDirty, true);
});

// --------------------------------------------------------------- the blockers

test('quarantined context this agent relied on is a blocker', () => {
  const b = stopBlockers(attention({ flaggedOwnContext: [flagged()] }));
  assert.equal(b.quarantined.length, 1);
  assert.equal(hasStopBlockers(b), true);
});

test('a merely FLAGGED item is NOT a blocker', () => {
  // A flag is an open question. Refusing every conclusion while one is open
  // would let any single participant freeze an investigation.
  const b = stopBlockers(attention({ flaggedOwnContext: [flagged({ state: 'flagged' })] }));
  assert.equal(hasStopBlockers(b), false);
});

test('an ordinary vote request is NOT a blocker', () => {
  // Most claims do not need this participant; a hook that stopped every
  // conclusion until the queue was empty would be uninstalled within a day.
  assert.equal(hasStopBlockers(stopBlockers(attention({ votesAwaited: [awaited()] }))), false);
});

test('an unanswered CONTRADICTION is a blocker', () => {
  const b = stopBlockers(
    attention({
      votesAwaited: [awaited({ shortfall: { text: 'contradicts admitted claims', missing: { contradiction: [31] } } })],
    }),
  );
  assert.equal(b.contradictions.length, 1);
  assert.equal(hasStopBlockers(b), true);
});

test('a shortfall with an EMPTY contradiction list is not a contradiction', () => {
  const b = stopBlockers(
    attention({ votesAwaited: [awaited({ shortfall: { text: 'needs one more', missing: { contradiction: [] } } })] }),
  );
  assert.equal(hasStopBlockers(b), false);
});

test('the refusal names the item, the reason and the way out', () => {
  const lines = describeStopBlockers({
    quarantined: [flagged()],
    contradictions: [awaited({ claimSeq: 52, shortfall: { missing: { contradiction: [31, 33] } } })],
  });
  assert.match(lines[0], /seq 21/);
  assert.match(lines[0], /QUARANTINED/);
  assert.match(lines[0], /cited by your claim #52/);
  assert.match(lines[0], /staging cluster/);
  assert.match(lines[1], /claim #52/);
  assert.match(lines[1], /contradicts admitted claim\(s\) #31, #33/);
});

test('an empty blocker set renders nothing', () => {
  assert.deepEqual(describeStopBlockers({ quarantined: [], contradictions: [] }), []);
  assert.deepEqual(describeStopBlockers(undefined), []);
});

// -------------------------------------------------- the socket and the hook

test('peek carries the attention snapshot alongside the queue', () => {
  const session = createBridgeSession({ client: { cfg: { slug: 'acme', incidentId: 'inc-1' } } });
  session.attention = attention({ flaggedOwnContext: [flagged()] });
  const res = handleSocketRequest({ op: 'peek' }, session, { pid: 1 });
  assert.equal(res.attention.flaggedOwnContext.length, 1);
  assert.equal(res.count, 0); // and it is a SEPARATE thing from what is queued
});

test('a serve process with no attention answers null, not a throw', () => {
  const res = handleSocketRequest({ op: 'peek' }, createBridgeSession({}), { pid: 1 });
  assert.equal(res.attention, null);
});

test('a blocker alone refuses a conclusion, with nothing queued and nothing consumed', () => {
  const d = buildStopDecision([
    { socketPath: '/s/1', response: { count: 0, dropped: 0, cursor: 5, maxSeq: 5, digest: [], attention: attention({ flaggedOwnContext: [flagged()] }) } },
  ]);
  assert.equal(d.block, true);
  assert.match(d.reason, /Do not conclude yet/);
  assert.match(d.reason, /QUARANTINED/);
  // A quarantined citation is not made untrue by having been mentioned, so
  // there is nothing to advance a cursor past.
  assert.deepEqual(d.consumes, []);
});

test('a pre-#252 serve process (no attention field) behaves exactly as before', () => {
  const quiet = buildStopDecision([{ socketPath: '/s/1', response: { count: 0, dropped: 0, cursor: 5, maxSeq: 5, digest: [] } }]);
  assert.equal(quiet.block, false);

  const owed = buildStopDecision([
    { socketPath: '/s/1', response: { count: 1, dropped: 0, cursor: 4, maxSeq: 5, digest: ['⚡ a finding'] } },
  ]);
  assert.equal(owed.block, true);
  assert.deepEqual(owed.consumes, [{ socketPath: '/s/1', upTo: 5 }]);
});

test('blockers come FIRST when both reasons apply, and events still consume', () => {
  const d = buildStopDecision([
    {
      socketPath: '/s/1',
      response: {
        count: 1,
        dropped: 0,
        cursor: 4,
        maxSeq: 5,
        digest: ['⚡ Ana posted a finding'],
        attention: attention({ flaggedOwnContext: [flagged()] }),
      },
    },
  ]);
  assert.equal(d.block, true);
  assert.ok(d.reason.indexOf('QUARANTINED') < d.reason.indexOf('Ana posted a finding'));
  assert.deepEqual(d.consumes, [{ socketPath: '/s/1', upTo: 5 }]);
});

test('the same quarantine seen by two sessions is reported once', () => {
  const one = { count: 0, dropped: 0, cursor: 5, maxSeq: 5, digest: [], attention: attention({ flaggedOwnContext: [flagged()] }) };
  const two = { ...one, attention: attention({ flaggedOwnContext: [flagged({ reason: 'phrased differently' })] }) };
  const d = buildStopDecision([
    { socketPath: '/s/1', response: one },
    { socketPath: '/s/2', response: two },
  ]);
  assert.equal(d.reason.match(/seq 21/g).length, 1);
});

test('the refusal stays inside the hook output cap', () => {
  const many = Array.from({ length: 40 }, (_, i) => flagged({ targetSeq: i, reason: 'x'.repeat(300) }));
  const d = buildStopDecision([
    { socketPath: '/s/1', response: { count: 0, dropped: 0, cursor: 5, maxSeq: 5, digest: [], attention: attention({ flaggedOwnContext: many }) } },
  ], { maxChars: 800 });
  assert.equal(d.block, true);
  assert.ok(d.reason.length <= 800);
});

// ------------------------------------------------------------------- helpers

test('touchesAttention fires on claim/context events and nothing else', () => {
  assert.equal(touchesAttention({ type: 'claim.staged' }), true);
  assert.equal(touchesAttention({ type: 'claim.position.taken' }), true);
  assert.equal(touchesAttention({ type: 'context.flagged' }), true);
  assert.equal(touchesAttention({ type: 'edge.finding' }), false);
  assert.equal(touchesAttention({ type: 'agent.message' }), false);
  assert.equal(touchesAttention(null), false);
});

test('formatDuration is locale-free and never negative', () => {
  assert.equal(formatDuration(0), '0m');
  assert.equal(formatDuration(-5), '0m');
  assert.equal(formatDuration(15 * 60_000), '15m');
  assert.equal(formatDuration(107 * 60_000), '1h 47m');
  assert.equal(formatDuration(Number.NaN), '0m');
});
