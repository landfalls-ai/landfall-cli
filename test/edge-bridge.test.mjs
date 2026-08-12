// edge-bridge.test.mjs — feature-012 gate + feature-021 magic-link/live-sync units
// (node --test, ESM-native).
import test from 'node:test';
import assert from 'node:assert/strict';
import { EdgeBridgeClient } from '../src/client.mjs';
import { handleMcpMessage } from '../src/mcp.mjs';
import { buildBridgeTools, createBridgeSession, EDGE_AGENT_INSTRUCTIONS } from '../src/tools.mjs';
import { narrateDoing, contributionFor } from '../src/narrate.mjs';
import { parseShareLink, redeemShareLink } from '../src/link.mjs';
import { describeEvent, watchIncident } from '../src/live.mjs';

const CFG = { baseUrl: 'http://api.test', slug: 'acme', incidentId: 'inc-1', token: 'tok', agentLabel: 'Claude Code' };

// a fetch fake that records calls
function fakeFetch(routes = {}) {
  const calls = [];
  const fn = async (url, opts = {}) => {
    calls.push({ url, method: opts.method ?? 'GET', body: opts.body ? JSON.parse(opts.body) : undefined });
    const r = routes[new URL(url).pathname] ?? { status: 202 };
    return { ok: true, status: r.status ?? 200, json: async () => r.json ?? {} };
  };
  fn.calls = calls;
  return fn;
}

test('client.join stores the server-issued agentInstanceId', async () => {
  const f = fakeFetch({ '/o/acme/incidents/inc-1/edge/join': { status: 201, json: { agentInstanceId: 'a-9' } } });
  const c = new EdgeBridgeClient(CFG, f);
  await c.join();
  assert.equal(c.agentInstanceId, 'a-9');
  assert.equal(f.calls[0].body.edgeAgentLabel, 'Claude Code');
});

test('client heartbeat/contribute/getBrief hit the edge/* contract', async () => {
  const f = fakeFetch({ '/o/acme/incidents/inc-1/edge/join': { status: 201, json: { agentInstanceId: 'a-9' } }, '/o/acme/incidents/inc-1/events': { status: 200, json: [{ type: 'agent.finding' }] } });
  const c = new EdgeBridgeClient(CFG, f);
  await c.join();
  await c.heartbeat('investigating');
  await c.contribute('finding', { text: 'origin 502s' });
  const brief = await c.getBrief();
  const paths = f.calls.map((x) => `${x.method} ${new URL(x.url).pathname}`);
  assert.ok(paths.includes('POST /o/acme/incidents/inc-1/edge/heartbeat'));
  assert.ok(paths.includes('POST /o/acme/incidents/inc-1/edge/contributions'));
  assert.ok(paths.includes('GET /o/acme/incidents/inc-1/events'));
  assert.equal(brief[0].type, 'agent.finding');
});

test('narration maps tool calls to a "doing" + a contribution for durable artifacts only', () => {
  assert.match(narrateDoing('post_finding', { text: 'x' }), /posting a finding/);
  assert.equal(narrateDoing('get_brief'), 'reviewing the incident brief');
  assert.equal(contributionFor('post_finding', { text: 'x' }).kind, 'finding');
  assert.equal(contributionFor('propose_action', { description: 'roll back' }).kind, 'action');
  assert.equal(contributionFor('get_brief'), null); // reads don't spam the timeline
});

test('post_widget (022) contributes a widget to the member sub-investigation dashboard', async () => {
  assert.match(narrateDoing('post_widget', { title: 'Checkout tasks' }), /dashboard widget/);
  const contrib = contributionFor('post_widget', { widgetType: 'stat', title: 'Checkout tasks', data: { value: 0 } });
  assert.equal(contrib.kind, 'widget');
  assert.deepEqual(contrib.body, { widgetType: 'stat', title: 'Checkout tasks', data: { value: 0 } });

  const posted = [];
  const fakeClient = {
    agentInstanceId: 'a-9',
    async heartbeat() {},
    async contribute(kind, body) { posted.push([kind, body]); },
    async getBrief() { return []; },
  };
  const tools = buildBridgeTools(fakeClient);
  const res = await handleMcpMessage(
    { jsonrpc: '2.0', id: 9, method: 'tools/call', params: { name: 'post_widget', arguments: { widgetType: 'stat', title: 'Checkout tasks', data: { value: 0 } } } },
    { tools },
  );
  assert.match(res.result.content[0].text, /added to your sub-investigation dashboard/i);
  assert.ok(posted.some(([kind, body]) => kind === 'widget' && body.title === 'Checkout tasks'));
});

test('upload_artifact (025) narrates presence but does NOT double-post a contribution', () => {
  assert.match(narrateDoing('upload_artifact', { filename: 'report.html' }), /sharing an artifact: "report\.html"/);
  // the /artifacts endpoint appends artifact.shared itself — the generic
  // contribution path must stay null so the event is not posted twice.
  assert.equal(contributionFor('upload_artifact', { filename: 'report.html' }), null);
});

