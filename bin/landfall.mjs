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
//   landfall login                OAuth 2.1 + PKCE sign-in (browser); caches the session
//   landfall logout               clear the cached session
//   landfall serve [--link URL]   MCP tools over stdio (default; auto-joins if configured)
//   landfall join [URL]           join + heartbeat (keep-alive presence, no MCP)
//   landfall note "<text>"        post a one-off finding, then exit
//   landfall leave                leave the incident
//   landfall install [--yes] [--only <ids>] [--dry-run]   register this machine's coding
//                                 agents (feature 049 — see contracts/cli.md)
//   landfall uninstall [--yes] [--only <ids>]              remove that registration
//   landfall hooks install [--only <ids>] [--dry-run] [--uninstall]   register the
//                                 lifecycle hooks that push room context into a
//                                 local session (#222; story #190)
//
// Auth (024): once `landfall login` has cached a token, `serve`/`join` can join a
// war room from a plain incident URL or LANDFALL_SLUG+LANDFALL_INCIDENT with NO
// share link — you join as your authenticated member identity.
import { EdgeBridgeClient } from '../src/client.mjs';
import { buildBridgeTools, createBridgeSession, EDGE_AGENT_INSTRUCTIONS } from '../src/tools.mjs';
import { runStdioServer } from '../src/mcp.mjs';
import { redeemShareLink } from '../src/link.mjs';
import { watchIncident, describeEvent } from '../src/live.mjs';
import { login, logout, getCachedAccessToken, explainExpiredCredential } from '../src/auth.mjs';
import { runInstall, runUninstall } from '../src/install/commands.mjs';
import { formatOutcomeLine } from '../src/install/report.mjs';
import { runHooksInstall, runHooksUninstall, parseHookFlags } from '../src/hooks/commands.mjs';
import { runHookEvent, HOOK_EVENT_IDS } from '../src/hooks/run.mjs';

/** Parse slug + incidentId from a plain incident URL (no ticket) for the OAuth path. */
function parseIncidentUrl(url) {
  try {
    const m = new URL(url).pathname.match(/\/o\/([^/]+)\/incidents\/([^/]+)/);
    if (m) return { slug: m[1], incidentId: m[2] };
  } catch { /* not a URL */ }
  return null;
}

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

/**
 * Resolve a joinable config, in priority order:
 *  1. a share link WITH a ticket → redeem it (guest / non-member path, 021);
 *  2. explicit env config (WARROOM/LANDFALL_TOKEN, 012);
 *  3. OAuth (024): a cached login token + an incident target (a plain incident
 *     URL or LANDFALL_SLUG+LANDFALL_INCIDENT) → join as the authenticated member
 *     with NO share link.
 * Returns null when none apply.
 */
async function resolveConfig(link) {
  const agentLabel = process.env.LANDFALL_AGENT_LABEL ?? 'edge-agent';
  const baseUrl = process.env.LANDFALL_BASE_URL;
  if (link && /[?&]ticket=/.test(link)) {
    const cfg = await redeemShareLink(link, { baseUrl });
    log(`magic link redeemed — incident ${cfg.incidentId} (workspace ${cfg.slug}).`);
    return { ...cfg, agentLabel };
  }
  const env = envConfig();
  if (env) return env;
  const target =
    (link && parseIncidentUrl(link)) ||
    (process.env.LANDFALL_SLUG && process.env.LANDFALL_INCIDENT
      ? { slug: process.env.LANDFALL_SLUG, incidentId: process.env.LANDFALL_INCIDENT }
      : null);
  if (target) {
    const token = await getCachedAccessToken();
    if (token) {
      log(`authenticated — joining incident ${target.incidentId} (workspace ${target.slug}) with no share link.`);
      return { baseUrl: baseUrl ?? 'http://localhost:3001', slug: target.slug, incidentId: target.incidentId, token, agentLabel };
    }
    // Feature 043 (T061, FR-055): an EXPIRED credential — including a legacy
    // Keycloak one this CLI can no longer refresh — produces an explicit
    // instruction, never a silent failure. A CLI that quietly stops working
    // during an incident is worse than one that says what to do.
    const expired = await explainExpiredCredential();
    log(expired ?? 'not signed in — run `landfall login`, or paste a share link.');
  }
  return null;
}

/**
 * Presence keep-alive + live watch for a joined client. Returns stop().
 * When a bridge session is given, each pushed event is also parked on it so the
 * agent's next tool call can carry it — the stderr line only reaches a human
 * who happens to be watching the terminal.
 */
