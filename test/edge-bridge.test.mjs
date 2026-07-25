// edge-bridge.test.mjs — feature-012 gate + feature-021 magic-link/live-sync units
// (node --test, ESM-native).
import test from 'node:test';
import assert from 'node:assert/strict';
import { EdgeBridgeClient } from '../src/client.mjs';
import { handleMcpMessage } from '../src/mcp.mjs';
import { buildBridgeTools, createBridgeSession, EDGE_AGENT_INSTRUCTIONS } from '../src/tools.mjs';
import { narrateDoing, contributionFor } from '../src/narrate.mjs';
import { parseShareLink, redeemShareLink } from '../src/link.mjs';

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

test('client.getUpdates hits the sinceSeq delta endpoint', async () => {
  const f = fakeFetch({ '/o/acme/incidents/inc-1/events': { status: 200, json: [] } });
  const c = new EdgeBridgeClient(CFG, f);
  await c.getUpdates(7);
  assert.match(f.calls[0].url, /\/events\?sinceSeq=7$/);
});
