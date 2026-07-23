#!/usr/bin/env node
// warroom — the Edge Bridge CLI (feature 012). One command for a teammate to join
// a war room from their own computer and have their favorite agent's local
// investigation narrate itself into the room.
//
// Seamless setup — add to your agent's MCP config:
//   { "command": "warroom", "args": ["serve"],
//     "env": { "WARROOM_BASE_URL": "...", "WARROOM_TOKEN": "...",
//              "WARROOM_SLUG": "...", "WARROOM_INCIDENT": "...",
//              "WARROOM_AGENT_LABEL": "Claude Code" } }
//
// Commands:
//   warroom serve            join + expose the incident MCP tools over stdio (default)
//   warroom join             join + heartbeat (keep-alive presence, no MCP)
//   warroom note "<text>"    post a one-off finding, then exit
//   warroom leave            leave the incident
import { EdgeBridgeClient } from '../src/client.mjs';
import { buildBridgeTools } from '../src/tools.mjs';
import { runStdioServer } from '../src/mcp.mjs';

function config() {
  const env = process.env;
  const cfg = {
    baseUrl: env.WARROOM_BASE_URL ?? 'http://localhost:3001',
    slug: env.WARROOM_SLUG,
    incidentId: env.WARROOM_INCIDENT,
    token: env.WARROOM_TOKEN,
    agentLabel: env.WARROOM_AGENT_LABEL ?? 'edge-agent',
  };
  for (const k of ['slug', 'incidentId', 'token']) {
    if (!cfg[k]) { log(`missing WARROOM_${k === 'incidentId' ? 'INCIDENT' : k.toUpperCase()} — set it in the environment`); process.exit(2); }
  }
  return cfg;
}

// log to STDERR so stdout stays a clean MCP JSON-RPC channel
function log(...a) { process.stderr.write(`[warroom] ${a.join(' ')}\n`); }

const HEARTBEAT_MS = 15_000;

async function main() {
  const cmd = process.argv[2] ?? 'serve';
  const cfg = config();
  const client = new EdgeBridgeClient(cfg);

  if (cmd === 'leave') {
    await client.join().catch(() => {});
    await client.leave().catch(() => {});
    log('left the incident.');
    return;
  }

  await client.join();
  log(`joined incident ${cfg.incidentId} as "${cfg.agentLabel}" (instance ${client.agentInstanceId}).`);

  if (cmd === 'note') {
    const text = process.argv.slice(3).join(' ');
    await client.heartbeat(`noting: ${text}`);
    await client.contribute('finding', { text });
    log('note posted.');
    await client.leave().catch(() => {});
    return;
  }

  // keep presence fresh while the teammate investigates
  const beat = setInterval(() => { client.heartbeat('investigating').catch(() => {}); }, HEARTBEAT_MS);
  const shutdown = async () => { clearInterval(beat); await client.leave().catch(() => {}); process.exit(0); };
  process.on('SIGINT', shutdown);
  process.on('SIGTERM', shutdown);

  if (cmd === 'join') {
    log('presence keep-alive running (Ctrl-C to leave).');
    return; // interval keeps the process alive
  }

  // default: serve — expose incident MCP tools over stdio; each call narrates.
  log('MCP stdio server ready — connect your agent. Every tool call narrates to the war room.');
  runStdioServer(buildBridgeTools(client), { serverInfo: { name: 'landfall-warroom', version: '0.1.0' } });
}

main().catch((e) => { log(`fatal: ${e.message}`); process.exit(1); });