function keepLive(client, cfg, session) {
  const beat = setInterval(() => { client.heartbeat('investigating').catch(() => {}); }, HEARTBEAT_MS);
  const unwatch = watchIncident(cfg, {
    ownInstanceId: () => client.agentInstanceId,
    onEvent: (evt) => {
      session?.enqueueEvent(evt);
      log(describeEvent(evt));
    },
    log,
  });
  return () => { clearInterval(beat); unwatch(); };
}

const HELP_TEXT = `landfall — join a Landfall war room from your terminal

Usage: landfall <command> [options]

Commands:
  login                                                  sign in (browser); caches the session
  logout                                                 clear the cached session
  serve [--link URL]                                     (default) join + expose incident MCP tools over stdio
  join [URL]                                             join + keep presence alive (no MCP) — Ctrl-C to leave
  note "<text>"                                          post a one-off finding, then exit
  leave                                                   leave the incident
  install [--yes] [--only <ids>] [--dry-run]              register this machine's coding agents
  uninstall [--yes] [--only <ids>]                        remove that registration
  hooks install [--only <ids>] [--dry-run] [--uninstall]  register lifecycle hooks so room context
                                                          reaches a local session it can't ignore
  hooks uninstall [--only <ids>]                          remove only landfall's hook entries

Run 'landfall <command>' with no further arguments for command-specific behavior.
Docs: https://github.com/landfalls-ai/landfall-cli`;

async function main() {
  const argv = process.argv.slice(2);
  if (argv.includes('--help') || argv.includes('-h') || argv[0] === 'help') {
    console.log(HELP_TEXT);
    return;
  }

  const { cmd, link, rest } = parseArgs(argv);

  if (cmd === 'login') {
    await login(log);
    log('signed in — session cached. You can now join a war room with no share link.');
    return;
  }

  if (cmd === 'logout') {
    await logout();
    log('signed out — cleared the cached session.');
    return;
  }

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

  if (cmd === 'hooks') {
    const { rest: subArgs, uninstall: uninstallFlag } = parseHookFlags(rest);
    const sub = subArgs[0];

    // `landfall hooks <event>` is what a registered hook entry itself runs, so
    // it must stay silent on stdout (the host parses that channel) and say
    // everything it has to say through the exit code.
    if (HOOK_EVENT_IDS.includes(sub)) {
      const { exitCode, error } = await runHookEvent(sub);
      if (error) log(error);
      process.exitCode = exitCode;
      return;
    }

    if (sub !== 'install' && sub !== 'uninstall') {
      log(`usage: landfall hooks <install|uninstall|${HOOK_EVENT_IDS.join('|')}> [--only <ids>] [--dry-run] [--uninstall]`);
      process.exitCode = 2;
      return;
    }

    const run = sub === 'uninstall' || uninstallFlag ? runHooksUninstall : runHooksInstall;
    const result = await run(rest, { log });
    if (result.usageError) {
      log(result.usageError);
      process.exitCode = result.exitCode;
      return;
    }
    for (const o of result.outcomes) console.log(formatOutcomeLine(o));
    process.exitCode = result.exitCode; // not process.exit — see the note below
    return;
  }

  if (cmd === 'install' || cmd === 'uninstall') {
    const run = cmd === 'install' ? runInstall : runUninstall;
    const result = await run(rest, { log });
    if (result.usageError) {
      log(result.usageError);
      process.exitCode = result.exitCode;
      return;
    }
    for (const o of result.outcomes) console.log(formatOutcomeLine(o));
    // NOT process.exit(): stdout is a pipe when scripted/tested, and exiting
    // immediately after console.log can truncate the write before it flushes
    // (worse right after a readline prompt, which leaves the stream in a
    // state where this raced reliably). Setting exitCode and returning lets
    // Node drain stdout before the process actually exits.
    process.exitCode = result.exitCode;
    return;
  }

  // default: serve — expose incident MCP tools over stdio; each call narrates.
  let stopLive = null;
  const session = createBridgeSession({
    agentLabel: process.env.LANDFALL_AGENT_LABEL ?? 'edge-agent',
    baseUrl: process.env.LANDFALL_BASE_URL,
    onJoined: (s, cfg) => {
      stopLive?.();
      stopLive = keepLive(s.client, cfg, s);
      log(`joined incident ${cfg.incidentId} as "${s.agentLabel}" (instance ${s.client.agentInstanceId}).`);
    },
  });

  const cfg = await resolveConfig(link);
  if (cfg) {
    const client = new EdgeBridgeClient(cfg);
    await client.join();
    session.client = client;
    stopLive = keepLive(client, cfg, session);
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
