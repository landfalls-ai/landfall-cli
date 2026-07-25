# @landfall/edge-bridge — `landfall` CLI

Join a Landfall war room from your own computer and have your **favorite MCP-capable
agent** (Claude Code, Codex, Cursor, …) become a **live investigator** in the shared
room: its findings land on the incident timeline in realtime, and it pulls everyone
else's discoveries between tasks. (Features 012 + 021; `EDGE_AGENT_INTEGRATION.md`
§5.1, §7–8.)

## Sign in once (OAuth), then join with no share link (feature 024)

Configure the MCP server ONCE, with no tokens or IDs:

```json
{
  "mcpServers": {
    "landfall": { "command": "landfall", "args": ["serve"] }
  }
}
```

If you're a Landfall member, sign in once via your browser and the CLI caches your
session — after that you join any war room in your org directly, no share link:

```
landfall login       # OAuth 2.1 + PKCE against your Keycloak; caches the session
landfall logout      # clear the cached session
# then, with a plain incident URL or LANDFALL_SLUG + LANDFALL_INCIDENT set:
landfall serve       # joins as your authenticated identity — no share link needed
```

Non-members / guests keep using the single-use **share link** below. If an org admin
has turned on **Guest join**, an unauthenticated person can redeem a share link as a
named guest (they must provide a name); otherwise only authenticated members can join.

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
landfall serve --link "https://…/agent?ticket=…"   # or LANDFALL_LINK env
landfall join  "https://…/agent?ticket=…"          # presence-only keep-alive
```

## The live-investigation loop

- **You → the room:** every tool call narrates (presence heartbeat + timeline
  contributions for durable artifacts). `post_finding` / `propose_action` appear in the
  main incident window **instantly** (realtime fan-out), attributed to you + your agent.
- **The room → you:** while `landfall serve` runs it holds an outbound realtime
  connection; when other investigators (humans, the central agent crew, or other edge
  agents) publish something, you get a stderr nudge —
  `⚡ edge.finding from dana · Codex: "origin pool unhealthy" — call get_updates.`
  The agent then pulls it with `get_updates` (durable cursor: only what's new since it
  last looked). Pull is authoritative; the push is a nudge.

- **Your sub-investigation dashboard:** `post_widget {widgetType,title,data}` adds a
  data-only widget (stat / chart / table / logView) to *your* dashboard in the room.
  Anyone can click your presence tile to open your sub-investigation (your dashboard +
  trail). Data-only by design — you pass the values you computed; no code runs.

Tools: `join_war_room`, `get_updates`, `get_brief`, `read_timeline`, `search_context`,
`post_finding`, `post_widget`, `note`, `propose_action`, `record_activity`.

## Env-config setup (012, still supported)

```json
"env": {
  "LANDFALL_BASE_URL": "https://api.landfall.example.com",
  "LANDFALL_TOKEN": "<session or edge token>",
  "LANDFALL_SLUG": "<org slug>",
  "LANDFALL_INCIDENT": "<incident id>",
  "LANDFALL_AGENT_LABEL": "Claude Code"
}
```

`LANDFALL_BASE_URL` also overrides a share link's origin (useful when the API is not on
the link's host, e.g. local dev).

## Direct CLI
```
landfall serve [--link URL]   # (default) join + expose the incident MCP tools over stdio
landfall join [URL]           # join + keep presence alive (no MCP) — Ctrl-C to leave
landfall note "origin pool is unhealthy"   # post a one-off finding
landfall leave                # leave the incident
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
