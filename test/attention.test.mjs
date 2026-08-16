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
import { ATTENTION_SETTLE_MS, buildBridgeTools, createBridgeSession } from '../src/tools.mjs';
import {
  describeStopBlockers,
  divergenceBlock,
  divergenceKey,
  formatDuration,
  hasStopBlockers,
  stopBlockers,
  touchesAttention,
  voteKey,
  voteRequestBlock,
} from '../src/attention.mjs';
import { buildStopDecision } from '../src/hooks/stop.mjs';
import { answerSocketRequest, handleSocketRequest } from '../src/hooks/socket.mjs';

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

// -------------------------------------- feature 20260812-010632 (US4/T049): getDivergence

test('getDivergence asks for THIS agent\'s read history, same discipline as getAttention', async () => {
  const f = fakeFetch({
    routes: {
      '/o/acme/incidents/inc-1/vetting/divergence': {
        status: 200,
        json: { diverging: true, establishedSubject: 'cli-handoff/redeem', observedSubject: 'cloudfront/5xxerrorrate' },
      },
    },
  });
  const c = new EdgeBridgeClient(CFG, f);
  c.agentInstanceId = 'a-9';
  const res = await c.getDivergence();
  assert.equal(f.calls[0].path, '/o/acme/incidents/inc-1/vetting/divergence');
  assert.equal(f.calls[0].search, '?agentInstanceId=a-9');
  assert.deepEqual(res, { diverging: true, establishedSubject: 'cli-handoff/redeem', observedSubject: 'cloudfront/5xxerrorrate' });
});

