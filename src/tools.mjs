// tools.mjs — the incident-scoped MCP tools the local agent sees. Each tool routes
// to the EdgeBridgeClient AND narrates the teammate's activity into the war room
// (a presence heartbeat every call + a timeline contribution for durable
// artifacts). This is the seam that makes an edge investigation self-narrate with
// zero extra effort: the agent just uses its tools; the war room shows who's doing
// what, live.

import { contributionFor, narrateDoing } from './narrate.mjs';

/** Build the MCP tool set bound to a joined EdgeBridgeClient. */
export function buildBridgeTools(client) {
  // wrap a handler so EVERY tool call heartbeats a "doing" + posts a contribution
  // if the action produced a durable artifact.
  const narrated = (name, run) => async (args) => {
    const doing = narrateDoing(name, args);
    try { await client.heartbeat(doing); } catch { /* narration is best-effort */ }
    const contrib = contributionFor(name, args);
    if (contrib) { try { await client.contribute(contrib.kind, contrib.body); } catch { /* best-effort */ } }
    return run(args, doing);
  };

  return [
    {
      name: 'get_brief',
      description: 'Get the current incident brief (the shared war-room timeline).',
      inputSchema: { type: 'object', properties: {} },
      handler: narrated('get_brief', async () => {
        const events = await client.getBrief();
        return `Incident timeline: ${Array.isArray(events) ? events.length : 0} events. ${summarize(events)}`;
      }),
    },
    {
      name: 'read_timeline',
      description: 'Read the full incident timeline (raw events).',
      inputSchema: { type: 'object', properties: {} },
      handler: narrated('read_timeline', async () => JSON.stringify(await client.getBrief())),
    },
    {
      name: 'search_context',
      description: 'Search the incident context for a term (narrated to the room).',
      inputSchema: { type: 'object', properties: { query: { type: 'string' } }, required: ['query'] },
      handler: narrated('search_context', async (a) => {
        const events = await client.getBrief();
        const q = String(a.query ?? '').toLowerCase();
        const hits = (Array.isArray(events) ? events : []).filter((e) => JSON.stringify(e).toLowerCase().includes(q));
        return `${hits.length} matching event(s) for "${a.query}".`;
      }),
    },
    {
      name: 'post_finding',
      description: 'Post a finding to the shared war-room timeline (attributed to you).',
      inputSchema: { type: 'object', properties: { text: { type: 'string' }, resource: { type: 'string' } }, required: ['text'] },
      handler: narrated('post_finding', () => 'Finding posted to the war room.'),
    },
    {
      name: 'note',
      description: 'Post a quick note/observation to the war room.',
      inputSchema: { type: 'object', properties: { text: { type: 'string' } }, required: ['text'] },
      handler: narrated('note', () => 'Note posted.'),
    },
    {
      name: 'propose_action',
      description: 'Propose a remediation (propose-only; a human approves — you cannot execute).',
      inputSchema: { type: 'object', properties: { description: { type: 'string' }, dryRunPreview: { type: 'string' } }, required: ['description'] },
      handler: narrated('propose_action', () => 'Remediation proposed — awaiting human approval.'),
    },
    {
      name: 'record_activity',
      description: 'Tell the war room what you are currently doing (narration only).',
      inputSchema: { type: 'object', properties: { doing: { type: 'string' } }, required: ['doing'] },
      handler: narrated('record_activity', (a) => `Recorded: ${a.doing}`),
    },
  ];
}

function summarize(events) {
  if (!Array.isArray(events) || !events.length) return 'No activity yet.';
  const types = {};
  for (const e of events) types[e.type] = (types[e.type] ?? 0) + 1;
  return Object.entries(types).map(([t, n]) => `${t}×${n}`).join(', ');
}
