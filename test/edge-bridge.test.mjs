// edge-bridge.test.mjs — feature-012 gate (node --test, ESM-native).
import test from 'node:test';
import assert from 'node:assert/strict';
import { EdgeBridgeClient } from '../src/client.mjs';
import { handleMcpMessage } from '../src/mcp.mjs';
import { buildBridgeTools } from '../src/tools.mjs';
import { narrateDoing, contributionFor } from '../src/narrate.mjs';

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