test('getDivergence omits agentInstanceId before an instance has been issued, same as getAttention', async () => {
  const f = fakeFetch({ routes: { '/o/acme/incidents/inc-1/vetting/divergence': { status: 200, json: { diverging: false } } } });
  await new EdgeBridgeClient(CFG, f).getDivergence();
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

// ---------------------------------------- feature 20260812-010632 (US4/T043/T044): divergenceBlock

function diverging(over = {}) {
  return { diverging: true, establishedSubject: 'cli-handoff/redeem', observedSubject: 'cloudfront/5xxerrorrate', ...over };
}

test('divergenceBlock is silent when not diverging, absent, or malformed', () => {
  assert.equal(divergenceBlock({ diverging: false }).text, '');
  assert.equal(divergenceBlock(null).text, '');
  assert.equal(divergenceBlock(undefined).text, '');
  assert.equal(divergenceBlock({}).text, '');
});

test('divergenceBlock names both the established and observed subject, and reads as an invitation', () => {
  const { text } = divergenceBlock(diverging());
  assert.match(text, /cli-handoff\/redeem/);
  assert.match(text, /cloudfront\/5xxerrorrate/);
  assert.match(text, /invitation, not a block/);
});

test('divergenceBlock announces a given established/observed pair once, not on every call', () => {
  const notified = new Set();
  const first = divergenceBlock(diverging(), { notified });
  assert.notEqual(first.text, '');
  for (const k of first.keys) notified.add(k);

  // Same pair again: silence.
  assert.equal(divergenceBlock(diverging(), { notified }).text, '');

  // A genuinely DIFFERENT pair (different observed subject): announced again.
  const different = divergenceBlock(diverging({ observedSubject: 'coralogix/query' }), { notified });
  assert.notEqual(different.text, '');
  assert.notEqual(divergenceKey(diverging()), divergenceKey(diverging({ observedSubject: 'coralogix/query' })));
});

test('divergenceBlock never mutates the caller\'s notified set', () => {
  const notified = new Set();
  divergenceBlock(diverging(), { notified });
  assert.equal(notified.size, 0);
});

test('divergenceKey is stable for the same pair and distinct across different pairs', () => {
  assert.equal(divergenceKey(diverging()), divergenceKey(diverging()));
  assert.notEqual(divergenceKey(diverging()), divergenceKey(diverging({ establishedSubject: 'something-else' })));
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
  assert.match(out, /No findings or open items yet/);
  assert.ok(out.indexOf('vote requested') < out.indexOf('No findings or open items yet'));
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
  assert.match(out, /No findings or open items yet/);
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

test('a read still in flight against the OLD room can never answer for the new one', async () => {
  // The case the test above does not reach: it resets already-settled fields,
  // while this one has a genuine unresolved promise spanning the switch.
  //
  // `claimSeq` is a PER-INCIDENT counter, so incident A's claim #48 rendered
  // into a tool result the agent is reading as incident B is not a cosmetic
  // mix-up — corroborate_claim goes straight to the CURRENT client, so acting
  // on it records a position on whatever #48 happens to be in B.
  let releaseOldRead;
  const oldRoomAnswer = attention({ votesAwaited: [awaited({ claimSeq: 48, statement: 'OLD ROOM claim' })] });
  const clientA = {
    agentInstanceId: 'a-1',
    leave: async () => {},
    getAttention: () => new Promise((resolve) => { releaseOldRead = () => resolve(oldRoomAnswer); }),
  };
  const clientB = {
    agentInstanceId: 'a-2',
    join: async () => {},
    leave: async () => {},
    getAttention: async () => attention({ votesAwaited: [awaited({ claimSeq: 7, statement: 'NEW ROOM claim' })] }),
  };

  const session = createBridgeSession({
    client: clientA,
    redeemImpl: async () => ({ ...CFG, incidentId: 'inc-2' }),
    clientFactory: () => clientB,
  });

  // A room event lands in A and starts a read that has not come back yet.
  session.enqueueEvent({ seq: 5, type: 'claim.staged', payload: { statement: 'x' } });
  const oldRead = session.refreshAttention();
  assert.ok(session.attentionInFlight, 'precondition: a read is genuinely in flight');

  await session.joinWarRoom('http://api.test/share/xyz');
  assert.equal(session.attentionInFlight, null, 'the old room’s read is abandoned, not inherited');

  // The next read must go to B rather than short-circuiting onto A's promise.
  const fresh = await session.refreshAttention();
  assert.equal(fresh.votesAwaited[0].statement, 'NEW ROOM claim');

  // And when A's request finally lands it must discard its own answer.
  releaseOldRead();
  await oldRead;
  assert.equal(session.attention.votesAwaited[0].statement, 'NEW ROOM claim');
  assert.equal(session.attention.votesAwaited[0].claimSeq, 7);
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

test('peek names the incident, so a hook can tell two rooms in one checkout apart', () => {
  const session = createBridgeSession({ client: { cfg: { slug: 'acme', incidentId: 'inc-1' } } });
  assert.equal(handleSocketRequest({ op: 'peek' }, session, { pid: 1 }).incidentId, 'inc-1');
});

// -------------------------------- feature 20260812-010632 (US5/T047): status carries votesAwaited

test('status carries a votesAwaited COUNT — landfall status formats a number, not a raw list', () => {
  const session = createBridgeSession({ client: { cfg: { slug: 'acme', incidentId: 'inc-1' } } });
  session.attention = attention({ votesAwaited: [awaited(), awaited({ claimSeq: 49 })] });
  const res = handleSocketRequest({ op: 'status' }, session, { pid: 1 });
  assert.equal(res.votesAwaited, 2);
});

test('status answers votesAwaited: 0 (not a throw) when no attention snapshot exists yet', () => {
  const res = handleSocketRequest({ op: 'status' }, createBridgeSession({}), { pid: 1 });
  assert.equal(res.votesAwaited, 0);
});

test('status is additive — every pre-#T047 field is still present unchanged', () => {
  const session = createBridgeSession({ client: { cfg: { slug: 'acme', incidentId: 'inc-1' } } });
  const res = handleSocketRequest({ op: 'status' }, session, { pid: 1 });
  assert.equal(res.incidentId, 'inc-1');
  assert.equal(res.connected, true);
  assert.equal(typeof res.cursor, 'number');
  assert.equal(typeof res.pending, 'number');
  assert.equal(typeof res.dropped, 'number');
});

// ---------------------------------------- feature 20260812-010632 (US4/T049): status.divergence

test('status omits divergence entirely until a background refresh has actually landed — never a misleading default', () => {
  const session = createBridgeSession({ client: { cfg: { slug: 'acme', incidentId: 'inc-1' } } });
  const res = handleSocketRequest({ op: 'status' }, session, { pid: 1 });
  assert.equal('divergence' in res, false);
});

test('status carries whatever session.divergence last landed, including a false-y-looking but real {diverging:false}', () => {
  const session = createBridgeSession({ client: { cfg: { slug: 'acme', incidentId: 'inc-1' } } });
  session.divergence = { diverging: false };
  const res = handleSocketRequest({ op: 'status' }, session, { pid: 1 });
  assert.deepEqual(res.divergence, { diverging: false });
});

test('status carries a real diverging:true answer verbatim', () => {
  const session = createBridgeSession({ client: { cfg: { slug: 'acme', incidentId: 'inc-1' } } });
  session.divergence = { diverging: true, establishedSubject: 'cli-handoff/redeem', observedSubject: 'cloudfront/5xxerrorrate' };
  const res = handleSocketRequest({ op: 'status' }, session, { pid: 1 });
  assert.equal(res.divergence.diverging, true);
  assert.equal(res.divergence.establishedSubject, 'cli-handoff/redeem');
});

test('refreshDivergence is coalesced (one in-flight read, not one per caller) and best-effort on failure', async () => {
  let calls = 0;
  let resolve1;
  const session = createBridgeSession({
    client: {
      cfg: { slug: 'acme', incidentId: 'inc-1' },
      getDivergence: () => { calls += 1; return new Promise((r) => { resolve1 = r; }); },
    },
  });
  const p1 = session.refreshDivergence();
  const p2 = session.refreshDivergence(); // joins the same in-flight read
  assert.equal(calls, 1);
  resolve1({ diverging: false });
  await Promise.all([p1, p2]);
  assert.deepEqual(session.divergence, { diverging: false });
});

test('a query-kind contribution (search_context) triggers a background divergence refresh', async () => {
  let calls = 0;
  const fakeClient = {
    agentInstanceId: 'a-1',
    cfg: { slug: 'acme', incidentId: 'inc-1' },
    async heartbeat() {},
    async contribute() {},
    async getBrief() { return []; },
    async searchContext() { return { hits: [] }; },
    async getDivergence() { calls += 1; return { diverging: false }; },
  };
  const session = createBridgeSession({ client: fakeClient });
  const tools = buildBridgeTools(session);
  const searchTool = tools.find((t) => t.name === 'search_context');
  await searchTool.handler({ query: 'cloudfront' });
  // The refresh is fire-and-forget (void), so give the microtask queue a turn.
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(calls, 1);
});

test('a quarantine arriving with NO tool call in between still reaches the Stop hook', async () => {
  // The real chain, not an injected snapshot: an event lands on the realtime
  // socket, the agent makes no further tool call (the Stop path makes none),
  // and the hook peeks. Before the fix, `enqueueEvent` only set a dirty flag
  // and the sole reader of that flag was a tool call — so this peek returned
  // the last blocker-free snapshot and the agent was allowed to conclude on
  // quarantined content.
  const f = fakeFetch({ attention: attention({ flaggedOwnContext: [flagged()] }) });
  const session = await joinedSession(f);
  session.attention = attention({}); // a clean snapshot from earlier in the session
  session.attentionDirty = false;

  session.enqueueEvent({ seq: 7, type: 'context.quarantined' });

  const res = await answerSocketRequest({ op: 'peek' }, session, { pid: 1 });
  assert.equal(res.attention.flaggedOwnContext.length, 1);
  assert.equal(buildStopDecision([{ socketPath: '/s/1', response: res }]).block, true);
});

test('a burst of claim events costs ONE read, not one per event', async () => {
  const f = fakeFetch();
  const session = await joinedSession(f);
  session.attentionDirty = false;

  for (let seq = 1; seq <= 5; seq += 1) session.enqueueEvent({ seq, type: 'claim.staged' });
  await answerSocketRequest({ op: 'peek' }, session, { pid: 1 });

  assert.equal(f.calls.filter((c) => c.path.endsWith('/vetting/attention')).length, 1);
});

test('an event that cannot change attention triggers no read at all', async () => {
  const f = fakeFetch();
  const session = await joinedSession(f);
  session.attentionDirty = false;
  session.attention = attention({});

  session.enqueueEvent({ seq: 1, type: 'edge.finding' });
  await answerSocketRequest({ op: 'peek' }, session, { pid: 1 });

  assert.equal(f.calls.filter((c) => c.path.endsWith('/vetting/attention')).length, 0);
});

test('a hung attention read does not hang the hook — it answers with what it has', async () => {
  // A hook that hangs is a hook that gets uninstalled. The wait is bounded, so
  // a slow server costs a possibly-stale answer and never a stuck agent.
  const never = new Promise(() => {});
  const session = createBridgeSession({
    client: { cfg: { slug: 'acme', incidentId: 'inc-1' }, getAttention: () => never },
  });
  session.attention = attention({ flaggedOwnContext: [flagged()] });
  session.attentionDirty = true;

  const started = Date.now();
  const res = await answerSocketRequest({ op: 'peek' }, session, { pid: 1 });

  assert.ok(Date.now() - started < ATTENTION_SETTLE_MS + 200);
  assert.equal(res.attention.flaggedOwnContext.length, 1); // the previous answer, not null
});

test('a failed attention read leaves the previous snapshot and stays dirty for a retry', async () => {
  const session = createBridgeSession({
    client: {
      cfg: { slug: 'acme', incidentId: 'inc-1' },
      getAttention: async () => { throw new Error('server unreachable'); },
    },
  });
  session.attention = attention({ flaggedOwnContext: [flagged()] });
  session.attentionDirty = true;

  const res = await answerSocketRequest({ op: 'peek' }, session, { pid: 1 });

  assert.equal(res.attention.flaggedOwnContext.length, 1);
  assert.equal(session.attentionDirty, true);
});

test('an abandoned slow read can never clobber the fresher snapshot that replaced it', async () => {
  // Two reads completing out of START order — a retry, a pool wait or a GC
  // pause is enough. The first misses its budget and is abandoned, a second
  // takes over and sees the quarantine; then the first finally lands with its
  // pre-quarantine answer. Assigning it unconditionally would overwrite the
  // newer answer AND leave the snapshot clean, so no later event would ever
  // correct it: a blocker-free Stop for the rest of the session, which is the
  // exact failure the settle path was added to prevent.
  const deferred = [];
  const session = createBridgeSession({
    client: {
      cfg: { slug: 'acme', incidentId: 'inc-1' },
      getAttention: () => new Promise((resolve) => { deferred.push(resolve); }),
    },
  });
  session.attention = attention({}); // clean, from earlier in the session
  session.attentionDirty = true;

  // Read #1 starts, misses the budget, and the hook answers with what it has.
  const answered = await answerSocketRequest({ op: 'peek' }, session, { pid: 1 });
  assert.equal(answered.attention.flaggedOwnContext.length, 0);
  assert.equal(deferred.length, 1);

  // Read #2 — fresh, and this one sees the quarantine.
  const second = session.refreshAttention();
  assert.equal(deferred.length, 2);
  deferred[1](attention({ flaggedOwnContext: [flagged()] }));
  await second;
  assert.equal(session.attention.flaggedOwnContext.length, 1);

  // Read #1 lands at last, carrying the world as it was before the quarantine.
  deferred[0](attention({}));
  await new Promise((resolve) => setTimeout(resolve, 0));

  assert.equal(session.attention.flaggedOwnContext.length, 1);
  const res = await answerSocketRequest({ op: 'peek' }, session, { pid: 1 });
  assert.equal(buildStopDecision([{ socketPath: '/s/1', response: res }]).block, true);
});

test('a superseded read that FAILS does not re-dirty the snapshot its successor owns', async () => {
  // Same out-of-order shape, failing instead of resolving. The current read's
  // answer is good; a corpse's error says nothing about it, and re-dirtying
  // would buy a pointless extra round trip on the next tool call.
  const deferred = [];
  const session = createBridgeSession({
    client: {
      cfg: { slug: 'acme', incidentId: 'inc-1' },
      getAttention: () => new Promise((resolve, reject) => { deferred.push({ resolve, reject }); }),
    },
  });
  session.attention = attention({});
  session.attentionDirty = true;

  await answerSocketRequest({ op: 'peek' }, session, { pid: 1 }); // abandons read #1
  const second = session.refreshAttention();
  deferred[1].resolve(attention({ flaggedOwnContext: [flagged()] }));
  await second;

  deferred[0].reject(new Error('server unreachable'));
  await new Promise((resolve) => setTimeout(resolve, 0));

  assert.equal(session.attention.flaggedOwnContext.length, 1);
  assert.equal(session.attentionDirty, false);
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

test('the same quarantine seen by two sessions in the SAME incident is reported once', () => {
  const one = { incidentId: 'inc-a', count: 0, dropped: 0, cursor: 5, maxSeq: 5, digest: [], attention: attention({ flaggedOwnContext: [flagged()] }) };
  const two = { ...one, attention: attention({ flaggedOwnContext: [flagged({ reason: 'phrased differently' })] }) };
  const d = buildStopDecision([
    { socketPath: '/s/1', response: one },
    { socketPath: '/s/2', response: two },
  ]);
  assert.equal(d.reason.match(/seq 21/g).length, 1);
});

test('two DIFFERENT incidents sharing a seq both survive the merge', () => {
  // Sockets are joined by workspace, not by incident, so two serve processes in
  // one checkout can be in two different rooms. Per-incident sequence numbers
  // are small counters, so a collision is ordinary — and a bare-seq dedupe
  // dropped the second session's real blocker on first-answer-wins, which is
  // the under-reporting this whole tier exists to prevent.
  const base = { count: 0, dropped: 0, cursor: 5, maxSeq: 5, digest: [] };
  const d = buildStopDecision([
    { socketPath: '/s/1', response: { ...base, incidentId: 'inc-a', attention: attention({ flaggedOwnContext: [flagged({ reason: 'the A room ruling' })] }) } },
    { socketPath: '/s/2', response: { ...base, incidentId: 'inc-b', attention: attention({ flaggedOwnContext: [flagged({ reason: 'the B room ruling' })] }) } },
  ]);
  assert.equal(d.reason.match(/seq 21/g).length, 2);
  assert.ok(d.reason.includes('the A room ruling'));
  assert.ok(d.reason.includes('the B room ruling'));
});

test('an answer with no incidentId falls back to its own socket — over-reports, never drops', () => {
  // An older serve process, before `peek` carried the incident. Collapsing
  // those into one scope would reintroduce exactly the cross-incident drop
  // above, so each is scoped to itself.
  const base = { count: 0, dropped: 0, cursor: 5, maxSeq: 5, digest: [] };
  const d = buildStopDecision([
    { socketPath: '/s/1', response: { ...base, attention: attention({ flaggedOwnContext: [flagged()] }) } },
    { socketPath: '/s/2', response: { ...base, attention: attention({ flaggedOwnContext: [flagged()] }) } },
  ]);
  assert.equal(d.reason.match(/seq 21/g).length, 2);
});

test('mid-upgrade, an old and a new process on one item report it twice — never zero times', () => {
  // One serve process upgraded, one not yet, same incident, same item. The old
  // answer carries no incident, so matching it to the new one would mean
  // assuming equal sequence numbers mean the same item — the assumption that
  // produced the cross-incident drop above. The duplicate line is the accepted
  // cost, and it disappears once every process in the workspace is new.
  const base = { count: 0, dropped: 0, cursor: 5, maxSeq: 5, digest: [] };
  const d = buildStopDecision([
    { socketPath: '/s/1', response: { ...base, attention: attention({ flaggedOwnContext: [flagged()] }) } },
    { socketPath: '/s/2', response: { ...base, incidentId: 'inc-a', attention: attention({ flaggedOwnContext: [flagged()] }) } },
  ]);
  assert.equal(d.reason.match(/seq 21/g).length, 2);
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

// ---------------------------- feature 20260812-010632 (US4/T044): end-to-end nudge wiring

function fakeDivergenceClient(divergenceSequence) {
  let i = 0;
  return {
    agentInstanceId: 'a-1',
    cfg: { slug: 'acme', incidentId: 'inc-1' },
    async heartbeat() {},
    async contribute() {},
    async getBrief() { return []; },
    async getContextFrame() {
      return { asOfSeq: -1, version: 0, freshnessMs: 0, incident: {}, brief: { established: [], workingTheory: [], disproved: [], open: [] }, participants: [] };
    },
    async getContextDelta(since) { return { sinceVersion: since, toVersion: since, items: [], routineCount: 0 }; },
    async searchContext() { return { hits: [] }; },
    async getDivergence() {
      const next = divergenceSequence[Math.min(i, divergenceSequence.length - 1)];
      i += 1;
      return next;
    },
  };
}

test('a tool result carries the nudge exactly once for a given established/observed pair across a sequence of calls, then falls silent', async () => {
  // Deliberately agnostic about WHICH call in the sequence ends up carrying
  // it — search_context's own handler body awaits client.getBrief(), which
  // can give the fire-and-forget refresh triggered at its start enough of a
  // microtask turn to land before that SAME call's own closing flush, so the
  // nudge may appear on the triggering call itself rather than only a later
  // one. The real guarantee under test is the count across the sequence:
  // exactly once, never zero, never more than once.
  const client = fakeDivergenceClient([diverging()]);
  const session = createBridgeSession({ client });
  const tools = buildBridgeTools(session);
  const search = tools.find((t) => t.name === 'search_context');
  const brief = tools.find((t) => t.name === 'get_brief');

  const results = [];
  results.push(await search.handler({ query: 'cloudfront' }));
  await new Promise((r) => setTimeout(r, 0)); // let any still-in-flight refresh land
  results.push(await brief.handler({}));
  results.push(await brief.handler({}));
  results.push(await brief.handler({}));

  const withNudge = results.filter((r) => r.includes('cli-handoff/redeem'));
  assert.equal(withNudge.length, 1, `expected the nudge exactly once across the sequence, got ${withNudge.length}`);
  assert.match(withNudge[0], /invitation, not a block/);
});

test('a tool result carries NO nudge when the read says {diverging:false}', async () => {
  const client = fakeDivergenceClient([{ diverging: false }]);
  const session = createBridgeSession({ client });
  const tools = buildBridgeTools(session);
  const search = tools.find((t) => t.name === 'search_context');
  await search.handler({ query: 'cli-handoff' });
  await new Promise((r) => setTimeout(r, 0));

  const brief = tools.find((t) => t.name === 'get_brief');
  const result = await brief.handler({});
  assert.equal(result.includes('diverging'), false);
  assert.equal(result.includes('invitation'), false);
});

test('a tool result carries no nudge before any divergence read has ever landed', async () => {
  // No search_context call at all — session.divergence stays null.
  const client = fakeDivergenceClient([diverging()]);
  const session = createBridgeSession({ client });
  const tools = buildBridgeTools(session);
  const brief = tools.find((t) => t.name === 'get_brief');
  const result = await brief.handler({});
  assert.equal(result.includes('cli-handoff/redeem'), false);
});

test('divergence, however sustained, NEVER touches stopBlockers()/tier-1 — it is tier-0 only', () => {
  // stopBlockers() takes an attention snapshot and has no divergence parameter
  // at all — the type signature itself is the proof this tier is untouched,
  // exercised here so a future edit that tried to thread divergence through
  // it would break this test rather than silently widening what blocks a
  // conclusion.
  const blockers = stopBlockers(attention({ flaggedOwnContext: [], votesAwaited: [] }));
  assert.equal(hasStopBlockers(blockers), false);
  // A room with an active, sustained, repeatedly-diverging investigator is
  // exactly the scenario a hypothetical tier-1 divergence block would fire
  // on — stopBlockers's signature does not even accept a divergence read, so
  // there is no path by which it could.
  assert.equal(stopBlockers.length, 1, 'stopBlockers takes only an attention snapshot, never a divergence read');
});
