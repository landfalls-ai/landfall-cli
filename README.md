# @landfall/edge-bridge — `warroom` CLI

Join a Landfall war room from your own computer and have your **favorite MCP-capable
agent** (Claude Code, Codex, Cursor, …) become a **live investigator** in the shared
room: its findings land on the incident timeline in realtime, and it pulls everyone
else's discoveries between tasks. (Features 012 + 021; `EDGE_AGENT_INTEGRATION.md`
§5.1, §7–8.)

## Magic-link setup (recommended)

Configure the MCP server ONCE, with no tokens or IDs:

```json
{
  "mcpServers": {
    "warroom": { "command": "warroom", "args": ["serve"] }
  }
}
```

Then, when an incident happens, any room member clicks **Share with agent** in the war
room and sends you the instruction block. Paste it into your agent — it contains a
single-use share link like

```
https://api.landfall.example.com/o/acme/incidents/inc-4821/agent?ticket=…
```

and the agent calls `join_war_room(shareUrl)`. The bridge redeems the ticket for a
short-lived **edge session scoped to exactly that incident** (8h, nothing else), joins,
and starts investigating. The URL's ticket is single-use and expires in 15 minutes;
possessing the URL without it grants nothing.

You can also pre-join from the terminal:

```
warroom serve --link "https://…/agent?ticket=…"   # or WARROOM_LINK env
warroom join  "https://…/agent?ticket=…"          # presence-only keep-alive
```

## The live-investigation loop

- **You → the room:** every tool call narrates (presence heartbeat + timeline
  contributions for durable artifacts). `post_finding` / `propose_action` appear in the
  main incident window **instantly** (realtime fan-out), attributed to you + your agent.
- **The room → you:** while `warroom serve` runs it holds an outbound realtime
  connection; when other investigators (humans, the central agent crew, or other edge
  agents) publish something, you get a stderr nudge —
  `⚡ edge.finding from dana · Codex: "origin pool unhealthy" — call get_updates.`
  The agent then pulls it with `get_updates` (durable cursor: only what's new since it
  last looked). Pull is authoritative; the push is a nudge.

Tools: `join_war_room`, `get_updates`, `get_brief`, `read_timeline`, `search_context`,
`post_finding`, `note`, `propose_action`, `record_activity`.

## Env-config setup (012, still supported)

```json
"env": {
  "WARROOM_BASE_URL": "https://api.landfall.example.com",
  "WARROOM_TOKEN": "<session or edge token>",
  "WARROOM_SLUG": "<org slug>",
  "WARROOM_INCIDENT": "<incident id>",
  "WARROOM_AGENT_LABEL": "Claude Code"
}
```

`WARROOM_BASE_URL` also overrides a share link's origin (useful when the API is not on
the link's host, e.g. local dev).

## Direct CLI
```
warroom serve [--link URL]   # (default) join + expose the incident MCP tools over stdio
warroom join [URL]           # join + keep presence alive (no MCP) — Ctrl-C to leave
warroom note "origin pool is unhealthy"   # post a one-off finding
warroom leave                # leave the incident
```

## Guarantees
- **Read-only by default**; `propose_action` is propose-only (a human approves — you
  cannot execute). humanActorId comes from your verified session, never the payload.
- An edge session token works ONLY on its one incident — default-deny everywhere else.
- Join tickets are single-use, short-lived, and bound to tenant + incident + member.
- Stdio only — the bridge never listens on a public interface; the realtime connection
  is outbound.
- Presence/summary/sub-tabs are projected server-side (feature 006) from what the
  bridge posts; nothing here bypasses the war room's approval gate.