test('upload_artifact (025) shares inline content: heartbeats, posts to /artifacts, no double contribution', async () => {
  const posted = [];
  const fakeClient = {
    agentInstanceId: 'a-9',
    async heartbeat(doing) { posted.push(['heartbeat', doing]); },
    async contribute(kind) { posted.push(['contribute', kind]); },
    async uploadArtifact(filename, contentType, dataBase64) {
      posted.push(['upload', filename, contentType, dataBase64]);
      return { artifactId: 'art-1', filename, contentType, size: Buffer.from(dataBase64, 'base64').length, safeRenderMode: 'sandboxed-iframe' };
    },
    async getBrief() { return []; },
  };
  const tools = buildBridgeTools(fakeClient);
  const res = await handleMcpMessage(
    { jsonrpc: '2.0', id: 11, method: 'tools/call', params: { name: 'upload_artifact', arguments: { content: '<html>hi</html>', filename: 'report.html' } } },
    { tools },
  );
  assert.match(res.result.content[0].text, /Shared "report\.html" \(text\/html/);
  assert.ok(posted.some(([k]) => k === 'heartbeat'));
  // it uploaded via the dedicated endpoint...
  const up = posted.find(([k]) => k === 'upload');
  assert.ok(up && up[2] === 'text/html');
  // ...and did NOT also post a generic contribution (no double-post).
  assert.ok(!posted.some(([k]) => k === 'contribute'));
});

test('upload_artifact (025) client-side pre-check refuses oversized/disallowed, sharing nothing', async () => {
  let uploaded = false;
  const fakeClient = {
    agentInstanceId: 'a-9',
    async heartbeat() {},
    async uploadArtifact() { uploaded = true; return {}; },
    async getBrief() { return []; },
  };
  const tools = buildBridgeTools(fakeClient);
  // disallowed type (inferred from extension)
  const bad = await handleMcpMessage(
    { jsonrpc: '2.0', id: 12, method: 'tools/call', params: { name: 'upload_artifact', arguments: { content: 'x', filename: 'evil.exe', contentType: 'application/x-msdownload' } } },
    { tools },
  );
  assert.match(bad.result.content[0].text, /not allowed.*Nothing shared/i);
  assert.equal(uploaded, false); // never reached the server
});

test('MCP: initialize + tools/list + tools/call', async () => {
  const tools = buildBridgeTools(new EdgeBridgeClient(CFG, fakeFetch({ '/o/acme/incidents/inc-1/edge/join': { status: 201, json: { agentInstanceId: 'a-9' } } })));
  const init = await handleMcpMessage({ jsonrpc: '2.0', id: 1, method: 'initialize' }, { tools });
  assert.equal(init.result.protocolVersion, '2024-11-05');
  const list = await handleMcpMessage({ jsonrpc: '2.0', id: 2, method: 'tools/list' }, { tools });
  const names = list.result.tools.map((t) => t.name);
  assert.ok(names.includes('get_brief') && names.includes('post_finding') && names.includes('propose_action'));
  const notif = await handleMcpMessage({ jsonrpc: '2.0', method: 'notifications/initialized' }, { tools });
  assert.equal(notif, null);
  const unknown = await handleMcpMessage({ jsonrpc: '2.0', id: 3, method: 'tools/call', params: { name: 'nope' } }, { tools });
  assert.equal(unknown.error.code, -32602);
});

test('MCP: initialize injects server instructions when provided (so the share paste can stay minimal)', async () => {
  const tools = buildBridgeTools(new EdgeBridgeClient(CFG, fakeFetch()));
  // no instructions passed → field omitted
  const bare = await handleMcpMessage({ jsonrpc: '2.0', id: 1, method: 'initialize' }, { tools });
  assert.equal(bare.result.instructions, undefined);
  // instructions passed → surfaced on the initialize result for the client to inject
  const withInstr = await handleMcpMessage(
    { jsonrpc: '2.0', id: 2, method: 'initialize' },
    { tools, instructions: EDGE_AGENT_INSTRUCTIONS },
  );
  assert.equal(withInstr.result.instructions, EDGE_AGENT_INSTRUCTIONS);
  // the guidance moved off the paste and onto the server: it covers the loop + guardrails
  assert.match(EDGE_AGENT_INSTRUCTIONS, /get_brief/);
  assert.match(EDGE_AGENT_INSTRUCTIONS, /propose-only/);
});

// -------------------------------- feature 20260812-010632 (US5/T050b, FR-030/SC-014)

test('EDGE_AGENT_INSTRUCTIONS states the delivery-timing ceiling plainly — no mid-turn push is ever implied', () => {
  assert.match(EDGE_AGENT_INSTRUCTIONS, /never mid-turn, unprompted/);
  assert.match(EDGE_AGENT_INSTRUCTIONS, /no push into an in-progress turn/i);
  // "realtime" (line 1) describes what OTHER participants see of YOUR
  // publishes — the ceiling line must not contradict that, only clarify what
  // it does and does not promise about the reverse direction.
  assert.match(EDGE_AGENT_INSTRUCTIONS, /realtime/);
});

test('MCP tools/call narrates: a post_finding call heartbeats + posts a finding contribution', async () => {
  const posted = [];
  const fakeClient = {
    agentInstanceId: 'a-9',
    async heartbeat(doing) { posted.push(['heartbeat', doing]); },
    async contribute(kind, body) { posted.push(['contribute', kind, body?.text]); },
    async getBrief() { return []; },
  };
  const tools = buildBridgeTools(fakeClient);
  const res = await handleMcpMessage({ jsonrpc: '2.0', id: 5, method: 'tools/call', params: { name: 'post_finding', arguments: { text: 'origin regressed' } } }, { tools });
  assert.match(res.result.content[0].text, /posted/i);
  assert.ok(posted.some(([k]) => k === 'heartbeat'));
  assert.ok(posted.some(([k, kind]) => k === 'contribute' && kind === 'finding'));
});

// ---- feature 021: magic link + live investigation loop ----------------------

const SHARE_URL = 'http://api.test/o/acme/incidents/inc-1/agent?ticket=tkt-123';

test('parseShareLink extracts baseUrl/slug/incident/ticket; rejects non-links', () => {
  const p = parseShareLink(SHARE_URL);
  assert.deepEqual(p, { baseUrl: 'http://api.test', slug: 'acme', incidentId: 'inc-1', ticket: 'tkt-123' });
  assert.throws(() => parseShareLink('http://api.test/o/acme/incidents/inc-1'), /agent share link/);
  assert.throws(() => parseShareLink('http://api.test/o/acme/incidents/inc-1/agent'), /missing its join ticket/);
});

test('redeemShareLink posts the ticket and returns the scoped bridge config', async () => {
  const f = fakeFetch({ '/o/acme/incidents/inc-1/edge/redeem': { status: 201, json: { token: 'edge-tok', humanActorId: 'u-1' } } });
  const cfg = await redeemShareLink(SHARE_URL, { fetchImpl: f });
  assert.deepEqual(cfg, { baseUrl: 'http://api.test', slug: 'acme', incidentId: 'inc-1', token: 'edge-tok', humanActorId: 'u-1' });
  assert.equal(f.calls[0].body.ticket, 'tkt-123');
  // baseUrl override wins over the link origin (dev/tests)
  const f2 = fakeFetch({ '/o/acme/incidents/inc-1/edge/redeem': { status: 201, json: { token: 't' } } });
  const cfg2 = await redeemShareLink(SHARE_URL, { baseUrl: 'http://127.0.0.1:9999', fetchImpl: f2 });
  assert.equal(cfg2.baseUrl, 'http://127.0.0.1:9999');
  assert.match(f2.calls[0].url, /^http:\/\/127\.0\.0\.1:9999\//);
});

test('join_war_room tool joins via the magic link and unlocks the other tools', async () => {
  const joined = [];
  const fakeClient = {
    agentInstanceId: 'a-1',
    async join() { joined.push('join'); },
    async heartbeat() {},
    async contribute() {},
    async getBrief() { return []; },
    async getUpdates() { return []; },
    async leave() {},
  };
  const session = createBridgeSession({
    agentLabel: 'Claude Code',
    redeemImpl: async (url) => ({ baseUrl: 'http://api.test', slug: 'acme', incidentId: 'inc-1', token: 'edge-tok', shareUrl: url }),
    clientFactory: () => fakeClient,
  });
  const tools = buildBridgeTools(session);

  // before joining, investigation tools fail closed with a helpful error
  const early = await handleMcpMessage({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'get_brief' } }, { tools });
  assert.equal(early.result.isError, true);
  assert.match(early.result.content[0].text, /join_war_room/);

  const res = await handleMcpMessage({ jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'join_war_room', arguments: { shareUrl: SHARE_URL } } }, { tools });
  assert.match(res.result.content[0].text, /Joined war room for incident inc-1/);
  assert.deepEqual(joined, ['join']);
  assert.equal(session.client, fakeClient);

  const brief = await handleMcpMessage({ jsonrpc: '2.0', id: 3, method: 'tools/call', params: { name: 'get_brief' } }, { tools });
  assert.match(brief.result.content[0].text, /Incident timeline/);
});

