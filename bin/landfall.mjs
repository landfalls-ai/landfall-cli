#!/usr/bin/env node
// landfall — the Edge Bridge CLI (features 012 + 021). One command for a teammate to
// join a war room from their own computer and have their favorite agent's local
// investigation narrate itself into the room — and stay in sync with everyone
// else's findings, live.
//
// Zero-config setup (021): add the MCP server once, with NO tokens or IDs —
//   { "command": "landfall", "args": ["serve"] }
// then paste the room's "Share with agent" instruction block into your agent; it
// calls join_war_room(shareUrl) and starts investigating. Or pre-join with the
// link directly:
//   landfall serve --link "https://…/o/acme/incidents/inc-1/agent?ticket=…"
//   landfall join  "https://…/agent?ticket=…"        (presence-only keep-alive)
//
// Env-config (012) still works: LANDFALL_BASE_URL / LANDFALL_SLUG / LANDFALL_INCIDENT /
// LANDFALL_TOKEN (+ LANDFALL_AGENT_LABEL). LANDFALL_LINK is the env form of --link;
// LANDFALL_BASE_URL overrides the link origin (dev/tests where web ≠ API origin).
//
// Commands:
//   landfall serve [--link URL]   MCP tools over stdio (default; auto-joins if configured)
//   landfall join [URL]           join + heartbeat (keep-alive presence, no MCP)
//   landfall note "<text>"        post a one-off finding, then exit
//   landfall leave                leave the incident
import { EdgeBridgeClient } from '../src/client.mjs';
import { buildBridgeTools, createBridgeSession, EDGE_AGENT_INSTRUCTIONS } from '../src/tools.mjs';
import { runStdioServer } from '../src/mcp.mjs';
import { redeemShareLink } from '../src/link.mjs';
import { watchIncident, describeEvent } from '../src/live.mjs';

// log to STDERR so stdout stays a clean MCP JSON-RPC channel
function log(...a) { process.stderr.write(`[landfall] ${a.join(' ')}\n`); }

const HEARTBEAT_MS = 15_000;

/** argv: [cmd] [positional-link] [--link URL] */
function parseArgs(argv) {
  const args = [...argv];
  let link;
  const i = args.indexOf('--link');
  if (i >= 0) { link = args[i + 1]; args.splice(i, 2); }
  const cmd = args[0] && !/^https?:\/\//.test(args[0]) ? args.shift() : 'serve';
  if (!link && args[0] && /^https?:\/\//.test(args[0])) link = args.shift();
  return { cmd, link: link ?? process.env.LANDFALL_LINK, rest: args };
}

/** Env-based config (012). Returns null when incomplete (021 allows link/lazy join). */
function envConfig() {
  const env = process.env;
  const cfg = {
    baseUrl: env.LANDFALL_BASE_URL ?? 'http://localhost:3001',
    slug: env.LANDFALL_SLUG,
    incidentId: env.LANDFALL_INCIDENT,
    token: env.LANDFALL_TOKEN,
    agentLabel: env.LANDFALL_AGENT_LABEL ?? 'edge-agent',
  };
  return cfg.slug && cfg.incidentId && cfg.token ? cfg : null;
}

/** Resolve a joinable config from the link (redeemed) or the env, else null. */
async function resolveConfig(link) {
  const agentLabel = process.env.LANDFALL_AGENT_LABEL ?? 'edge-agent';
  if (link) {
    const cfg = await redeemShareLink(link, { baseUrl: process.env.LANDFALL_BASE_URL });
    log(`magic link redeemed — incident ${cfg.incidentId} (workspace ${cfg.slug}).`);
    return { ...cfg, agentLabel };
  }
  return envConfig();
}

/** Presence keep-alive + live watch for a joined client. Returns stop(). */
function keepLive(client, cfg) {
  const beat = setInterval(() => { client.heartbeat('investigating').catch(() => {}); }, HEARTBEAT_MS);
  const unwatch = watchIncident(cfg, {
    ownInstanceId: () => client.agentInstanceId,
    onEvent: (evt) => log(describeEvent(evt)),
    log,
  });
  return () => { clearInterval(beat); unwatch(); };
}

async function main() {
  const { cmd, link, rest } = parseArgs(process.argv.slice(2));

  if (cmd === 'leave') {
    const cfg = await resolveConfig(link);
    if (!cfg) { log('nothing to leave — no link or LANDFALL_* config.'); return; }
    const client = new EdgeBridgeClient(cfg);
    await client.join().catch(() => {});
    await client.leave().catch(() => {});
    log('left the incident.');
    return;
  }

  if (cmd === 'note') {
    const cfg = await resolveConfig(link);
    if (!cfg) { log('set LANDFALL_* env or pass --link to post a note.'); process.exit(2); }
    const client = new EdgeBridgeClient(cfg);
    await client.join();
    const text = rest.join(' ');
    await client.heartbeat(`noting: ${text}`);
    await client.contribute('finding', { text });
    log('note posted.');
    await client.leave().catch(() => {});
    return;
  }

  if (cmd === 'join') {
    const cfg = await resolveConfig(link);
    if (!cfg) { log('pass an agent share link: landfall join "<url>" (or set LANDFALL_* env).'); process.exit(2); }
    const client = new EdgeBridgeClient(cfg);
    await client.join();
    log(`joined incident ${cfg.incidentId} as "${cfg.agentLabel}" (instance ${client.agentInstanceId}).`);
    const stop = keepLive(client, cfg);
    const shutdown = async () => { stop(); await client.leave().catch(() => {}); process.exit(0); };
    process.on('SIGINT', shutdown);
    process.on('SIGTERM', shutdown);
    log('presence keep-alive running (Ctrl-C to leave).');
    return; // heartbeat interval keeps the process alive
  }

  // default: serve — expose incident MCP tools over stdio; each call narrates.
  let stopLive = null;
  const session = createBridgeSession({
    agentLabel: process.env.LANDFALL_AGENT_LABEL ?? 'edge-agent',
    baseUrl: process.env.LANDFALL_BASE_URL,
    onJoined: (s, cfg) => {
      stopLive?.();
      stopLive = keepLive(s.client, cfg);
      log(`joined incident ${cfg.incidentId} as "${s.agentLabel}" (instance ${s.client.agentInstanceId}).`);
    },
  });

  const cfg = await resolveConfig(link);
  if (cfg) {
    const client = new EdgeBridgeClient(cfg);
    await client.join();
    session.client = client;
    stopLive = keepLive(client, cfg);
    log(`joined incident ${cfg.incidentId} as "${cfg.agentLabel}" (instance ${client.agentInstanceId}).`);
  } else {
    log('not joined yet — the agent should call join_war_room with a Landfall share link.');
  }

  const shutdown = async () => {
    stopLive?.();
    await session.client?.leave().catch(() => {});
    process.exit(0);
  };
  process.on('SIGINT', shutdown);
  process.on('SIGTERM', shutdown);

  log('MCP stdio server ready — connect your agent. Every tool call narrates to the war room.');
  runStdioServer(buildBridgeTools(session), {
    serverInfo: { name: 'landfall', version: '0.2.0' },
    instructions: EDGE_AGENT_INSTRUCTIONS,
  });
}

main().catch((e) => { log(`fatal: ${e.message}`); process.exit(1); });