// ---- feature 20260812-010632 (US5/T046): join_war_room returns the brief inline ----

test('join_war_room includes the brief inline — no second get_brief call required to see current state', async () => {
  let getBriefCalls = 0;
  const fakeClient = {
    agentInstanceId: 'a-2',
    async join() {},
    async heartbeat() {},
    async contribute() {},
    async getBrief() {
      getBriefCalls += 1;
      return [{ seq: 0, type: 'incident.opened', payload: {} }];
    },
    async getUpdates() { return []; },
    async leave() {},
  };
  const session = createBridgeSession({
    agentLabel: 'Claude Code',
    redeemImpl: async (url) => ({ baseUrl: 'http://api.test', slug: 'acme', incidentId: 'inc-2', token: 'edge-tok', shareUrl: url }),
    clientFactory: () => fakeClient,
  });
  const tools = buildBridgeTools(session);

  const res = await handleMcpMessage({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'join_war_room', arguments: { shareUrl: SHARE_URL } } }, { tools });
  const text = res.result.content[0].text;
  assert.match(text, /Joined war room for incident inc-2/);
  // The brief content — not just a pointer telling the model to call get_brief.
  assert.match(text, /Incident timeline: 1 events/);
  assert.equal(getBriefCalls, 1);
});

test('join_war_room still succeeds if the inline brief fetch fails — the join itself must not fail', async () => {
  const fakeClient = {
    agentInstanceId: 'a-3',
    async join() {},
    async heartbeat() {},
    async contribute() {},
    async getBrief() { throw new Error('network blip'); },
    async getUpdates() { return []; },
    async leave() {},
  };
  const session = createBridgeSession({
    agentLabel: 'Claude Code',
    redeemImpl: async (url) => ({ baseUrl: 'http://api.test', slug: 'acme', incidentId: 'inc-3', token: 'edge-tok', shareUrl: url }),
    clientFactory: () => fakeClient,
  });
  const tools = buildBridgeTools(session);

  const res = await handleMcpMessage({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'join_war_room', arguments: { shareUrl: SHARE_URL } } }, { tools });
  assert.equal(res.result.isError, undefined);
  assert.match(res.result.content[0].text, /Joined war room for incident inc-3/);
  assert.match(res.result.content[0].text, /call get_brief/);
});

test('get_updates pulls only past the durable cursor and advances it', async () => {
  const all = [
    { seq: 0, type: 'edge.participant.joined', payload: {} },
    { seq: 1, type: 'edge.finding', payload: { humanActorId: 'u-2', displayName: 'Dana', edgeAgentLabel: 'Codex', text: 'origin pool unhealthy' } },
    { seq: 2, type: 'edge.hypothesis', payload: { humanActorId: 'u-2', displayName: 'Dana', text: 'bad rollout' } },
  ];
  const fakeClient = {
    agentInstanceId: 'a-1',
    async heartbeat() {},
    async contribute() {},
    async getBrief() { return all; },
    async getUpdates(since) { return all.filter((e) => e.seq > since); },
  };
  const session = createBridgeSession({ client: fakeClient });
  const tools = buildBridgeTools(session);

  const first = await handleMcpMessage({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'get_updates' } }, { tools });
  assert.match(first.result.content[0].text, /3 new event\(s\)/);
  assert.match(first.result.content[0].text, /origin pool unhealthy/);
  // feature 024: attribution uses the resolved display name, never the raw id.
  assert.match(first.result.content[0].text, /Dana · Codex/);
  assert.doesNotMatch(first.result.content[0].text, /u-2/);
  assert.equal(session.cursor, 2);

  const second = await handleMcpMessage({ jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'get_updates' } }, { tools });
  assert.match(second.result.content[0].text, /No new shared context since seq 2/);

  all.push({ seq: 3, type: 'edge.finding', payload: { humanActorId: 'u-3', text: 'rollback fixed staging' } });
  const third = await handleMcpMessage({ jsonrpc: '2.0', id: 3, method: 'tools/call', params: { name: 'get_updates' } }, { tools });
  assert.match(third.result.content[0].text, /1 new event\(s\) since seq 2/);
  assert.equal(session.cursor, 3);
});

test('get_brief advances the cursor so get_updates only returns genuinely new events', async () => {
  const all = [
    { seq: 0, type: 'edge.finding', payload: { text: 'seen already' } },
    { seq: 1, type: 'edge.finding', payload: { text: 'also seen' } },
  ];
  const fakeClient = {
    agentInstanceId: 'a-1',
    async heartbeat() {},
    async contribute() {},
    async getBrief() { return all; },
    async getUpdates(since) { return all.filter((e) => e.seq > since); },
  };
  const session = createBridgeSession({ client: fakeClient });
  const tools = buildBridgeTools(session);
  await handleMcpMessage({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'get_brief' } }, { tools });
  assert.equal(session.cursor, 1);
  const upd = await handleMcpMessage({ jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'get_updates' } }, { tools });
  assert.match(upd.result.content[0].text, /No new shared context/);
});

// ---- feature 188/#217: live events park on the session between tool calls ----

test('enqueueEvent parks live events, deduped by seq, dropping ones at/below the cursor', () => {
  const session = createBridgeSession({ client: {} });
  session.cursor = 4;

  assert.equal(session.enqueueEvent({ seq: 4, type: 'edge.finding' }), false); // already delivered
  assert.equal(session.enqueueEvent({ seq: 2, type: 'edge.finding' }), false);
  assert.equal(session.enqueueEvent({ seq: 6, type: 'edge.finding' }), true);
  assert.equal(session.enqueueEvent({ seq: 6, type: 'edge.finding' }), false); // duplicate seq
  assert.equal(session.enqueueEvent({ seq: 5, type: 'edge.hypothesis' }), true);
  assert.equal(session.enqueueEvent({ type: 'edge.finding' }), false); // no seq to dedupe on

  assert.deepEqual(session.pending.map((e) => e.seq), [5, 6]); // held, in seq order
});

test('the pending queue is bounded, keeping the newest and counting what it dropped', () => {
  const session = createBridgeSession({ client: {} });
  for (let seq = 0; seq < 60; seq += 1) session.enqueueEvent({ seq, type: 'edge.finding' });

  assert.equal(session.pending.length, 50);
  assert.equal(session.pending[0].seq, 10);
  assert.equal(session.pending.at(-1).seq, 59);
  assert.equal(session.pendingDropped, 10);
});

test('a pull the agent made itself is not repeated back to it by the flush', async () => {
  const all = [
    { seq: 0, type: 'edge.finding', payload: { text: 'first' } },
    { seq: 1, type: 'edge.finding', payload: { text: 'second' } },
  ];
  const fakeClient = {
    agentInstanceId: 'a-1',
    async heartbeat() {},
    async contribute() {},
    async getBrief() { return all; },
    async getUpdates(since) { return all.filter((e) => e.seq > since); },
  };
  const session = createBridgeSession({ client: fakeClient });
  const tools = buildBridgeTools(session);

  session.enqueueEvent({ seq: 1, type: 'edge.finding', payload: { text: 'second' } });
  session.enqueueEvent({ seq: 2, type: 'edge.finding', payload: { text: 'third' } });
  const res = await handleMcpMessage({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'get_updates' } }, { tools });
  const text = res.result.content[0].text;

  // seq 0 and 1 came back on the pull itself; only seq 2 was still owed
  assert.match(text, /2 new event\(s\) since seq -1/);
  assert.match(text, /⚠ 1 update\(s\) from other investigators/);
  assert.match(text, /#2 edge\.finding — third/);
  assert.equal(text.match(/#1 edge\.finding/g).length, 1);
  assert.equal(session.cursor, 2);
  assert.deepEqual(session.pending, []);
});

test('a rejoin clears whatever the queue was holding', async () => {
  const fakeClient = {
    agentInstanceId: 'a-1',
    async join() {},
    async heartbeat() {},
    async contribute() {},
    async getBrief() { return []; },
    async leave() {},
  };
  const session = createBridgeSession({
    client: fakeClient,
    redeemImpl: async () => ({ baseUrl: 'http://api.test', slug: 'acme', incidentId: 'inc-1', token: 'edge-tok' }),
    clientFactory: () => fakeClient,
  });

  session.enqueueEvent({ seq: 7, type: 'edge.finding' });
  await session.joinWarRoom(SHARE_URL);
  assert.deepEqual(session.pending, []);
  assert.equal(session.pendingDropped, 0);
  assert.equal(session.cursor, -1);
});

// ---- feature 188/#218: the queue flushes onto every narrated tool result ----

/** A joined client that answers reads from `all` and records nothing else. */
function queueClient(all = []) {
  return {
    agentInstanceId: 'a-1',
    async heartbeat() {},
    async contribute() {},
    async getBrief() { return all; },
    async getUpdates(since) { return all.filter((e) => e.seq > since); },
  };
}

const callTool = (tools, name, args) =>
  handleMcpMessage({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name, arguments: args } }, { tools })
    .then((r) => r.result.content[0].text);

test('a tool call carries back what other investigators published while the agent worked', async () => {
  const session = createBridgeSession({ client: queueClient() });
  const tools = buildBridgeTools(session);

  session.enqueueEvent({ seq: 4, type: 'edge.finding', payload: { displayName: 'Dana', edgeAgentLabel: 'Codex', text: 'origin pool unhealthy' } });
  const text = await callTool(tools, 'post_finding', { text: 'mine' });

  assert.match(text, /Finding posted to the war room\./); // the tool's own result survives
  assert.match(text, /⚠ 1 update\(s\) from other investigators since your last tool call:/);
  assert.match(text, /#4 edge\.finding \[Dana · Codex\] — origin pool unhealthy/);
});

test('flushed events advance the cursor, so get_updates does not re-deliver them', async () => {
  const all = [{ seq: 4, type: 'edge.finding', payload: { text: 'origin pool unhealthy' } }];
  const session = createBridgeSession({ client: queueClient(all) });
  const tools = buildBridgeTools(session);

  session.enqueueEvent(all[0]);
  await callTool(tools, 'post_finding', { text: 'mine' });
  assert.equal(session.cursor, 4);
  assert.deepEqual(session.pending, []);

  const pulled = await callTool(tools, 'get_updates', {});
  assert.match(pulled, /No new shared context since seq 4/);
  assert.doesNotMatch(pulled, /origin pool unhealthy/); // delivered exactly once
});

test('a result with nothing owed is left exactly as the tool wrote it', async () => {
  const tools = buildBridgeTools(createBridgeSession({ client: queueClient() }));
  assert.equal(await callTool(tools, 'post_finding', { text: 'mine' }), 'Finding posted to the war room.');
});

test('a large backlog degrades to the newest events plus a recoverable count', async () => {
  const all = [];
  for (let seq = 0; seq < 60; seq += 1) all.push({ seq, type: 'edge.finding', payload: { text: `finding ${seq}` } });
  const session = createBridgeSession({ client: queueClient(all) });
  const tools = buildBridgeTools(session);

  for (const e of all) session.enqueueEvent(e); // 50 held, 10 dropped by the cap
  const text = await callTool(tools, 'post_finding', { text: 'mine' });

  assert.match(text, /⚠ 60 update\(s\) from other investigators/);
  // only the newest FLUSH_MAX are spelled out — never an unbounded timeline dump
  assert.equal(text.match(/^#\d+ edge\.finding/gm).length, 5);
  assert.match(text, /#59 edge\.finding — finding 59/);
  assert.doesNotMatch(text, /finding 54\b/);
  // ...and what it left out is counted, with the pull that recovers it
  assert.match(text, /\+55 earlier update\(s\) not shown \(edge\.finding×45\) — call get_updates with sinceSeq=-1 for the full detail\./);

  const recovered = await callTool(tools, 'get_updates', { sinceSeq: -1 });
  assert.match(recovered, /60 new event\(s\) since seq -1/);
});

test('vetting and claim events render their substance instead of a bare type', async () => {
  const session = createBridgeSession({ client: queueClient() });
  const tools = buildBridgeTools(session);

  session.enqueueEvent({ seq: 1, type: 'context.flagged', payload: { displayName: 'Dana', reason: 'that dashboard is stale' } });
  session.enqueueEvent({ seq: 2, type: 'context.voted', payload: { displayName: 'Sam', stance: 'concur' } });
  session.enqueueEvent({ seq: 3, type: 'claim.staged', payload: { displayName: 'Ana', statement: 'the rollout caused the 5xx' } });
  const text = await callTool(tools, 'post_finding', { text: 'mine' });

  assert.match(text, /#1 context\.flagged \[Dana\] — that dashboard is stale/);
  assert.match(text, /#2 context\.voted \[Sam\] — concur/);
  assert.match(text, /#3 claim\.staged \[Ana\] — the rollout caused the 5xx/);
});

test('describeEvent surfaces the same vetting fields on the stderr nudge', () => {
  assert.match(describeEvent({ type: 'context.flagged', payload: { displayName: 'Dana', reason: 'stale dashboard' } }), /from Dana: "stale dashboard"/);
  assert.match(describeEvent({ type: 'claim.staged', payload: { statement: 'the rollout caused the 5xx' } }), /"the rollout caused the 5xx"/);
  assert.match(describeEvent({ type: 'context.voted', payload: { stance: 'dissent' } }), /"dissent"/);
});

// ---- feature 188/#221: the push path, end to end, with no network ----------
//
// The tests above drive the queue through `enqueueEvent` directly. These drive
// it from the wire instead — a stand-in socket handed to `watchIncident` — so
// what is locked is the whole path an event actually travels: socket → filter →
// queue → tool result. `watchIncident` is wired exactly as `serve` wires it.

/** A socket.io stand-in: `fire()` plays the server's side of the connection. */
function fakeSocket() {
  const handlers = new Map();
  const socket = {
    emitted: [],
    disconnected: false,
    on(event, fn) { handlers.set(event, fn); return socket; },
    emit(event, payload) { socket.emitted.push([event, payload]); },
    disconnect() { socket.disconnected = true; },
    fire(event, payload) { handlers.get(event)?.(payload); },
  };
  return socket;
}

/** Wire a session to a stand-in socket the way `landfall serve` does. */
function watchWith(session, { ownInstanceId = 'a-1' } = {}) {
  const socket = fakeSocket();
  const logged = [];
  const stop = watchIncident(
    { baseUrl: 'http://api.test/', slug: 'acme', incidentId: 'inc-1', token: 'edge-tok' },
    {
      ownInstanceId: () => ownInstanceId,
      onEvent: (evt) => { session.enqueueEvent(evt); logged.push(describeEvent(evt)); },
      log: (line) => logged.push(line),
      ioImpl: () => socket,
    },
  );
  return { socket, logged, stop };
}

test('an event pushed over the socket reaches the agent on its next tool result', async () => {
  const session = createBridgeSession({ client: queueClient() });
  const tools = buildBridgeTools(session);
  const { socket, stop } = watchWith(session);

  socket.fire('connect');
  assert.deepEqual(socket.emitted, [['incident.subscribe', { incidentId: 'inc-1' }]]);

  socket.fire('incident.event', {
    seq: 4,
    type: 'edge.finding',
    payload: { agentInstanceId: 'a-2', displayName: 'Dana', edgeAgentLabel: 'Codex', text: 'origin pool unhealthy' },
  });
  assert.deepEqual(session.pending.map((e) => e.seq), [4]); // parked, not lost

  const text = await callTool(tools, 'post_finding', { text: 'mine' });
  assert.match(text, /Finding posted to the war room\./);
  assert.match(text, /#4 edge\.finding \[Dana · Codex\] — origin pool unhealthy/);

  stop();
  assert.equal(socket.disconnected, true);
});

test('the agent never has its own publications or presence plumbing pushed back at it', async () => {
  const session = createBridgeSession({ client: queueClient() });
  const tools = buildBridgeTools(session);
  const { socket } = watchWith(session, { ownInstanceId: 'a-1' });

  socket.fire('connect');
  socket.fire('incident.event', { seq: 5, type: 'edge.finding', payload: { agentInstanceId: 'a-1', text: 'mine, echoed back' } });
  socket.fire('incident.event', { seq: 6, type: 'edge.participant.heartbeat', payload: { agentInstanceId: 'a-2' } });
  socket.fire('incident.event', { seq: 7, type: 'edge.participant.joined', payload: { agentInstanceId: 'a-2' } });
  socket.fire('incident.event', null);

  assert.deepEqual(session.pending, []);
  // a peer's real finding still gets through — the filter is selective, not off
  socket.fire('incident.event', { seq: 8, type: 'edge.finding', payload: { agentInstanceId: 'a-2', text: 'theirs' } });
  assert.deepEqual(session.pending.map((e) => e.seq), [8]);

  const text = await callTool(tools, 'post_finding', { text: 'mine' });
  assert.doesNotMatch(text, /echoed back/);
  assert.doesNotMatch(text, /participant/);
  assert.match(text, /#8 edge\.finding — theirs/);
});

test('with realtime unavailable the tools behave exactly as they do today, and say nothing about it', async () => {
  const all = [{ seq: 0, type: 'edge.finding', payload: { text: 'pulled, not pushed' } }];
  const session = createBridgeSession({ client: queueClient(all) });
  const tools = buildBridgeTools(session);
  const { socket, logged } = watchWith(session);

  socket.fire('connect_error', new Error('ECONNREFUSED'));

  // the failure is a stderr line for the human, and nothing else
  assert.ok(logged.some((l) => /realtime unavailable \(ECONNREFUSED\).*get_updates pulls/.test(l)));
  assert.deepEqual(session.pending, []);
  assert.equal(session.pendingDropped, 0);

  // a plain tool result is byte-identical to the no-socket case...
  const posted = await callTool(tools, 'post_finding', { text: 'mine' });
  assert.equal(posted, 'Finding posted to the war room.');
  // ...and the cursor pull still carries everything, unchanged
  const pulled = await callTool(tools, 'get_updates', {});
  assert.match(pulled, /1 new event\(s\) since seq -1/);
  assert.match(pulled, /pulled, not pushed/);
  assert.doesNotMatch(pulled, /ECONNREFUSED|unavailable|error/i);
  assert.equal(session.cursor, 0);
});

test('client.getUpdates hits the sinceSeq delta endpoint', async () => {
  const f = fakeFetch({ '/o/acme/incidents/inc-1/events': { status: 200, json: [] } });
  const c = new EdgeBridgeClient(CFG, f);
  await c.getUpdates(7);
  assert.match(f.calls[0].url, /\/events\?sinceSeq=7$/);
});

// ---- feature 029/034 from the edge: vetting + claims tools (#248) -----------

/** A client that records the vetting/claims calls a tool made. */
function vettingClient() {
  const calls = [];
  return {
    calls,
    agentInstanceId: 'a-9',
    async heartbeat(doing) { calls.push(['heartbeat', doing]); },
    async contribute(kind, body) { calls.push(['contribute', kind, body]); },
    async getBrief() { return []; },
    async flagContext(targetSeq, reason) { calls.push(['flag', targetSeq, reason]); return {}; },
    async positionClaim(claimSeq, position, reason) { calls.push(['position', claimSeq, position, reason]); return {}; },
    async stageClaim(body) { calls.push(['stage', body]); return {}; },
  };
}

test('client routes flag/position/stage through the existing vetting + claims controllers', async () => {
  const f = fakeFetch({ '/o/acme/incidents/inc-1/edge/join': { status: 201, json: { agentInstanceId: 'a-9' } } });
  const c = new EdgeBridgeClient(CFG, f);
  await c.join();
  await c.flagContext(12, 'origin was healthy at that timestamp');
  await c.positionClaim(48, 'corroborate', 'reproduced locally');
  await c.stageClaim({ claimClass: 'causal', statement: 'the rollback caused the 5xx' });

  const seen = f.calls.map((x) => `${x.method} ${new URL(x.url).pathname}`);
  assert.ok(seen.includes('POST /o/acme/incidents/inc-1/vetting/flag'));
  assert.ok(seen.includes('POST /o/acme/incidents/inc-1/claims/48/position'));
  assert.ok(seen.includes('POST /o/acme/incidents/inc-1/claims'));

  // the server-issued instance id rides along on all three so the server can
  // attribute the position to {human · agent}; nothing asserts an identity.
  for (const call of f.calls.slice(1)) {
    assert.equal(call.body.agentInstanceId, 'a-9');
    assert.equal(call.body.edgeAgentLabel, 'Claude Code');
    assert.equal(call.body.humanActorId, undefined);
    assert.equal(call.body.displayName, undefined);
    assert.equal(call.body.kind, undefined);
  }
  assert.equal(f.calls[1].body.reason, 'origin was healthy at that timestamp');
  assert.equal(f.calls[2].body.position, 'corroborate');
  assert.equal(f.calls[3].body.claimClass, 'causal');
});

test('the four vetting tools are exposed and narrate like every other bridge tool', async () => {
  const client = vettingClient();
  const tools = buildBridgeTools(client);
  const names = tools.map((t) => t.name);
  for (const n of ['flag_context', 'corroborate_claim', 'contest_claim', 'stage_claim']) {
    assert.ok(names.includes(n), `missing tool ${n}`);
  }

  await callTool(tools, 'flag_context', { targetSeq: 12, reason: 'origin was healthy then' });
  assert.deepEqual(client.calls.filter(([k]) => k === 'flag'), [['flag', 12, 'origin was healthy then']]);
  // narrated() wrapping: presence heartbeat on every call, like the rest
  assert.ok(client.calls.some(([k, doing]) => k === 'heartbeat' && /flagging #12/.test(doing)));
});

test('vetting tools never double-post a generic contribution (the endpoints append their own events)', async () => {
  const client = vettingClient();
  const tools = buildBridgeTools(client);
  await callTool(tools, 'flag_context', { targetSeq: 3, reason: 'r' });
  await callTool(tools, 'corroborate_claim', { claimSeq: 48 });
  await callTool(tools, 'contest_claim', { claimSeq: 49, reason: 'bisect says otherwise' });
  await callTool(tools, 'stage_claim', { claimClass: 'observation', statement: 'p99 rose at 14:02' });

  assert.equal(client.calls.filter(([k]) => k === 'contribute').length, 0);
  for (const name of ['flag_context', 'corroborate_claim', 'contest_claim', 'stage_claim']) {
    assert.equal(contributionFor(name, {}), null);
  }
});

test('corroborate/contest are one endpoint with opposite stances', async () => {
  const client = vettingClient();
  const tools = buildBridgeTools(client);
  const yes = await callTool(tools, 'corroborate_claim', { claimSeq: 48, reason: 'reproduced' });
  const no = await callTool(tools, 'contest_claim', { claimSeq: 48, reason: 'not on my build' });
  assert.deepEqual(
    client.calls.filter(([k]) => k === 'position'),
    [['position', 48, 'corroborate', 'reproduced'], ['position', 48, 'contest', 'not on my build']],
  );
  // both results tell the agent its position is not the decision
  assert.match(yes, /needs a human in the chain/);
  assert.match(no, /needs a human in the chain/);
});

test('tool descriptions state plainly that a vote is visibility-only and needs a human quorum', () => {
  const byName = Object.fromEntries(buildBridgeTools(vettingClient()).map((t) => [t.name, t.description]));
  assert.match(byName.flag_context, /VISIBILITY, never truth/);
  assert.match(byName.flag_context, /agents alone can never quarantine/i);
  assert.match(byName.corroborate_claim, /requires a human in the chain/i);
  assert.match(byName.corroborate_claim, /never counts the claim author corroborating themselves/i);
  assert.match(byName.contest_claim, /decides nothing/i);
  assert.match(byName.stage_claim, /NOT in the room feed/);
  // and the standing instructions carry the same rule, since that is what the
  // model reads before it ever looks at a tool schema
  assert.match(EDGE_AGENT_INSTRUCTIONS, /POSITION, never a decision/);
});

test('a malformed seq is refused locally and never reaches the server', async () => {
  const client = vettingClient();
  const tools = buildBridgeTools(client);
  assert.match(await callTool(tools, 'flag_context', { targetSeq: 'twelve', reason: 'r' }), /Nothing flagged/);
  assert.match(await callTool(tools, 'stage_claim', { claimClass: 'causal', statement: '  ' }), /Nothing staged/);
  // An ABSENT seq must not coerce into one. `Number(null)` and `Number('')` are
  // both 0, and 0 is a real seq — so the naive check would have turned a tool
  // call that forgot the argument into a position on the incident's first
  // event. That is the one failure this surface must never have.
  for (const missing of [null, undefined, '', {}]) {
    assert.match(await callTool(tools, 'corroborate_claim', { claimSeq: missing }), /No position recorded/);
    assert.match(await callTool(tools, 'flag_context', { targetSeq: missing, reason: 'r' }), /Nothing flagged/);
  }
  assert.deepEqual(client.calls.filter(([k]) => k !== 'heartbeat'), []);

  // ...while seq 0 itself, explicitly given, is a legitimate target.
  assert.match(await callTool(tools, 'corroborate_claim', { claimSeq: 0 }), /claim #0/);
  assert.deepEqual(client.calls.filter(([k]) => k === 'position'), [['position', 0, 'corroborate', undefined]]);
});

test('a server refusal is relayed with its reason, and claims nothing was recorded', async () => {
  const client = vettingClient();
  client.flagContext = async () => { throw new Error('/vetting/flag → HTTP 400: only chat messages and findings can be flagged'); };
  const tools = buildBridgeTools(client);
  const out = await callTool(tools, 'flag_context', { targetSeq: 5, reason: 'r' });
  assert.match(out, /only chat messages and findings can be flagged/);
  assert.match(out, /Nothing flagged/);
});
